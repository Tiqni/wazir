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
- **Retry lives in the provider layer, not the core.** The transient/permanent
  decision is inherently provider-specific (HTTP status codes, git stderr
  patterns, the `claude` envelope), so classification and the retry wrapping live in
  `internal/githubauth`, `internal/forge/github`, and `internal/claude`. The
  `internal/orchestrator` core is **unchanged** and still imports no provider
  package (CLAUDE.md rule 1; `imports_test.go` stays green). The ports become
  reliable *by construction*: every caller — the worker today, the `memory` fake
  tomorrow — gets resilience for free, and the `GetCard`-drop bug is fixed as a
  side effect.
- **The GitHub-HTTP half is a transport concern, not per-call.** `githubauth.New`
  builds a single `*http.Client{Transport: ghinstallation.Transport}` shared by
  board REST, board GraphQL, and forge PR calls. Wrapping that transport in one
  retrying `http.RoundTripper` covers all GitHub HTTP in one place — far fewer edit
  sites than wrapping each call, uniform across REST and GraphQL, and automatically
  covering future calls. `board/github` and `forge/github`'s go-github calls are
  left untouched; they inherit retry through the shared client.
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
internal/githubauth/       NEW retrying http.RoundTripper wrapping the ghinstallation
                           transport; covers board REST + GraphQL + forge PRs. HTTP-status classifier.
internal/forge/github/     wraps the one git-exec chokepoint (network ops); transientGit classifier.
internal/claude/           wraps the exec run; conservative transientClaude classifier.
internal/orchestrator/     UNCHANGED. Still imports only board + forge interfaces.
```

Why provider-layer placement (settled in brainstorming): the classification
knowledge only exists provider-side, and a worker-level retry would force provider
error types up into the core (breaking rule 1 / `imports_test.go`) or require a
`Temporary() bool` wrapper on every error return in every port method — more
surface area, not less. The GitHub-HTTP retry is a natural transport concern
(`githubauth` already owns the one shared client), and the git/`claude` retries sit
at their single exec chokepoints — three insertion points, not twenty.

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

## Component 2 — classification (each provider-side)

- **`githubauth` transport → HTTP-status classifier** — the `RoundTripper` inspects
  the `*http.Response` directly: retry on `429` (honoring `Retry-After` /
  `X-RateLimit-Reset` as the next delay) and `500`/`502`/`503`/`504`; retry on a
  `RoundTrip` transport error that is a `net`/`url` timeout or connection reset. Do
  **not** retry `4xx` other than `429`, nor a `401`/`403` that is not a rate limit —
  those are terminal. This one classifier covers REST **and** GraphQL because both
  flow through the same client, side-stepping go-github-vs-githubv4 typed-error
  differences.
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

## Component 3 — insertion points

- **`githubauth` (`internal/githubauth`):** `New` wraps the `ghinstallation.Transport`
  in a `retryTransport` (a `http.RoundTripper` that calls `retry.Do` around the inner
  `RoundTrip`). Every board REST call, board GraphQL `Mutate`/`Query`, and forge PR
  call inherits retry through the one shared `*http.Client` — no edits to `board.go`,
  `projects_gql.go`, or `forge.go`. This is also what fixes the `GetCard`-drop bug.
  (POST/mutation retries rely on `Request.GetBody`, which `net/http` populates for
  the byte-buffer bodies go-github/githubv4 send, so the body can be rewound.)
- **`forge/github` (`git.go`):** the single `gitRunner.run` chokepoint that every git
  op flows through gets the retry, gated on network ops (`auth == true`:
  clone/fetch/push) with `transientGit`. Purely-local ops (`worktree add`,
  `rev-parse`) pass `auth == false` and are not retried.
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
- `OpenPR` — the transport retries a `POST /pulls` only on `429`/`5xx`/transport
  error. If a create actually succeeded but its response was lost, the retried POST
  gets a `422` ("A pull request already exists"), which the classifier treats as
  non-retryable and returns — go-github then surfaces it as an error to the worker,
  same as today. No double-PR.
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

`retry.*` is **reload-safe** — it is stateless and read per-call. The
`retryTransport` holds the policy in an `atomic.Pointer[retry.Policy]`; `serve`'s
reload swaps it, mirroring `maxBrainstormTurns`. This joins the live-reload safe
subset alongside `claude.*`. A nice-to-have, not required for the slice to ship — if
cut, the policy is read once at startup and the transport holds it by value.

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
- **`githubauth` transport:** a stub `http.RoundTripper` returning `503` twice then
  `200` → the wrapped transport makes 3 round-trips and returns `200`; a `422` → one
  round-trip, no retry; a `429` with `Retry-After: 1` → the next delay respects the
  header; a `POST` body is rewound and re-sent intact on the retry.
- **`forge/github`:** stub `git` (existing exec-stub pattern) to exit with a
  transient stderr once then succeed; a merge-conflict stderr → no retry; a local
  (`auth == false`) op is never retried.
- **`claude`:** fake `claude` binary that fails to spawn / prints an
  `overloaded_error` once then succeeds → retried; a model-reported `is_error` →
  **not** retried.
- **Core unchanged:** `internal/orchestrator` and `internal/queue` tests pass
  untouched; `imports_test.go` stays green (proves the core imported no provider).

## Files touched (anticipated)

- **New:** `internal/retry/retry.go` + `retry_test.go` (the leaf helper).
- **New:** `internal/githubauth/transport.go` + `transport_test.go` — the retrying
  `http.RoundTripper` + HTTP-status classifier; `New` wraps the ghinstallation
  transport with it.
- `internal/forge/github/git.go` (+ `git_test.go`) — `transientGit` classifier +
  retry around the `gitRunner.run` network path.
- `internal/claude/runner.go` (+ `runner_test.go`) — `transientClaude` + conservative
  transport retry.
- `internal/config/config.go` (+ `config_test.go`) — `retry` section +
  `claude.max_transport_retries`.
- `cmd/wazir/serve.go` — build the policy from config, pass it to `githubauth.New`;
  add `retry.*` to the reload safe subset (swap the transport's `atomic.Pointer`).
- `wazir.example.yaml`, `CLAUDE.md` — document the new section + behavior.

No edits to `internal/board/github/board.go` / `projects_gql.go` / `forge.go` (they
inherit HTTP retry through the shared client) and none to `internal/orchestrator`.
