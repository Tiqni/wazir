# Wazir Retries & Backoff Design

**Status:** Design approved; ready for implementation plan.
**Date:** 2026-07-05
**Branch:** `retries-backoff` (off `main`).

## Goal

Today any transient infrastructure blip during a phase — a GitHub `502`, a
rate-limit, a `git push` network hiccup, a `claude` process that failed to spawn —
propagates as a plain `error` and sends the card straight to **Failed** (via
`Worker.fail`). Worse, a transient `board.GetCard` failure at the top of
`Worker.Process` *propagates past* `fail` and the event is **silently dropped**
(logged by the queue, never retried, never surfaced on the board).

This design makes transient failures self-healing. A momentary GitHub / git /
`claude`-transport error is retried with bounded exponential backoff instead of
nuking the card. **Deterministic, model-reported failures still go to Failed
exactly as today** — a plan/execute/rework turn that *ran and reported failure* is
a real outcome, not a blip, and must never be silently re-run (cost + partial
side-effects).

This is the "retries/backoff" line item of M5 hardening (init plan §10, §12).

## Scope (locked during brainstorming)

- **Hybrid classification.** Errors are classified transient-vs-permanent, and we
  retry *both* idempotent I/O and — conservatively — `claude` transport failures.
- **Retry lives inside the provider impls, not the core.** The transient/permanent
  decision is inherently provider-specific (go-github status codes, git stderr
  patterns, the `claude` envelope), so classification and the retry wrapping live in
  `internal/board/github`, `internal/forge/github`, and `internal/claude`. The
  `internal/orchestrator` core is **unchanged** and still imports no provider
  package (CLAUDE.md rule 1; `imports_test.go` stays green). The ports become
  reliable *by construction*: every caller — the worker today, the `memory` fake
  tomorrow — gets resilience for free, and the `GetCard`-drop bug is fixed as a
  side effect.
- **Never retry a paid model turn that ran.** A model-reported failure
  (`res.Status != complete`, `is_error`, a failure subtype) is non-retryable → the
  existing `Fail` path. Only a `claude` *transport* failure that occurred **before
  any work started** (process spawn failure; a recognized `overloaded_error` /
  `api_error` 529 with no session id and no result text) is retried, and only a
  couple of times.
- **Bounded budgets, well under the lock TTL.** The summed backoff for I/O retries
  stays far below the queue's cross-restart lock TTL (default 5m) so a card's lock
  can't expire mid-retry and let a peer double-process it.
- **No new regression path.** After retries are exhausted, the *existing*
  `Worker.fail` behavior runs (post the error comment, move to Failed). We only
  *prevent* the unnecessary Fails; we never change what a genuine, terminal failure
  does.

### Out of scope (later, or never)

- **Persisting attempt counts across restarts.** Retries are in-process, within a
  single `Worker.Process` call. Crash resilience already comes from webhook
  re-delivery + the existing per-card TTL lock; a durable retry ledger is not worth
  the complexity for v1.
- **Retrying a whole phase.** We retry individual idempotent operations, never the
  enclosing phase (which would re-run the paid model turn).
- **A global rate-limiter / token bucket.** Reacting to GitHub's rate-limit reset
  on the retry path is enough for v1; proactive throttling is a later concern.
- **Retrying non-idempotent-without-guard sequences.** We do not add retry where a
  re-run could double a side effect that isn't already guarded (see Idempotency).

## Architecture — where retry lives

The two-port, provider-agnostic core (CLAUDE.md rule 1) is preserved. Retry is a
property of each **port implementation** plus a shared leaf helper.

```
internal/retry/            NEW leaf util. Policy + Do(); pure, no provider imports.
internal/board/github/     wraps REST + GraphQL calls; transientGitHub classifier.
internal/forge/github/     wraps network git ops + go-github PR calls; transientGit classifier.
internal/claude/           wraps the exec run; conservative transientClaude classifier.
internal/orchestrator/     UNCHANGED. Still imports only board + forge interfaces.
```

Why provider-impl placement (settled in brainstorming): the classification
knowledge only exists in the impls, and a worker-level retry would force provider
error types up into the core (breaking rule 1 / `imports_test.go`) or require a
`Temporary() bool` wrapper on every error return in every port method — more
surface area, not less. Placing it in the impls matches where the code already owns
provider mapping (column↔Phase, `IsBot`, `Dedup`, the marker).

