# Wazir — The Installable GitHub App Plan

> **North star:** a GitHub App you (and eventually anyone) can **install** on an account, point at a
> Projects v2 board, and have it drive cards from idea → spec → plan → build → PR using Claude Code —
> with a human gate at every step.
>
> This document is the *successor* to `docs/wazir-init-plan.md`. The init plan described a
> single-installer, self-hosted orchestrator built from scratch. M0–M2 of that plan are now merged;
> this plan keeps everything that's built and re-centers the remaining work on the **installable,
> multi-tenant GitHub App** goal. Where the init plan and the code/CLAUDE.md disagree, the code wins.
>
> **Stack: Go.** &nbsp;**Module:** `github.com/EmadMokhtar/wazir` &nbsp;**Binary:** `wazir` (daemon: `wazird`).

---

## 0. Where we are today (read this first)

The orchestrator already exists and runs. This plan is about turning a working personal tool into an
installable product, not a green-field build.

| Milestone | Status | What it delivered |
|---|---|---|
| **M0** | ✅ merged | Two ports (`Board`, `CodeForge`), GitHub Projects v2 plumbing, idempotent `provision`/`bootstrap`, the cobra CLI, PAT auth. |
| **M1** | ✅ merged | Webhook receiver, delivery-id dedupe, per-card queue, in-memory `Board` fake, orchestrator core with a faked `Brain`. |
| **M2** | ✅ merged | Real `claude`-CLI brain (M2+M3 collapsed): live brainstorm loop, questions ↔ answers, spec written to the card body. |
| **M4** | 🚧 in progress | Live forge git ops (clone/worktree/push) + real `claude` plan/execute turns in a per-card worktree → a real PR. |
| **M5 slice 1** | 🚧 in progress | Execution isolation: per-run `CLAUDE_CONFIG_DIR`, `--plugin-dir`, long-lived-token auth, repo-aware brainstorm cwd. |
| **M5 slice 2** | 🔬 spiked | GitHub **App** auth for git + REST. Spike confirmed the user-owned-board limitation (see §4). |

**The product gap.** Today Wazir authenticates with a PAT and is wired for exactly one installer on one
board. To become "install and go," three things must change, and they are the spine of this plan:

1. **App-based auth** that mints per-installation tokens instead of carrying a human's PAT (§4).
2. **Multi-tenant routing & state** — one daemon serving many installations, keyed by `installation_id` (§5).
3. **Zero-touch onboarding** — provisioning the board and labels automatically when the App is installed (§6).

---

## 1. Goal

Ship Wazir as a **GitHub App** that a user installs on their account or org. On install, Wazir
provisions (or adopts) a Projects v2 board, then watches it: a human writes an idea/bug as a card, and
Wazir drives it through brainstorming → spec → planning → execution, pausing at every gate for explicit
human approval, and finally opens a PR and posts the link back on the card.

Wazir does **not** reimplement Superpowers. It invokes Claude Code (with the Superpowers plugin)
headlessly as the per-phase "brain," and owns all deterministic GitHub state changes itself through two
provider-agnostic ports.

**v1 distribution target: a multi-tenant App, shipped in stages.** Single-installer works today; this
plan adds multi-tenant *as the destination* but keeps every step independently shippable, so a personal
install keeps working throughout.

---

## 2. Design principles (unchanged — these are load-bearing)

1. **The board is the source of truth.** A card's phase = its `Status` single-select field value. The
   orchestrator re-derives what to do from the board + comment thread on every event. No hidden state.
2. **The model reasons; the orchestrator acts.** Claude is invoked only to return structured text. The
   orchestrator performs *all* provider I/O (post comment, rewrite spec, move column, open PR)
   deterministically through the ports. Never let the model move columns or open PRs directly.
3. **Human gates are explicit.** A card only advances past a review gate on an explicit human signal (a
   label, an approval comment, or a column move). Silence never auto-advances. Wazir never merges.
4. **Idempotent & serialized.** Webhooks fire repeatedly and out of order: dedupe on the delivery id,
   track `last_processed_comment_id`, serialize work per card with a keyed mutex + a cross-restart TTL
   lock; different cards run concurrently.
