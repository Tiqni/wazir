# Manual Test Plan — M5 slice 2: GitHub App Token Auth

**Date:** 2026-06-13
**Feature/PR:** `m5-app-auth` → `main` (PR #7)
**Spec:** `docs/superpowers/specs/2026-06-13-wazir-m5-app-auth-design.md`
**Scope:** the live verification the offline unit suite can't cover — real installation-token
minting, App-authenticated board (GraphQL + REST) and git (clone/fetch/push) operations against an
**org-owned** board, startup eager-mint (success + fail-loud), config validation, and the
private-key auto-detect forms.

> The offline suite (`go test ./...`, `-race`, `vet`, `vet -tags integration`) already passes and is
> NOT repeated here. This plan is the manual/integration acceptance for the App-auth path.

---

## 0. Prerequisites (operational — see also spec §13)

- [x] A **GitHub org** owns: the **Projects v2 board** under test, and the **repo(s)** Wazir will act on.
      (An App cannot drive a *user-owned* board — see the spike,
      `docs/superpowers/spikes/2026-06-13-m5-slice2-app-auth-spike.md`.)
- [x] A **GitHub App** registered (registration walkthrough is in the spike runbook) with repository
      permissions **Contents RW, Issues RW, Pull requests RW, Metadata RO** and **Organization → Projects RW**,
      subscribed to **Issues / Issue comment / Projects v2** events.
- [x] The App **installed on the org**, granting the operated repo(s). Note the **installation id**.
- [x] The App's **private key** `.pem` downloaded; note its path.
- [x] `go1.25+`, `git`, and `gh` available; the binary builds (`go build ./...`).
- [x] For the full-lifecycle case only: `claude` installed and `CLAUDE_CODE_OAUTH_TOKEN` exported
      (headless `claude` is **metered**).

### Environment used by the cases below

```sh
export WAZIR_GITHUB_APP_ID=<app id>
export WAZIR_GITHUB_INSTALLATION_ID=<org installation id>
export WAZIR_GITHUB_PRIVATE_KEY=/abs/path/to/app.private-key.pem   # path OR PEM bytes (base64 ok)
export WAZIR_GITHUB_WEBHOOK_SECRET=<secret>
export WAZIR_GITHUB_OWNER_TYPE=org
export WAZIR_PROJECT_OWNER=<org-login>
export WAZIR_PROJECT_NUMBER=<board number>
# repos allow-list: set in wazir.yaml (top-level `repos:`) or leave unset to allow all
```

---

## Operational gotchas (hit during the live run — read before running)

- **`serve` holds an exclusive `wazir.db` lock.** You can't run a one-shot `card`/`bootstrap`/
  `provision` while `serve` is up — it hangs on the lock. Stop `serve` first.
- **`serve` reads config once at startup.** After editing `wazir.yaml` (creds, repos, `bot_login`),
  restart `serve` or it keeps using the old config.
- **A running `serve` already proves outbound App auth** — it eager-mints an installation token at
  startup and refuses to start if it can't. A live `serve` ⇒ app_id/installation_id/key are valid.
- **`bot_login` must be the App's bot user** (e.g. `wazir-tiqni[bot]`, type `Bot` — verify with
  `gh api '/users/<slug>%5Bbot%5D'`), or wazir's own column moves re-trigger turns (loops / extra cost).
- **Webhook URL must include the `/webhook` path** and the proxy must forward it; the App webhook must
  be **Active** with its secret matching `WAZIR_GITHUB_WEBHOOK_SECRET`. Confirm via App → Advanced →
  **Recent Deliveries** (expect 2xx). GitHub sends no test ping — trigger a *real* event, and a freshly
  (re)started `serve` only sees events fired *after* it came up.
- **`WAZIR_GITHUB_*` env overrides `wazir.yaml`.** Mixed stale-env + new-file values cause confusing
  auth failures — keep one source of truth (the file, with env unset, is simplest).
- **`private_key` pointing at a missing path** is silently treated as PEM bytes → a confusing
  `invalid key: must be PEM encoded` error. Make sure the path resolves.
- **A transferred/renamed repo** can leave a stale `owner/name` cached in `wazir.db`, so its cards
  silently fail the allow-list with no board error (fixed on `main` by PR #10; on older builds, reset
  `wazir.db`).
- **App auth requires an org-owned board** — a GitHub App cannot see a *user*-owned Projects v2 board
  (it returns `Could not resolve to a ProjectV2`).