## Component 1 — `internal/retry` (shared leaf helper)

```go
package retry

type Policy struct {
    MaxAttempts int           // total tries including the first (e.g. 4)
    BaseDelay   time.Duration // first backoff (e.g. 500ms)
    MaxDelay    time.Duration // cap per backoff (e.g. 8s)
}

// Classifier reports whether an error is worth retrying, and optionally a
// provider-supplied minimum delay before the next attempt (e.g. a rate-limit
// reset). retryAfter == 0 means "use the computed backoff".
type Classifier func(err error) (retry bool, retryAfter time.Duration)

// Do runs fn until it succeeds, the classifier says stop, attempts are exhausted,
// or ctx is cancelled. Backoff is exponential (BaseDelay * 2^n) capped at MaxDelay
// with full jitter; retryAfter, when larger, overrides the computed delay. Returns
// the last error on give-up (wrapped so callers can see the attempt count).
func Do(ctx context.Context, p Policy, classify Classifier, fn func() error) error
```

- Pure and deterministic-to-test: jitter is drawn from an injectable source so unit
  tests can pin it; `ctx` cancellation is honored during the sleep.
- No provider imports — it is a leaf util both the github impls and the `claude`
  runner may import without the core ever importing a provider.

## Component 2 — classification (each in its impl)

- **`board/github` → `transientGitHub(err)`** — retry: `*github.RateLimitError` and
  `*github.AbuseRateLimitError` (surface `Rate.Reset` / `RetryAfter` as the delay),
  `*github.ErrorResponse` with a `5xx` or `429` status, GraphQL transport `5xx`, and
  `net`/`url` timeout / connection-reset errors. Do **not** retry `4xx` validation,
  auth (`401`/`403` non-rate-limit), or not-found — those are terminal.
- **`forge/github` → `transientGit(err)`** — the git CLI surfaces network trouble in
  stderr; retry on patterns like `Could not resolve host`, `Connection timed out`,
  `Connection reset`, `early EOF`, `the remote end hung up`, `TLS`, and remote `5xx`.
  Do **not** retry merge conflicts, non-fast-forward pushes, `nothing to commit`, or
  auth failures. go-github PR calls reuse `transientGitHub`.
- **`claude` runner → `transientClaude(err)` (conservative)** — retry **only**: an
  `exec` spawn failure (the process never started), and a recognized
  `overloaded_error` / `api_error` `529` in the envelope/stderr **with no
  `session_id` and no result text** (no paid work happened). A model-reported
  `is_error` / failure subtype is **not** retried — it flows to the existing Fail
  path unchanged.

## Component 3 — call sites wrapped

- **`board/github` (`board.go`, `projects_gql.go`):** each REST call — `PostComment`,
  `SetBody`, `GetCard`, comment/PR reads — and each GraphQL `Mutate`/`Query` —
  `MoveTo`, provisioning reconcile, per-card `node()` resolve — wrapped in
  `retry.Do` with `transientGitHub`. This is what fixes the `GetCard`-drop bug.
- **`forge/github` (`forge.go`, `git.go`):** network git ops (clone/fetch,
  `PushBranch`) with `transientGit`; go-github PR calls (`OpenPR`, `PRStatus`,
  `PRReviewFeedback`, `CheckAnnotations`) with `transientGitHub`. Purely-local ops
  (creating a worktree from an already-present clone) need no network retry, but a
  `fetch` performed inside one does.
- **`claude` runner (`runner.go`):** the `exec.CommandContext` run wrapped in a
  bounded `retry.Do` (`MaxAttempts` 2) with `transientClaude` — the "claude
  transport" half of the hybrid.

## Idempotency (why per-call retry is safe here)

Retrying an individual idempotent operation is safe; retrying an operation whose
re-run could double a side effect is not, so those are deliberately excluded:

- `PostComment` / `MoveTo` / `SetBody` / status reads / `git fetch` / `git push` /
  `clone` — safe to repeat (a re-push of the same commits is a no-op; a duplicate
  identical comment only happens if the *first* call actually succeeded but its
  response was lost, an accepted rare cost).