5. **Isolated execution.** Each card's plan/build runs in its own `git worktree` and an isolated
   `CLAUDE_CONFIG_DIR`/`HOME` per concurrent `claude` run (M5 slice 1).
6. **Provider-agnostic core, two ports.** The core depends only on `Board` and `CodeForge`. GitHub
   implements both; nothing in the core imports a provider package (enforced by
   `internal/orchestrator/imports_test.go`).
7. **(New) Tenant-agnostic core.** The orchestrator core must not know whether it's serving one
   installation or a thousand. Tenancy (installation id, token source, per-tenant config) is resolved at
   the edge and injected — the same discipline that kept providers out of the core (§5).

---

## 3. The state machine (unchanged)

The Project's `Status` single-select field defines the columns.

| Column            | Owner        | Meaning / trigger                                                        |
|-------------------|--------------|--------------------------------------------------------------------------|
| `Inbox`           | Human        | New idea/bug. Human writes the card.                                     |
| `Brainstorming`   | Orchestrator | Picked up; running a brainstorm turn.                                    |
| `Awaiting Answers`| Human        | Orchestrator posted clarifying questions; waiting for a human reply.     |
| `Spec Review`     | Human        | Brainstorm decided it's clear; spec written to the issue body.           |
| `Planning`        | Orchestrator | Approved spec; running `write-plan`, creating worktree.                  |
| `Building`        | Orchestrator | Running `execute-plan` in the worktree.                                  |
| `PR Review`       | Human        | Execution done; PR opened and link posted.                              |
| `Done`            | Human        | PR merged (native GitHub).                                               |
| `Failed`          | Orchestrator | A phase errored; needs human attention. Always have a failure column.    |

The `Phase` constants are camelCase tokens (`AwaitingAnswers`, `SpecReview`, `PRReview`); the spaced
display names live only inside the GitHub `Board` impl's mapping.

```
Inbox ──(human moves OR auto-add workflow)──▶ Brainstorming
Brainstorming ──Claude: needs answers──▶ Awaiting Answers   (post questions)
Awaiting Answers ──human comments──▶ Brainstorming          (re-run with updated thread)
Brainstorming ──Claude: spec ready──▶ Spec Review           (rewrite issue body = spec)
Spec Review ──human comments──▶ Spec Review                 (re-run, adjust spec)
Spec Review ──human approves──▶ Planning
Planning ──worktree + plan written──▶ Building
Building ──execution complete──▶ PR Review                  (open PR, post link)
PR Review ──human merges──▶ Done
any phase ──error──▶ Failed                                 (post error comment)
```

---

## 4. GitHub App authentication & board ownership (the central fork)

This is the decision that makes or breaks the "install and go" story, so it gets its own section.

### 4.1 The limitation (confirmed, not theoretical)

The M5 slice-2 spike (`docs/superpowers/spikes/2026-06-13-m5-slice2-app-auth-spike.md`) confirmed with
GitHub support and a live test:

> A GitHub **App installation token can authenticate git (clone/fetch/push) and the REST API
> (issues, comments, PRs) perfectly — but it CANNOT read or write a *user-owned* Projects v2 board via
> GraphQL.** The call returns `Resource not accessible by integration`. App tokens *can* drive
> **org-owned** Projects v2 boards (via the org-level Projects permission).

This is the crux: the whole product is "an App that manages your board," but the App token can only
manage the board if the board is **org-owned**.

### 4.2 Two paths (recommend the first)

**Path A — Org-owned board, full App auth (recommended).**
The board lives under an organization. The App is granted the org **Projects: read & write**
permission, and a single installation token drives *everything*: board GraphQL, issue/comment REST, git
push, and PR creation. The PAT disappears entirely. This is the clean, pure-install experience and the
only one that scales to multi-tenant without asking each installer for a personal secret.

- *Cost to the installer:* the board must be org-owned. For a personal user this means creating a
  free GitHub org (zero cost) and moving/creating the board there. Onboarding (§6) should detect a
  user-owned board and guide the installer to convert.
- *Why recommended:* a multi-tenant product cannot ask every tenant to hand over a long-lived PAT — it's
  a security liability and a support burden. Org board + App token is the only model where "install the
  App" is genuinely sufficient.

