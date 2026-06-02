# Wazir M0 setup

## Prerequisites
- Go 1.25+ (required transitively by `golang.org/x/oauth2`), the `gh` CLI (for the provisioning spike).
- A fine-grained PAT with **Projects: read/write**, **Issues: read/write**,
  **Pull requests: read/write**, **Contents: read/write**, scoped to *every repo*
  whose cards appear on the board (init-plan §4.1).
- A Projects v2 board you own (user or org).

## Configure
```sh
export GITHUB_AUTH=pat
export GITHUB_PAT=ghp_xxx
export OWNER_TYPE=user            # or org
export PROJECT_OWNER=your-login
export PROJECT_NUMBER=7
export BOARD_NAME=Wazir
export REPOS=you/repo-a,you/repo-b
export BOT_LOGIN=your-bot-login
export GITHUB_WEBHOOK_SECRET=unused-in-m0-but-required-by-parseevent
export WAZIR_DB=./wazir.db
```

## Run
```sh
go build ./...
go test ./...                                   # unit suite
./wazir provision                               # create + reconcile columns
./wazir provision                               # run again: converges, no dupes
./wazir card comment <issue-node-id> "hello"    # exercise the Board port
./wazir card move <issue-node-id> Brainstorming
```

## Integration test (real board)
```sh
go test -tags integration ./internal/board/github/ -run TestIntegrationProvision -v
```