- `OpenPR` — retried only on a transport error *before* GitHub created the PR; if a
  create partially succeeded, the classifier sees a non-transient `422`
  ("A pull request already exists") and stops. (Implementation note for the plan:
  confirm go-github surfaces the already-exists case as a non-retryable `422`.)
- The **paid model turns** are never wrapped by the phase-level retry — only the
  `claude` runner's own conservative pre-work transport retry touches them, and that
  path is defined to run only when no work has happened.

## Policy & configuration (fig)

Two defaults, both env-overridable:

- **GitHub / git I/O:** `MaxAttempts` 4, `BaseDelay` 500ms, factor ×2, `MaxDelay`
  8s, full jitter. Worst-case summed backoff (~0.5 + 1 + 2 + 4 ≈ 7.5s + jitter) is
  far under the 5-minute lock TTL.
- **`claude` transport:** `MaxAttempts` 2, `BaseDelay` ~2s.

New `retry` config section:

```
retry:
  max_attempts: 4          # WAZIR_RETRY_MAX_ATTEMPTS
  base_delay:   500ms      # WAZIR_RETRY_BASE_DELAY
  max_delay:    8s         # WAZIR_RETRY_MAX_DELAY
```

The `claude` transport cap lives under the existing `claude` section
(`max_transport_retries`, env `WAZIR_CLAUDE_MAX_TRANSPORT_RETRIES`).

`retry.*` is **reload-safe** — it is stateless and read per-call — so it joins the
`wazir serve` live-reload safe subset alongside `claude.*`. (The impls read the
current policy via an atomically-swappable holder, mirroring
`maxBrainstormTurns`.) A nice-to-have, not required for the slice to ship.

## Lock-TTL interaction (a constraint, not a feature)

The queue holds a per-card in-process mutex **and** a cross-restart advisory lock
(`LockTTL`, default 5m) for the whole `Worker.Process` call. Retry backoff extends
that call, so the I/O retry budget is capped (see policy above) to stay well under
5m — otherwise a lock could expire mid-retry and a peer worker could double-process
the card. Note: long `claude` turns can already approach this ceiling independently
of retries; that pre-existing tension is out of scope here — this design only
commits to **not making it worse**.

## Observability

- Log each retry attempt at `debug` (`card`/op, attempt N/max, delay, error) and a
  single `warn` on final give-up before the error propagates to `Worker.fail`.
- The existing per-turn cost/`session_id` logging is untouched; a retried claude
  transport failure logs one line per attempt so a chatty-provider situation is
  visible.

## Testing strategy

- **`internal/retry`:** table tests — succeeds first try; succeeds after N;
  exhausts and returns the last error with attempt count; honors `ctx` cancel
  mid-backoff; `retryAfter` overrides the computed delay; jitter stays within
  `[0, computed]`. Injected jitter/clock, no real sleeps beyond tiny bounds.
- **`board/github`:** `httptest` returning `503` twice then `200` → one
  `PostComment` succeeds in 3 attempts; a `422` → no retry, immediate error; a
  `RateLimitError` → next delay respects the reset.
- **`forge/github`:** stub `git` (existing exec-stub pattern) to exit with a
  transient stderr once then succeed; a merge-conflict stderr → no retry.
- **`claude`:** fake `claude` binary that fails to spawn / prints an
  `overloaded_error` once then succeeds → retried; a model-reported `is_error` →
  **not** retried.
- **Core unchanged:** `internal/orchestrator` and `internal/queue` tests pass
  untouched; `imports_test.go` stays green (proves the core imported no provider).

## Files touched (anticipated)

- **New:** `internal/retry/retry.go` + `retry_test.go`.
- `internal/board/github/board.go`, `projects_gql.go` (+ tests) — classifier + wraps.
- `internal/forge/github/forge.go`, `git.go` (+ tests) — classifier + wraps.
- `internal/claude/runner.go` (+ tests) — conservative transport retry.
- `internal/config/config.go` (+ tests) — `retry` section + `claude.max_transport_retries`.
- `cmd/wazir/serve.go` — wire the policy in; add `retry.*` to the reload safe subset.
- `wazir.example.yaml`, `CLAUDE.md` — document the new section + behavior.