**Path B — Hybrid: App forge + PAT board (fallback for user-owned boards).**
Forge operations (git clone/fetch/push, PR creation) use the App installation token; the board client
(Projects v2 GraphQL + issue comments, if you keep those on the same client) stays on a PAT the
installer supplies. Works with user-owned boards, but the installer must *also* register and store a
PAT — which weakens the install story and is awkward for a published, multi-tenant App.

- *Use it for:* a single-installer personal deployment where the board is already user-owned and the
  installer doesn't want to move it.
- *Don't use it for:* the published multi-tenant product. Carrying a tenant's PAT centrally is exactly
  the liability Path A avoids.

**Decision for this plan:** build **Path A as the default and the multi-tenant path**, but keep the
existing PAT/hybrid code path behind config (`github.auth: pat | app`) so a personal user-owned board
keeps working. The auth seam (`internal/githubauth`) already abstracts this — the work is making the
token source installation-aware, not rewriting callers.

### 4.3 How App auth works mechanically

The App holds a private key and an App ID. Per installation:

1. Sign a short-lived **JWT** with the App private key (proves "I am this App").
2. Exchange the JWT for an **installation access token** scoped to that installation
   (`POST /app/installations/{id}/access_tokens`). Tokens last ~1 hour.
3. Use the installation token for git (`https://x-access-token:<tok>@github.com/...`), REST, and — for
   org boards — Projects v2 GraphQL.

`bradleyfalzon/ghinstallation/v2` does steps 1–2 and caches/refreshes the token as an
`http.RoundTripper`. The spike already proved `tr.Token(ctx)` mints a working token — that exact call is
what the git token source and the GraphQL/REST clients share.

**Key management.** The App private key (`.pem`) and the webhook secret are the daemon's crown jewels.
They come from the environment (`WAZIR_GITHUB_APP_ID`, `WAZIR_GITHUB_PRIVATE_KEY`,
`WAZIR_GITHUB_WEBHOOK_SECRET`), never the committed config. For multi-tenant, the *App* key is
single (one App, many installations); only the per-installation **ids** are multi-valued (§5).

### 4.4 App registration & permissions

Register at `https://github.com/settings/apps/new` (or via an App manifest flow for repeatable setup):

- **Repository permissions:** Contents (read/write — git push), Issues (read/write), Pull requests
  (read/write), Metadata (read-only, mandatory).
- **Organization permissions:** Projects (read/write) — this is what makes Path A work and is useless
  for user-owned boards.
