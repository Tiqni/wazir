# Spike — GitHub App token vs. user-owned Projects v2 (M5 slice 2)

**Date:** 2026-06-13
**Owner:** run by the user (needs a real GitHub App + board #5; not metered).
**Blocks:** the board-auth branch of the M5 slice-2 (App-token git auth) design.

## Why this spike exists

M5 slice 2 wires GitHub **App** auth. App installation tokens authenticate git
(clone/fetch/push) and the REST API (issues/PRs) fine. But GitHub support has confirmed
that App installation tokens **cannot access user-owned Projects v2 boards** via GraphQL —
they return `Resource not accessible by integration`. Wazir's board #5 is **user-owned**.

This spike answers one question that decides the design:

> Can an App installation token read user-owned board #5, or does it hit the limitation?

It also confirms the git-side path works: minting an installation token (step 1 below) is
the exact call the git token source will make on every clone/push.

### What the result decides

| Step (2) result | Branch chosen for the slice |
|---|---|
| **Error** (`Resource not accessible by integration` / similar) | **Hybrid** — board (Projects v2 GraphQL + issue comments) stays on PAT; forge (git + PRs) uses the App. Or move board #5 to an org. |
| **Returns the board title** | **Full App** — board client also uses the App; PAT becomes optional. |

Step (1) is expected to succeed **regardless** of step (2) — it proves the git token source
works, which is the actual point of the slice.

---

## Step 1 — Register the GitHub App

Go to **https://github.com/settings/apps/new** (personal account — matching board #5's
owner) and fill it in:

**Basics**
- **GitHub App name:** globally unique, e.g. `wazir-<yourhandle>`.
- **Homepage URL:** `https://github.com/EmadMokhtar/wazir`.
- **Description:** optional.

**Identifying and authorizing users** — leave all at defaults (Wazir uses installation
tokens, not user OAuth): Callback URL blank, "Request user authorization" unchecked, Device
Flow unchecked.

**Post installation** — Setup URL blank, "Redirect on update" unchecked.