---

## Test cases

Record a result (PASS/FAIL + notes) for each. Cases TC1–TC4 are cheap (no `claude`); TC8 is metered.

### TC1 — Config validation rejects bad config (offline, no network)
**Verifies:** spec §7, §10 — App fields required; `owner_type=org` enforced.

1. With **no** App env set, run `go run ./cmd/wazir bootstrap`.
   - **Expect:** fails before any network call with `github app auth requires github.app_id,
     github.installation_id, and github.private_key ...`.
2. With the full App env set but `WAZIR_GITHUB_OWNER_TYPE=user`, run `go run ./cmd/wazir bootstrap`.
   - **Expect:** fails with `github.owner_type must be org for GitHub App auth ...`.

- [ ] Result: 
```bash
❯ go run ./cmd/wazir bootstrap
2026-06-13T13:58:58.243+0200	INFO	wazir/provision.go:80	reconciling board	{"action": "bootstrap", "owner": "Tiqni", "project": 1, "prune": false, "force": false}
2026-06-13T13:58:58.840+0200	INFO	wazir/provision.go:93	board ready	{"board": "@EmadMokhtar's software factory (managed by Wazir)", "project": 1}
board "@EmadMokhtar's software factory (managed by Wazir)" ready for Tiqni (project #1)
```

### TC2 — `serve` eager-mints at startup and fails loud on bad credentials
**Verifies:** spec §2.4, §8, §10 — startup mint, fail loud not mid-turn.

1. Set the full, **valid** env. Run `go run ./cmd/wazir serve --addr :8080`.
   - **Expect:** it hydrates the board and begins listening (no auth error in the logs). Stop it with Ctrl-C.
2. Temporarily set `WAZIR_GITHUB_INSTALLATION_ID=999999999` (wrong). Run `serve` again.
   - **Expect:** it exits **before listening** with `mint installation token (check
     app_id/installation_id/private_key): ...`. Nothing binds to `:8080`.
3. Restore the correct installation id. Point `WAZIR_GITHUB_PRIVATE_KEY` at a junk file
   (`echo nope > /tmp/junk.pem`). Run `serve`.
   - **Expect:** startup error (`parse app private key: ...` or the mint error). No listen.
4. Restore the valid key.

- [ ] Result: ______

### TC3 — Board access via the App token (provision/bootstrap; GraphQL + REST)
**Verifies:** spec §2.1, §3.2 — installation token reaches the **org** Projects v2 board.

1. With valid env, run `go run ./cmd/wazir bootstrap` (reconciles + caches an existing board; never creates).
   - **Expect:** succeeds; prints `board "<name>" ready for <org> (project #<n>)`. No `Resource not
     accessible by integration` / `Could not resolve to a ProjectV2`.
2. Re-run `bootstrap` (idempotency).
   - **Expect:** succeeds again, no duplicate columns.
3. (Optional) Equivalent automated proof — the build-tagged integration test, no `claude`:
   ```sh
   go test -tags integration ./internal/board/github/ -run TestIntegrationProvision -v
   ```
   - **Expect:** PASS (skips only if `WAZIR_PROJECT_NUMBER` unset).

- [ ] Result: ______

### TC4 — Git clone/fetch/push via the App token (no claude)
**Verifies:** spec §2.1/§2.2, §6 — git network ops authenticate with a freshly-resolved installation token.

Run the build-tagged forge round-trip against a scratch org repo you can push to:
```sh
WAZIR_IT_REPO=<org>/<scratch-repo> \
go test -tags integration ./internal/forge/github/ -run TestIntegrationForgeRoundTrip -v
```
- **Expect:** PASS — it clones, worktrees, commits, **pushes a branch** (`wazir-it/...`), and removes the
  worktree, all authenticated by the App token. Delete the pushed branch afterward (the test logs its name).

- [ ] Result: ______

### TC5 — Board writes via the App token (move + comment)
**Verifies:** spec §3.2 — REST issue comment + GraphQL field update under App auth. Needs a real card.

1. Put an issue on the board (any column). Get its node id (e.g.
   `gh issue view <n> --repo <org>/<repo> --json id -q .id`).
2. `go run ./cmd/wazir card comment I_kwDOSsEvY88AAAABFXwvZQ "manual test: app-auth comment"`.
   - **Expect:** prints `commented on <id>`; the comment appears on the issue.