- **Webhook:** Active, URL = `https://<host>/webhook`, secret = `WAZIR_GITHUB_WEBHOOK_SECRET`.
- **Subscribe to events:** Issues, Issue comment, Projects v2, plus **Installation** and
  **Installation repositories** (so the daemon learns when it's installed/uninstalled — §6).
- **Where can this App be installed?** "Only on this account" for personal; "Any account" for the
  published product.

---

## 5. Multi-tenant architecture

The core orchestrator stays single-tenant in spirit — it operates on a `Card` for a `Board` for a
`CodeForge`. Tenancy is resolved at the **edge** (the webhook receiver) and injected, mirroring how
providers are injected in `main.go` today.

### 5.1 What "tenant" means here

One **tenant = one App installation** (`installation_id`), which maps to one account/org and a set of
repos plus (typically) one configured board. The single-installer case is just "one tenant."

### 5.2 What changes vs. today

```
                 GitHub (many installations)
   ┌─────────────────────────────────────────────────┐
   │ Webhooks carry installation.id on every event    │
   └───────────────┬─────────────────────────────────┘
                   ▼
        ┌───────────────────────────┐
        │ Webhook Receiver           │  ValidatePayload (App webhook secret) → ParseEvent
        │  + Tenant Resolver         │  extract installation_id → look up tenant config + token source
        └──────────┬────────────────┘
                   ▼
        ┌───────────────────────────┐
        │ Per-(tenant,card) queue    │  keyed mutex now keyed by (installation_id, card_id)
        └──────────┬────────────────┘
                   ▼
        ┌───────────────────────────┐
        │ Orchestrator Worker        │  unchanged core; receives a Board + CodeForge
        │                            │  already bound to this tenant's token source
        └───────────────────────────┘
```

Concretely:

- **Token source is per-installation.** `internal/githubauth` gains an installation-keyed factory:
  `TokenSource(installationID) → *http.Client`. `ghinstallation` keeps one transport per installation,
  refreshing tokens independently. The App private key is shared; installation ids are the variable.
- **Webhook routing.** Every App webhook payload includes `installation.id`. The receiver extracts it,
  resolves the tenant (config + board ids + token source), and only then enqueues. Events for unknown
  installations are dropped.
- **Board/CodeForge are built per tenant.** `main.go`'s wiring moves behind a small `tenantRegistry`
  that lazily builds (and caches) a `Board` + `CodeForge` for an installation id. The orchestrator never
  sees the registry — it's handed ready-built ports, exactly as today.
- **Store rows are installation-aware.** The init plan already mandated carrying `(project_id, repo,
  item)` on every row (§4.1 there). Add `installation_id` to that key. The bbolt buckets become
  installation-namespaced; the `BoardRecord`/`CardRecord` keys gain the installation dimension. This is
  the same "leave the seam, don't build routing yet" discipline — except now we *do* build the routing.

### 5.3 The seam already exists

CLAUDE.md rule 6 says every store row and write already carries `(project_id, repo, item)` "so going
multi-board later is a config change, not a migration." Multi-tenant is the cash-in on that seam: add
`installation_id` as the outermost key and turn the single-project config into a per-installation
lookup. No domain-type changes; the `Board`/`CodeForge` interfaces are unchanged.

### 5.4 Tenant state & lifecycle

- On **`installation.created`**: persist the installation (id, account, granted repos), run onboarding
  (§6).
- On **`installation_repositories`**: update the repo allow-list for that tenant.
- On **`installation.deleted`**: tear down the tenant — stop serving its events, drop cached tokens,
  optionally purge worktrees/clones for its repos.

---

## 6. Zero-touch onboarding (the "install and go" experience)

The difference between a script and a product is what happens in the 60 seconds after someone clicks
**Install**. Wazir should provision itself.

### 6.1 Install-time flow

1. **User clicks Install** on the App listing, picks an account/org and repos.
2. GitHub redirects to the App's **Setup URL** (`https://<host>/setup?installation_id=…`) and fires
   `installation.created`.
3. The setup handler / event handler, using the fresh installation token:
   - Detects the target account type. **If user-owned and `auth: app`**, surface the §4.2 guidance:
     "Projects boards on personal accounts can't be driven by the App token — create/move the board to
     an org, or switch to hybrid PAT mode." (This is the one unavoidable friction point; make it a clear
     guided step, not a silent failure.)
   - **Provisions the board:** `Board.EnsureProvisioned` — create the board if absent, reconcile the
     `Status` column set to the §3 phases (additive-safe: preserve existing option ids, append missing
     ones), enable the built-in "Auto-add to project" workflow so new issues land in `Inbox`.
   - **Creates the human-signal labels** (`spec-approved`, `needs-revision`) via the REST labels API.
   - Caches project id + field id + per-phase option ids into the (now installation-keyed) store.
4. **Posts a welcome card** in `Inbox` explaining the loop, so the first thing the installer sees is a
   working example they can drag to `Brainstorming`.

`EnsureProvisioned` is already idempotent (M0), so re-installs / re-runs converge.

### 6.2 What stays manual (and can't be automated)

- Registering the App itself and generating its private key (one-time, done by *you* the publisher, not
  the installer).
- For user-owned boards on Path A: the org conversion. Guide it; can't do it for them.

### 6.3 Publishing

For a truly public App, list it on the **GitHub Marketplace** (optional) or share the install URL
directly. Marketplace adds review + (optional) billing plumbing; the install URL works immediately.
Either way the daemon must be reachable at a stable HTTPS webhook URL. v1 can ship as "share the install
link + self-host the daemon"; Marketplace listing is a later, additive step.

---

## 7. Component specs (deltas from the init plan)

Only the parts that change for the App/multi-tenant goal are listed; everything else in
`wazir-init-plan.md` §8 still holds.

