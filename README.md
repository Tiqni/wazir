# Wazir

**A vizier for your dev workflow.** Wazir turns a GitHub Projects board into a human-gated,
AI-driven development loop: you write an idea or a bug as a card, and Wazir shepherds it from
brainstorming to a finished pull request — pausing for your approval at every gate.

> *Wazir* (Arabic وزير, "vizier") is the chief minister who orchestrated the affairs of state on
> the ruler's behalf — dispatching work, overseeing operations, and reporting up. This tool plays
> the same role for your codebase. (The ancient Egyptians called the same office the *tjaty*.)

> ⚠️ **Status: early / work in progress.** Expect rough edges and breaking changes.

---

## What it is

Wazir is a small Go service that sits between your GitHub Projects board and
[Claude Code](https://www.claude.com/product/claude-code). The board is your control surface — the
one place you watch and steer work. Wazir invokes Claude Code (with the
[Superpowers](https://github.com/obra/superpowers) plugin) as the "brain" for each phase, but **you**
stay in charge: nothing advances past a review gate without your explicit approval, and Wazir never
merges anything.

## How the loop works

You move a card across columns; Wazir does the work in between and reports back on the card itself.

1. **You write a card** — an idea to build or a bug to fix.
2. **Wazir picks it up** and starts brainstorming.
3. **It asks clarifying questions** as comments on the card.
4. **You answer** in the comments.
5. Steps 3–4 repeat until the requirements are clear.
6. **Wazir writes the spec** into the card.
7. **You review the spec** — leave comments to refine it, or approve to move on.
8. **Wazir plans and builds** in an isolated branch.
9. **It opens a pull request** and posts the link back for your review.

Every hand-off is visible on the board, and every gate waits for you.

## Why a board?

Because it's the interface you already use to think about work — and it's asynchronous. Drop an idea
in, walk away, come back to questions waiting for you, answer them on your own time, and approve when
you're ready. No terminal babysitting, no chat to keep alive.

## Requirements

- **Go** (recent stable release) to build the binary.
- The **`claude` CLI** installed and authenticated, with the **Superpowers** plugin available.
- A **GitHub Projects (v2)** board — Wazir can create one for you, or use one you already have.
- A **GitHub App or token** with access to the repos and project you want it to drive.

## Quick start

```sh
# build
go install github.com/EmadMokhtar/wazir/cmd/wazir@latest

# create the board (or point Wazir at an existing one)
wazir provision

# run the service
wazird
```

(See the configuration docs for the full set of environment variables.)

## Design notes

- **The board is the source of truth.** Wazir derives what to do from the card's column and its
  comment thread, so the system has no hidden state you can't see.
- **You own the gates.** Approvals are explicit; silence never auto-advances a card, and Wazir never
  merges a PR.
- **Pluggable by design.** GitHub is the first provider, but the board and code-host integrations sit
  behind interfaces, so other backends can be added later.

## License

Released under the [MIT License](./LICENSE).

## Acknowledgements

Built on [Claude Code](https://www.claude.com/product/claude-code) and the
[Superpowers](https://github.com/obra/superpowers) plugin.