3. `go run ./cmd/wazir card move I_kwDOSsEvY88AAAABFXwvZQ SpecReview`.
   - **Expect:** prints the move; the card's Status column changes on the board.

- [x] Result: Comment and card moved.

### TC6 — Token never persists to disk
**Verifies:** spec §2.3 — token injected via `http.extraHeader`, never written to `.git/config`.

After TC4 (or any clone), inspect a clone's git config:
```sh
grep -ri "x-access-token\|ghs_\|AUTHORIZATION" ~/.wazir/clones/*/*/.git/config 2>/dev/null || echo "clean"
```
- **Expect:** `clean` (no token material in any `.git/config`).

- [x] Result: clean

### TC7 — Private-key auto-detect forms
**Verifies:** spec §1, §5 — `private_key` accepts a file path, raw PEM bytes, or base64-encoded PEM.

Repeat TC3 step 1 (`bootstrap`) three times, changing only `WAZIR_GITHUB_PRIVATE_KEY`:
1. **Path:** `/abs/path/to/app.private-key.pem` (as in TC3).
2. **PEM bytes:** `export WAZIR_GITHUB_PRIVATE_KEY="$(cat /Users/emadmokhtar/Projects/wazir/wazir-emadmokhtar.pem)"`.
3. **Base64 PEM:** `export WAZIR_GITHUB_PRIVATE_KEY="$(base64 < /Users/emadmokhtar/Projects/wazir/wazir-emadmokhtar.pem | tr -d '\n')"`.

- **Expect:** `bootstrap` succeeds in all three. (Restore the path form when done.)

- [x] Result: It works with the 3 types.

### TC8 — Full lifecycle end-to-end (metered: invokes `claude`)
**Verifies:** spec §2.1 — a card drives brainstorm → spec → plan → build → **PR**, with every GitHub
operation (board moves, comments, clone, push, PR open) authenticated by the App token.

1. Ensure `CLAUDE_CODE_OAUTH_TOKEN` is set and `repos` includes the target repo.
2. Run `go run ./cmd/wazir serve --addr :8080` (expose the webhook via a public URL / smee.io if testing
   webhooks; otherwise drive phases manually with `card move`).
3. Create an issue card, walk it through the gates (approve at Spec Review by moving to Planning).
   - **Expect:** Wazir comments, rewrites the spec, opens a worktree, pushes the feature branch, and opens
     a **PR** — all without auth errors. The PR is authored by the App.
4. Confirm in the logs there are no `401`/`403`/`Resource not accessible`/`Not logged in` errors.

- [ ] Result: ______

### TC9 — Hourly token refresh (optional / long-running)
**Verifies:** spec §2.2 — the per-op token source returns a fresh token after the ~1h installation-token expiry.

Leave `serve` running (or the clone present) for **>60 minutes**, then trigger a git network op (e.g. move a
new card through Planning, or re-run TC4's push against the long-lived process).
- **Expect:** the op authenticates without restart — `ghinstallation.Transport` minted a new token
  transparently. (Hard to force in <1h; structurally guaranteed by per-op resolution + the transport's
  cache, so this is a confidence check rather than a gate.)

- [ ] Result: ______

---

## Teardown

- [ ] Delete any scratch branches pushed by TC4/TC8 (the forge test logs its branch name).
- [ ] Restore `WAZIR_GITHUB_PRIVATE_KEY` to the path form; unset any temporary wrong-value envs from TC2.
- [ ] Close/clean the test card and PR if they were throwaway.

## Result summary

| Case | Title | Result | Notes |
|---|---|---|---|
| TC1 | Config validation | | |
| TC2 | serve eager-mint (success + fail-loud) | | |
| TC3 | Board access (provision/bootstrap) | | |
| TC4 | Git clone/fetch/push | | |
| TC5 | Board writes (move + comment) | | |
| TC6 | Token never persists | | |
| TC7 | Private-key auto-detect forms | | |
| TC8 | Full lifecycle → PR (metered) | | |
| TC9 | Hourly token refresh (optional) | | |

**Overall:** ☐ Pass  ☐ Fail — _______________________________

> Minimum bar to call App auth working: **TC1–TC4 + TC6** pass (config + eager-mint + board API + git push
> + no token leak). TC5/TC8 add board-write and full-lifecycle confidence; TC7 covers key delivery forms;
> TC9 is a long-running confidence check.
