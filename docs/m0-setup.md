# Wazir M0 setup

## Prerequisites
- Go 1.25+ (required transitively by `golang.org/x/oauth2`), the `gh` CLI (for the provisioning spike).
- A fine-grained PAT with **Projects: read/write**, **Issues: read/write**,
  **Pull requests: read/write**, **Contents: read/write**, scoped to *every repo*
  whose cards appear on the board (init-plan §4.1).
- A Projects v2 board you own (user or org).

## Configure

Configuration is loaded with [fig](https://github.com/kkyr/fig): an optional
`wazir.yaml` file with environment-variable overrides. Copy the example and edit:

```sh
cp wazir.example.yaml wazir.yaml
```

```yaml
github:
  auth: pat          # pat | app
  owner_type: user   # user | org
project:
  owner: your-login
  number: 7
  board_name: Wazir
repos:
  - you/repo-a
bot_login: your-bot-login
store:
  db_path: ./wazir.db
```

Any field can be overridden by an env var named `WAZIR_<SECTION>_<FIELD>`.
Supply secrets via env rather than committing them to the file:

```sh
export WAZIR_GITHUB_PAT=$(gh auth token)   # overrides github.pat
export WAZIR_GITHUB_WEBHOOK_SECRET=...      # used by ParseEvent (M1)
```

A config file is optional — with none present, Wazir runs from env + defaults
(e.g. `WAZIR_PROJECT_OWNER`, `WAZIR_PROJECT_NUMBER`, `WAZIR_GITHUB_OWNER_TYPE`).

## Run

```sh
go build ./...
go test ./...                                    # unit suite

./wazir provision                                # create + reconcile columns
./wazir provision                                # run again: converges, no dupes
./wazir bootstrap                                # reconcile + cache an existing board

./wazir card comment <issue-node-id> "hello"     # exercise the Board port
./wazir card move <issue-node-id> Brainstorming
```

Global flags: `--config <path>`, `--log-level debug|info|warn|error`,
`--log-format console|json`.

## Integration test (real board)

```sh
WAZIR_GITHUB_PAT=$(gh auth token) WAZIR_GITHUB_OWNER_TYPE=user \
WAZIR_PROJECT_OWNER=your-login WAZIR_PROJECT_NUMBER=NN \
go test -tags integration ./internal/board/github/ -run TestIntegrationProvision -v
```
