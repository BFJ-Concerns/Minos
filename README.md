# Minos

[![CI](https://github.com/BFJ-Concerns/Minos/actions/workflows/ci.yml/badge.svg)](https://github.com/BFJ-Concerns/Minos/actions/workflows/ci.yml)

Minos is a self-hosted pull-request review service for GitHub and Forgejo.
When a pull request opens or its head changes, Minos starts an AI lead
(Claude Code by default) on a disposable Linux box. A panel of model
reviewers examines the whole diff, and a verifier, by default from a
different model family, checks every finding they raise. The lead then
reports the result on the pull request with a commit status, a reaction or
label, and, where there are findings, a review.

Minos only reviews. It never builds or tests the change, never pushes to the
pull-request branch and never merges. Fixing and landing stay with the
author, and the forge's own CI remains the build evidence.

## How a review runs

1. A signed webhook delivery, or the periodic sweep, finds an open, non-draft
   pull request that Minos has not yet finished reviewing at its current
   head. Pull requests on a work-in-progress branch, or on Forgejo blocked
   by an unresolved dependency, wait.
2. The run clones the pull request at its head. The lead marks it as in
   progress (on Forgejo it also requests a review from the bot account) and
   reads the project's guidance (the configured sources, or else the first
   non-empty file among `AGENTS.md`, `CLAUDE.md` and `README.md`) along with
   the pull request's title and description.
3. A quick first check decides whether the change gives the reviewers
   anything to look at. If it doesn't, the review finishes clean without
   running the full workflow.
4. An exploration pass plans the review and hands each part of the diff that
   needs attention to correctness, security, testing or design specialists.
   A verifier checks every finding they propose. The repository's own
   [review briefs](#review-briefs) then run the same way.
5. The lead classifies the verdict, guided by the repository's severity
   threshold, and publishes the result.

If any reviewer fails to finish, or any finding lacks a complete verifier
verdict, the run is incomplete: Minos publishes no review and sets no clean
status, and the sweep picks the pull request up again on a later pass.

With both engines available, the proposing and planning roles run on Codex
(`gpt-6-sol`) and the verifier on Claude (`claude-opus-5-5`), so every finding
is checked by the family that did not propose it. `[routing]` in
`service.toml` moves any role to another engine, model or effort, and a box
with only one engine runs every role on it.

## What the author sees

Minos reports through a commit status (`Minos` by default), a reaction or
label on the pull request, and, where there are findings, a review.

| Outcome | Status | On the pull request |
|---------|--------|---------------------|
| Under review | `pending` — Reviewing changes | The in-flight marker (an `eyes` reaction by default) |
| Continuing | `pending` — Review continuing in a fresh run | The in-flight marker stays while a fresh run carries on the review |
| Changes needed | `failure` — Changes need attention | A request-changes review carrying every confirmed finding, inline where it can be anchored to the diff and in the review body otherwise, plus the attention marker if one is configured |
| Clean | `success` — Changes approved | The clean marker (a `+1` reaction by default). Confirmed advisory findings go to the [filing destination](#configuration), by default a comment review |
| Incomplete | `error` — Review incomplete | No review; the head is retried on a later sweep pass. A run that fails before the lead starts writes no status |

Each finding comment opens with **Blocking** or **Advisory** and its
severity, and closes with the models that proposed and verified it (for
example, *Proposed by `gpt-6-sol`; verified by `claude-opus-5-5`.*). The
review body names the briefs it applied and the lead model that signed it.

Reviewers treat the pull request's description as the author's account of
the change. Where it explicitly puts something out of scope and the project
guidance doesn't contradict it, the missing piece is not reported as a
defect; incorrect behaviour in the code the change does carry still is.

## Requirements

- A disposable, single-tenant Linux machine with a systemd user manager
  (systemd 250 or later). The machine is the security boundary: the lead runs
  inside it without any further sandbox.
- Claude Code, the Codex CLI, Node.js, Git, Go, Bash, curl, jq, openssl, the
  OpenSSH client tools, tar, zstd, sha256sum and GNU find.
  [`scripts/expected-tool-versions`](scripts/expected-tool-versions) lists the
  versions the installer checks for.
- Model access: a Claude Code subscription or gateway credential, a Codex
  (ChatGPT) account, or both. One engine is enough.
- Memory: concurrent runs share a 22 GiB envelope by default
  (`runs.memory-envelope-gib`), and the [deployment guide](docs/deployment.md)
  covers sizing.
- On GitHub, a GitHub App installed on each reviewed repository. On Forgejo,
  a bot account and an access token.
- Project guidance in each reviewed repository: a configured source, or a
  non-empty `AGENTS.md`, `CLAUDE.md` or `README.md`. A run that finds none
  stops before reviewing.

The reviewed repositories' own toolchains are not needed, because Minos never
builds them.

## Installation

The [deployment guide](docs/deployment.md) covers every step in detail,
including the forge credentials and the archive host. In outline:

```sh
# Build and install the service binary.
go build -o minos ./cmd/minos
sudo install -m 0755 minos /usr/local/bin/minos

# Install the forge adaptations, per-run scripts and lead prompt under
# /opt/minos (see the guide for the exact layout), then the review runtime.
# The installer stops if a tool's version does not match the declared set.
scripts/install-review-runtime /opt/minos

# Copy the example configuration and fill in the placeholders.
sudo cp -r deploy/etc/minos /etc/minos

# Install and start the systemd user units.
minos install-units --config /etc/minos ~/.config/systemd/user
systemctl --user daemon-reload
systemctl --user enable --now minos-receiver.service minos-sweep.timer
```

Then point each forge's webhook at the receiver's `/hooks/<forge key>` route
(`/hooks/github` for a GitHub App) with the matching secret. The shipped
configuration listens on port 8919, and the shipped timer runs the sweep
every 10 to 12 minutes.

## Configuration

Configuration lives in `/etc/minos` by default (`--config` names another
root):

| File | Contents |
|------|----------|
| `service.toml` | Bot identity, forges, defaults for every repository, model routing and run limits |
| `repos/*.toml` | One file per reviewed repository, with any per-repository overrides |
| `run-body.env` | Lead engine and model, engine credential seeds and installed paths |
| `archive.env` | SSH settings for archiving each run's transcripts |

A repository file needs only the repository's identity and the run script:

```toml
forge = "github"   # the key of a [forges.<key>] table in service.toml
owner = "your-org"
repo = "your-repository"

[adaptation]
run-body = "/opt/minos/run-body/run-body"
```

Per-repository settings take their defaults from `[repositories]` in
`service.toml`, and a repository's own file overrides any of them:

| Setting | Default | Effect |
|---------|---------|--------|
| `review.threshold` | `High` | Severity threshold the lead weighs when classifying: `Critical`, `High`, `Medium` or `Low` |
| `filing-destination.kind` | `pull-request-comment` | Where a clean run's advisory findings go: committed to a `file` in a repository, filed as an `issue`, posted as a `pull-request-comment`, or `none` |
| `markers` | `eyes` in flight, `+1` clean | The reactions or labels Minos puts on a pull request |
| `work-in-progress-branch-prefixes` | none | Branch prefixes Minos leaves unreviewed |
| `guidance-sources` | first non-empty of `AGENTS.md`, `CLAUDE.md`, `README.md` | The documents a review is grounded on |

`runs.max-concurrent` (default `1`) sets how many reviews run at once. The
shipped [`service.toml`](deploy/etc/minos/service.toml) explains the common
settings and their defaults, and the [deployment guide](docs/deployment.md)
covers the rest.

## Review briefs

A repository can tell Minos what to look for by adding Markdown briefs under
`.review/`:

```markdown
---
relevance: Changes to authentication or session handling
---
Check that every new endpoint requires an authenticated session and that
tokens never reach the logs.
```

A brief's directory scopes it: `.review/internal/security.md` covers
`internal/`. Briefs run after the main review with the same verification,
and the review names the briefs it applied. The deployment guide sets out the
[discovery and frontmatter rules](docs/deployment.md#repository-review-briefs).

## Operating

The receiver and the sweep run as systemd user units:

```sh
systemctl --user status minos-receiver.service minos-sweep.timer
journalctl --user -u minos-receiver.service -u minos-sweep.service
systemctl --user start minos-sweep.service   # run a sweep pass now
```

- `/status` reports live runs, the sweep's deferrals and the receiver's
  heartbeat, and `/runs/recent` lists recently finished runs. Both are
  optional, read-only and behind a bearer token:
  [setup](docs/deployment.md#serving-run-status-to-an-operator-surface).
- With an alert repository configured, Minos files an issue there when the
  sweep fails or a repository is skipped on six consecutive passes.
- With an archive host configured, each run's transcripts and run records
  stream to it over SSH as a zstd tarball.
- With a failure ledger configured, the sweep records summaries of failed
  runs in the ledger's `FAILURES.md`.

## Building from source

The service binary needs only Go 1.26 or later:

```sh
go build -o minos ./cmd/minos
```

To check a build against Minos's own test suites as well, you also need
Node.js 24, jq and [just](https://github.com/casey/just):

```sh
just verify
```

## Repository layout

| Path | Contents |
|------|----------|
| [`cmd/minos`](cmd/minos) | The service binary |
| [`internal/`](internal) | Webhook receiver, sweep, run lifecycle and forge bridge |
| [`workflows/`](workflows) | The Ensemble review workflows and reviewer briefs |
| [`runtime/`](runtime) | The vendored Ensemble runtime |
| [`scripts/`](scripts) | Forge adaptations, per-run scripts and installers |
| [`lifecycle/`](lifecycle) | The lead's run prompt |
| [`deploy/`](deploy) | Example configuration and systemd units |
| [`docs/`](docs) | The deployment guide |