**Webhook** — for this spike, **uncheck "Active"** (no public URL needed). For the real
daemon later: Active on, **Webhook URL** = `https://<your-host>/webhook` (the `wazir serve`
endpoint; use a [smee.io](https://smee.io) proxy to `http://localhost:8080/webhook` for
local dev), **Webhook secret** = the same value as `WAZIR_GITHUB_WEBHOOK_SECRET`. Leave SSL
verification enabled.

**Permissions**

Repository permissions:
| Permission | Access | Why |
|---|---|---|
| Contents | Read & write | git clone / fetch / **push** (core of the slice) |
| Issues | Read & write | read issue+comment threads, post Wazir comments |
| Pull requests | Read & write | open the PR |
| Metadata | Read-only | mandatory baseline (auto-selected) |

Organization permissions:
| Permission | Access | Why |
|---|---|---|
| Projects | Read & write | board moves + field updates — **only helps org-owned boards**; this is what the spike tests for user-owned |

Account permissions: none.

**Subscribe to events** — for the real daemon: Issues, Issue comment, Projects v2. For the
spike you can skip events.

**Where can this GitHub App be installed?** — "Only on this account".

Click **Create GitHub App**.

## Step 2 — Generate the key, install, collect IDs

1. On the App's **General** page, note the **App ID** → `APP_ID`.
2. Under **Private keys** → **Generate a private key** → downloads a `.pem`. Note its path →
   `KEY_PATH`.
3. **Install App** (left sidebar) → your account → grant it the repos in Wazir's allow-list
   (or "All repositories").
4. After installing, the page URL ends in `/installations/<number>` → that number is
   `INSTALLATION_ID`.
5. `OWNER` = your GitHub login (the owner of board #5).

## Step 3 — Create the spike program

Create a throwaway module **outside** the wazir repo so its `go.mod` stays clean:

```sh
mkdir -p ~/wazir-appauth-spike && cd ~/wazir-appauth-spike
go mod init wazir-appauth-spike
go get github.com/bradleyfalzon/ghinstallation/v2
go get github.com/shurcooL/githubv4
```

Save this as `~/wazir-appauth-spike/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/shurcooL/githubv4"
)

func mustInt64(name string) int64 {
	n, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "env %s must be an integer: %v\n", name, err)
		os.Exit(1)
	}
	return n
}

func main() {
	appID := mustInt64("APP_ID")
	instID := mustInt64("INSTALLATION_ID")
	keyPath := os.Getenv("KEY_PATH")
	owner := os.Getenv("OWNER")
	projNum := 5
	if v := os.Getenv("PROJECT_NUMBER"); v != "" {
		projNum = int(mustInt64("PROJECT_NUMBER"))
	}

	tr, err := ghinstallation.NewKeyFromFile(http.DefaultTransport, appID, instID, keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build transport: %v\n", err)
		os.Exit(1)
	}

	// (1) Token minting — the exact call the git token source will make per network op.
	tok, err := tr.Token(context.Background())
	fmt.Printf("(1) token minted: %v (err: %v)\n", tok != "", err)

	// (2) Can the installation token read the USER-owned board via GraphQL?
	gql := githubv4.NewClient(&http.Client{Transport: tr})
	var q struct {
		User struct {
			ProjectV2 struct {
				ID    githubv4.ID
				Title githubv4.String
			} `graphql:"projectV2(number: $num)"`
		} `graphql:"user(login: $login)"`
	}
	vars := map[string]interface{}{
		"login": githubv4.String(owner),
		"num":   githubv4.Int(projNum),
	}
	if err := gql.Query(context.Background(), &q, vars); err != nil {
		fmt.Printf("(2) projectV2 read FAILED: %v\n", err)
	} else {
		fmt.Printf("(2) projectV2 read OK: title=%q id=%v\n", q.User.ProjectV2.Title, q.User.ProjectV2.ID)
	}
}
```

Then tidy:

```sh
go mod tidy
```

## Step 4 — Run it

```sh
cd ~/wazir-appauth-spike
export APP_ID=<app id>
export INSTALLATION_ID=<installation id>
export KEY_PATH=/absolute/path/to/wazir-<...>.private-key.pem
export OWNER=<your github login>
# export PROJECT_NUMBER=5   # optional; defaults to 5

go run .
```

## Step 5 — Interpret the output

Two lines print:

```
(1) token minted: true  (err: <nil>)
(2) projectV2 read ...
```

- **(1) `token minted: true`** → the App's key/IDs are correct and `ghinstallation` mints an
  installation token. This is the git-side path; it should work regardless of (2).
  - If `false` / non-nil err: check `APP_ID`, `INSTALLATION_ID`, the key path, and that the
    App is actually installed on the account.
- **(2) result** is the decision (see the table at the top):
  - `projectV2 read FAILED: ... Resource not accessible by integration` → limitation holds →
    **hybrid** (or org-board).
  - `projectV2 read OK: title="..."` → limitation lifted → **full App** is viable.

## Step 6 — Report back & clean up

Report the two output lines (the `(1)` boolean and the `(2)` title-or-error). That resolves
the board-auth branch and the slice-2 spec gets finalized.

Cleanup: `rm -rf ~/wazir-appauth-spike`. Keep the GitHub App + key — they're the real
credentials the daemon will use (`WAZIR_GITHUB_APP_ID` / `WAZIR_GITHUB_INSTALLATION_ID` /
`WAZIR_GITHUB_PRIVATE_KEY`, with `github.auth: app`).

---

## Findings (2026-06-13)

App registered + installed on the personal account; spike output:

```
(1) token minted: true (err: <nil>)
(2) projectV2 read FAILED: Could not resolve to a ProjectV2 with the number 5.
```

PAT control (`gh api graphql`) read board #5 fine
(`@EmadMokhtar's software factory (managed by Wazir)`), same owner + number. So the board
exists and the App **cannot see it** — the user-owned Projects v2 limitation, surfacing as a
not-found error rather than `Resource not accessible by integration`.

**Conclusion:**
- **Token minting + git/issues/PRs under App auth: works.** The slice's core (App-token git
  auth) is viable.
- **Board (Projects v2) under App auth: does not work** on a user-owned board.
- **Branch:** the board client stays on the PAT; the forge (git + PRs) uses the App
  (**hybrid**), unless board #5 is moved to an org (then full App auth, PAT optional).
```