### 7.1 Webhook Receiver (+ Tenant Resolver)
- Verify signatures with the **App webhook secret** (one secret for the App, all installations).
- `board.ParseEvent` still normalizes the payload into a domain `Event`. New: extract `installation.id`
  alongside `Dedup`, before enqueue.
- Handle the **installation lifecycle** events (`installation`, `installation_repositories`) — these
  drive tenant create/update/delete, not the card state machine.
- Drop events for unknown installations or for a `projects_v2_item` whose project ≠ that tenant's board.

### 7.2 Auth seam (`internal/githubauth`)
- Add `InstallationTokenSource(appID, installationID, key) → *http.Client` backed by
  `ghinstallation/v2` (proven by the spike).
- Keep the PAT round-tripper for `auth: pat`/hybrid and tests.
- The factory caches one transport per installation; tokens auto-refresh.

### 7.3 GitHub `Board` impl
- For **org boards** the same GraphQL code now runs on the installation token — no logic change, just a
  different `*http.Client`.
- For **hybrid** keep the board client on the PAT client; only the forge swaps to the App token.

### 7.4 GitHub `CodeForge` impl
- Git remote auth uses `https://x-access-token:<installation-token>@github.com/<repo>` (token minted
  per network op via the transport). Clone/worktree/push and `OpenPR` are otherwise the M4 live path.

### 7.5 Store
- Outermost key becomes `installation_id`. Buckets: `installations`, then per-installation
  `boards`/`cards`/`deliveries`/`runs`/`locks`. `installation.deleted` drops the namespace.

### 7.6 Claude Runner (unchanged contract, isolation matters more)
- Multi-tenant means concurrent `claude` runs across tenants. M5 slice 1's per-run `CLAUDE_CONFIG_DIR` +
  isolated `HOME` is now a hard requirement, not a nicety: never let two tenants share session state, and
  never expose one tenant's credentials to another's run.
- The Agent SDK cost (`claude -p`) is now billed across *all* tenants on the publisher's account —
  per-tenant cost accounting and budget breakers become essential (§9).

---

## 8. Revised milestones (continuing from M5)

M0–M2 are done; M4 and M5 slice 1 are in flight. This plan adds the App/multi-tenant track.

- **M5 slice 1 — Execution isolation (🚧 in progress).** Per-run `CLAUDE_CONFIG_DIR`, `--plugin-dir`,
  long-lived-token auth, repo-aware brainstorm cwd. *Already specced & in progress.*
- **M5 slice 2 — App auth (single-installer).** Wire `ghinstallation` installation tokens behind
  `github.auth: app`. Forge + REST on the App token; board on App (org) or PAT (hybrid) per §4.2. Ship
  for one installation first. *Demo: a personal org-board install drives a card to a PR with no PAT.*
- **M6 — Onboarding / auto-provision on install.** Setup URL + `installation.created` handler →
  `EnsureProvisioned` + labels + welcome card. Detect user-owned-board case and guide. *Demo: click
  Install, board appears provisioned with a welcome card, no CLI.*
- **M7 — Multi-tenant.** Tenant resolver in the receiver, per-installation token sources & port
  registry, installation-keyed store, installation lifecycle handling. *Demo: one daemon serves two
  separate installations concurrently; events route correctly; uninstall tears a tenant down.*
- **M8 — Hardening & operability.** Per-tenant cost accounting + daily budget breaker, retries/backoff,
  prompt-injection guardrails, `/runs` + `/health` endpoints, structured logging, secret rotation story.
- **M9 (optional) — Marketplace & polish.** Marketplace listing, billing hooks, a small status
  dashboard, configurable column names; optionally a second `Board` provider to re-prove the abstraction.

Each milestone is independently shippable: a personal org-board install is fully usable after M6, before
multi-tenant exists.

---

## 9. Configuration / env (App-oriented)

```
# Provider selection
BOARD_PROVIDER=github

# App auth (preferred for the installable product)
WAZIR_GITHUB_APP_ID
WAZIR_GITHUB_PRIVATE_KEY            # the App .pem (path or contents); never committed
WAZIR_GITHUB_WEBHOOK_SECRET        # one secret for the App
WAZIR_GITHUB_AUTH=app              # app | pat   (pat/hybrid for user-owned single-installer)

# Single-installer convenience (multi-tenant learns these from webhooks instead)
WAZIR_GITHUB_INSTALLATION_ID       # pinned installation for personal use
WAZIR_PROJECT_OWNER / WAZIR_PROJECT_NUMBER

# Hybrid fallback (Path B) only
WAZIR_GITHUB_PAT                   # board GraphQL when the board is user-owned

# Claude
CLAUDE_CODE_OAUTH_TOKEN            # long-lived token for headless claude (M5 slice 1)
CLAUDE_BIN                         # path to the claude binary (injectable for tests)
WAZIR_CLAUDE_PLUGINS_DIR / WAZIR_CLAUDE_PLUGIN_ID / WAZIR_CLAUDE_SETTING_SOURCES

# Execution & safety
REPO_CLONE_PATH / WORKTREE_ROOT
BOT_LOGIN                          # filter self-events (the App's bot login)
MAX_BRAINSTORM_TURNS               # safety cap on the question loop
COST_BUDGET_USD_PER_DAY            # circuit breaker (per-tenant in multi-tenant mode)
```

---

## 10. Risks & gotchas (App-specific additions)

Carry over every gotcha from `wazir-init-plan.md` §12 (cost, no Go SDK, Projects v2 is GraphQL-only,
board-view refresh quirk, headless single-turn, prompt injection, webhook loops, permissions). The
following are new or sharpened by the App/multi-tenant goal:

- **User-owned Projects v2 is the headline constraint (§4).** App tokens can't touch it. Design around
  org boards; treat user-owned + full-App as an explicit, guided onboarding dead-end, not a runtime
  surprise.
- **One Agent SDK bill, many tenants.** In multi-tenant mode every tenant's `claude -p` usage draws from
  *your* metered Agent SDK credit. Per-tenant cost accounting and a per-tenant daily breaker aren't
  optional — a single chatty tenant can drain the shared pool.
- **Tenant isolation is a security boundary, not a convenience.** Concurrent `claude` runs across
  tenants must never share `CLAUDE_CONFIG_DIR`, `HOME`, worktrees, or tokens. A leak crosses customers,
  not just cards.
- **Installation token expiry.** Tokens last ~1 hour; long plan/execute runs can outlive one. Mint per
  network op (the `ghinstallation` transport does this) rather than caching a token for a whole run.
- **Webhook secret & key rotation.** A published App's webhook secret and private key are shared across
  all tenants — plan for rotation without downtime, and keep them out of the repo.
- **Bot identity & loops.** The App acts as its own bot user; filter its own comments/moves by the App's
  `BOT_LOGIN` + the hidden `<!-- wazir -->` marker, and dedupe by delivery id, exactly as today — just
  scoped per installation.

---

## 11. Out of scope (v1)

- Non-GitHub boards (Linear, Jira) — the ports make these *addable* (M9+), but only GitHub ships in v1.
- Auto-merging PRs (the human stays the final merge gate; Wazir never merges).
- Per-tenant billing/metering UI (Marketplace billing is M9, optional).
- User-level OAuth (Wazir uses installation tokens, not user-OAuth — acting *as the App*, not as a user).

---

## 12. Open questions

1. **Org board enforcement:** for the published App, do we *require* an org board (reject user-owned at
   onboarding), or ship hybrid PAT as a documented fallback for personal installers?
2. **Hosting the multi-tenant daemon:** single always-on box vs. a small autoscaling deployment — the
   worktree + clone footprint per tenant pushes toward a beefier persistent host than ephemeral runners.
3. **Welcome-card / setup UX:** how much to do on the Setup URL page (a real web flow) vs. purely via
   the `installation.created` webhook + a posted card.
4. **Cost model for a public App:** absorb Agent SDK cost, pass it through (require tenants to bring
   their own `CLAUDE_CODE_OAUTH_TOKEN`?), or gate behind Marketplace billing.
5. **Resume vs. reconstruct:** keep persisting `claude` `session_id` for `--resume` as an optimization,
   or always rebuild context from the board thread (the source of truth)?
```

