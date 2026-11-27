---
description: Install, configure and operate a Minos deployment, from host prerequisites and forge credentials to run liveness, status serving and review briefs
---
# Deployment

Minos runs on Linux under a functioning systemd user manager (systemd 250 or
later), because each review runs as a transient user unit; in a container,
use an image that runs systemd as its init. The host needs Claude Code or the
Codex CLI (at least one), Node.js, Git, Bash, curl, jq, openssl, OpenSSH
client utilities, ssh-keygen, tar, zstd, sha256sum and GNU find installed
(openssl signs the GitHub App token), plus Go wherever the service binary is
built. `scripts/expected-tool-versions` records recommended minimum version
ranges. Newer stable versions satisfy those ranges without a warning. The
default routing uses both engines: `claude-opus-5-5` on Claude Code, and
`gpt-6.2-astra` and `gpt-6.1-sol` on Codex. That takes a Claude Code
subscription or gateway credential for the Claude engine, and a Codex
(ChatGPT) account for the Codex engine. A host with only one engine
provisioned runs every role on it, and so checks no finding with a family
other than the one that proposed it. Reviews never build or test the
reviewed repository, so no repository toolchains are needed.

Minos adds no sandbox of its own: the lead holds the engine and forge
credentials and reads untrusted pull-request content with nothing between it
and its host. Isolating the deployment — a dedicated container or VM — is
recommended so that a misbehaving run can reach only what Minos itself
needs; it is not required.

Every model provider the deployment provisions receives the source of the
enrolled repositories: the lead and the review agents read the diff and the
repository's files through their engine, so Anthropic, OpenAI or a gateway
in between sees that source. The run archives carry it too. Each archive
holds the run's Claude transcripts, Codex rollouts and Ensemble records, and
those hold the source the agents read, so the archive host and its
destination keep copies for as long as the archives are kept. The failure
record carries it as well: the digest the sweep records for a crashed run
holds the tails of its transcripts, and goes to `runs.failure-log` or, with
`runs.failures-repo` set, is committed to that repository and pushed, which
may sit on another forge or be more widely readable than the repository
reviewed (see *How runs and the sweep behave* below). Enrol a private repository only where
its owner accepts all three.

## Check the release binary

In the directory containing the downloaded `minos-linux-amd64`, set `tag`
to the release's exact tag and run the check below. It compares the local
file with the SHA-256 digest GitHub records for that release asset. Install
the file only if the command prints `minos-linux-amd64: OK`.

```sh
tag=vX.Y.Z
digest=$(curl -fsSL "https://api.github.com/repos/BFJ-Concerns/Minos/releases/tags/$tag" |
  jq -er '.assets[] | select(.name == "minos-linux-amd64")
    | .digest | strings | select(test("^sha256:[0-9a-f]{64}$"))') &&
  printf '%s  minos-linux-amd64\n' "${digest#sha256:}" | sha256sum --check --strict
```

[releases]: https://github.com/BFJ-Concerns/Minos/releases

## Install and configure

1. On Linux amd64, download `minos-linux-amd64` from the
   [GitHub releases page][releases]. [Check its SHA-256 digest](#check-the-release-binary)
   and install it with `sudo install -m 0755 minos-linux-amd64 /usr/local/bin/minos`.
   Use the source files for the same tag for the remaining steps. For a
   release without a binary asset or for another architecture, run
   `go build -o minos ./cmd/minos` from the source tree with Go 1.27.2 or
   later. Install it with `sudo install -m 0755 ./minos /usr/local/bin/minos`.
2. Install the whole of `scripts/adaptations/` under
   `/opt/minos/adaptations` — one directory per forge (`forgejo`, `github`),
   each keeping its basenames and modes; install the whole of `scripts/run-body/`
   under `/opt/minos/run-body`, each file keeping its basename and mode. Copy
   the directory rather than a named list: the scripts source their shared
   pieces — `cgroup-memory.sh`, `archive-transport.sh` — from beside
   themselves, and a run-body missing one fails at the step that needed it.
   Install `scripts/provision-archive-transport` and
   `scripts/provision-failure-checkout` under `/opt/minos`, with executable
   mode. On the archive host, install `scripts/archive-receiver` where the
   archive account can run it and provision the account with
   `provision-archive-transport destination PUBLIC_KEY DESTINATION
   AUTHORIZED_KEYS BOX_ADDRESS RECEIVER_PATH`: the box's key lands in the
   account's `authorized_keys` as one `restrict,from=…,command=…` line, so
   the key opens no shell and reaches exactly three operations — landing
   an archive artefact beside its final name under `DESTINATION`,
   committing it there once the box's own pipeline has succeeded, and
   listing the timing sidecars — from the box's address alone. Re-running it replaces
   an earlier line for the same key. Install `lifecycle`
   under `/opt/minos/lifecycle`. Copy `docs/` to `/opt/minos/docs` — the
   installed systemd units carry `Documentation=file:/opt/minos/docs/deployment.md`,
   and the runtime installer does not install them. Install
   `deploy/environment.d/50-minos-path.conf` as
   `~/.config/environment.d/50-minos-path.conf` — the shipped file already
   uses `${HOME}` for the per-user bin directories, so it is portable
   without edits. After installing, run `systemctl --user daemon-reload`
   and re-login (or `systemctl --user import-environment PATH` to apply
   an existing shell PATH immediately). Transient run units inherit PATH
   from the user manager; the manager's default PATH omits per-user
   directories like `~/.local/bin` and `~/.cargo/bin`, where the agent
   CLIs are commonly installed.
3. Run `scripts/install-review-runtime /opt/minos`. Before installing
   anything it checks the host's tools against
   `scripts/expected-tool-versions`: it stops, naming every gap, when a
   required tool check fails, neither engine CLI is installed, or the review
   runtime refuses to start under the installed Node. For tools with a
   recommended minimum, it warns about older or unrecognised versions.
   It then installs the complete test-free
   review runtime under `/opt/minos`: the vendored Ensemble launcher and
   its provenance files, the adjudication wrapper and adapter, and every
   non-test workflow file. It also verifies the launcher against
   `ensemble.mjs.sha256`.
4. Copy `deploy/etc/minos` to `/etc/minos`, replace the placeholder values,
   and add one repository TOML file per opted-in repository under
   `repos/`. The service discovers repositories by globbing `repos/*.toml`:
   the shipped `owner--repository.toml.example` is a template, not a loaded
   file — copy and rename it to end in `.toml`. The filename itself carries
   no identity; `forge`, `owner` and `repo` inside the file do.

   `service.toml` and every repository file are read strictly: a key Minos
   does not know stops the load with `unknown TOML keys:` and the file and
   keys it found. The receiver reloads service and repository settings for
   each webhook delivery. Restart the receiver for changes to its listener
   settings.

   The `.env` files (`run-body.env`, `archive.env`) are sourced as shell
   programs with `set -a` (automatic export), not parsed as generic dotenv
   files. Shell expansion and commands execute during loading; values must
   obey shell quoting rules.

   In `run-body.env`:

   - set `MINOS_CLAUDE` to the absolute path of the installed Claude
     executable — install Claude Code natively, not as an npm package.
     The lead uses this
     path directly; Ensemble workers resolve `claude` and `codex` by
     command name through the inherited PATH (the `environment.d` file
     from step 2), so both the absolute variable and PATH must be correct;
   - on upgrade, set `MINOS_RUN_DRIVER` to the installed
     `workflows/run-driver.mjs` to use the scripted driver with an existing
     configuration file;
   - to use the resident lead, set `MINOS_LEAD_ENGINE` to `claude` or `codex` (`claude` by default)
     and `MINOS_LEAD_MODEL` to the model the lead runs on — `claude-opus-5-5`
     by default for Claude, `gpt-6.1-sol` for Codex; a Codex lead needs
     `MINOS_CODEX` set to the installed Codex executable;
   - set `MINOS_CLAUDE_CONFIG_SEED` to a directory containing known-good,
     non-interactive Claude configuration — required when the lead engine is
     `claude`;
   - set `MINOS_CODEX_CONFIG_SEED` to a directory containing known-good,
     non-interactive Codex ChatGPT authentication state, or leave it unset
     to run without the Codex engine: an engine is provisioned exactly
     when its seed is named, and the workflows route every role by what is
     provisioned (`[routing]` in `service.toml` names any role the operator
     pins; unset roles pair the families when both are provisioned and
     run on the one engine otherwise); and
   - check that `MINOS_LIFECYCLE_INSTRUCTION`, `MINOS_REVIEW_WORKFLOW`,
   `MINOS_ARCHIVE_RUN` and the
   other installed paths match the deployment. Configure `archive.env` with
   the archive SSH host, identity and pinned known-hosts file; the
   destination lives on the archive host, bound into the key's forced
   command below, so the box never names it.

   Per-repository knobs — admission rules, review threshold, whether
   safeguard requests are filed, the outcomes push inspection may choose,
   work-in-progress branch
   prefixes, the guidance sources a review is grounded on, the filing
   destination for advisory material and configuration diagnostics,
   and the markers Minos writes on a pull request — are set once under
   `[repositories]` in `service.toml` and overridden whole by any
   repository's own file; the shipped `service.toml` documents each knob
   and the defaults a repository gets when neither level sets it. A
   guidance source in a secondary repository is cloned read-only beside
   the workspace at setup.

   The filing destination controls where confirmed advisory findings,
   unverified material — observations from outside the change and findings
   a verifier left unsettled, filed as open questions — and
   configuration diagnostics — brief misconfigurations and unreadable guidance
   sources — are delivered: `kind` is one of `file`
   (path required; repository optional, defaulting to the reviewed
   repository's default branch), `issue` (repository required — the
   owner/name that takes the issues; no path), `pull-request-comment`, or
   `none`. Unset at both levels, it defaults to `pull-request-comment`.
   The `pull-request-comment` kind deduplicates against the service
   account's existing review bodies on the pull request, then posts one
   comment-review with the remaining advisory entries; when none remain,
   nothing is posted — there is no all-clear comment. This kind never posts
   unverified material: observations and unsettled findings reach no
   surface on a repository whose destination is `pull-request-comment`.
   The `none` kind discards everything it is given. A
   file-kind destination is a separate write path committed as
   `service.commit-author-name` (default: the bot login) and
   `service.commit-author-email` (default: the bot login at
   `minos.invalid`), as are the failure ledger's commits. If delivery is
   unavailable, the run report records the failure without posting a
   fallback comment.

   Markers define the form of each marker Minos writes on a pull request:
   `in-flight` while a run is active, `clean` on a clean outcome, and
   `attention` on a request-changes outcome. Each is a reaction or a
   label. Unset, `in-flight` defaults to the eyes reaction and `clean` to
   the +1 reaction; no attention marker is written unless configured. A
   label must already be defined: on Forgejo, on the repository or its
   organisation; on GitHub, on the repository itself, since an
   organisation-level label does not count there. Minos never creates
   labels, and refuses the marker write when the
   label is missing. A repository's own `[markers]` table replaces the
   service-level one whole.

   Every commit status Minos writes carries `service.status-context`
   (default `Minos`), and the sweep's completion marker is read under
   that context only. A status's Details link lands on the review Minos
   posted for the reviewed head, or on the pull request itself when it
   posted none. On either forge, `[forges.<key>] web-base` sets the link's
   host and optional path prefix as an absolute HTTP(S) URL; set it on a
   GitHub forge whose web host is not the one Minos derives. When unset,
   the link's host comes from `api-base`: Forgejo strips a trailing
   `/api/v1`, and GitHub uses `https://github.com` for
   `https://api.github.com` and strips a trailing `/api/v3` from any other
   API base, which gives the web host of a GitHub Enterprise Server.

   The status is review information, not a merge gate: do not make this
   context a required status check in branch protection. It can be missing
   or stale on a head. A run that fails before the lead launches writes no
   status, a status write Minos cannot read back is reported as uncertain
   and may or may not have replaced the previous status, and a lead that dies can leave `pending` behind until a
   later run replaces it. When a continuation run makes no progress beyond
   the run it continues, admission stops it instead of starting it again:
   it writes `attention` on the head, removes the in-flight marker and
   records the stall in `runs.failure-log`.

   `runs.max-concurrent` caps how many run units may be live at once, and
   defaults to one when unset; it must not exceed `runs.memory-envelope-gib`.
   That service-only knob defaults to 8 GiB. Run units share the configured
   whole-box memory envelope live, through the `minos-runs.slice` unit.
   The shipped slice holds `MemoryHigh=6G` and `MemoryMax=8G`, so no run feels any pressure
   until the runs *together* approach the envelope — a lone run may use all
   of it — reclaim then pushes them back, and only combined demand the
   envelope cannot hold reaches the kernel's out-of-memory handling inside
   the slice. Each unit also
   carries its own `MemoryMax` at the whole envelope as the backstop for a
   box missing the slice unit. Size the cap against the machine's memory
   and cores. `minos install-units` (step 5) renders the installed slice
   from this knob — `MemoryMax` at the envelope, `MemoryHigh` two GiB below
   it but never under 1 GiB — so after changing the envelope re-run it and reload the user
   manager. At startup the receiver logs a warning when the installed
   slice's `MemoryMax` differs from the configured envelope.
   `runs.duration-ceiling` sets each run's hard
   duration limit (a Go duration of at least `1us`, default `12h`).
   `runs.pressure-threshold-percent` sets the continuation threshold as a
   percentage of the run ceiling (1–100, default 85); the watch signals after
   two consecutive samples of anonymous memory plus swap at that threshold.
   With a higher concurrency cap, raise the envelope toward the runs'
   combined peak, measured as below. Each run paces `ensemble.concurrency-claude` and
   `concurrency-codex` workers of its own (the loader defaults each one left
   unset to 2; the shipped example comments both at 2).
   `ensemble.agent-ceiling` optionally caps a workflow's agents across
   both engines together; unset, the runtime applies no combined cap (the
   shipped example comments an illustrative value of 4).

   `[routing]` maps each workflow role — `exploration`, `proposer`,
   `verifier`, `brief-planner`, `judgement`, `description` — to an
   engine, model and effort. A role left unset defaults at run time: with
   both engines provisioned, proposing and planning roles (`exploration`,
   `proposer`, `brief-planner`) run on Codex, and the
   `verifier`, `judgement` and `description` on Claude. With one engine provisioned, every
   role runs on it. A role on
   Claude defaults to `claude-opus-5-5`; on Codex the `proposer` defaults to
   `gpt-6.2-astra` and every other role to `gpt-6.1-sol`. Default effort is `high` for
   `exploration` and `brief-planner`, and `medium` for `proposer`,
   `verifier`, `judgement` and `description`. `judgement` is the role
   of the run driver's one-shot judgement stages
   (`workflows/judgements/`), and `description` the role of the stage
   that writes the description Minos keeps of a repository. An effort is one of the Ensemble
   runtime's levels: `none`, `minimal`, `low`, `medium`, `high`, `xhigh`
   or `max`. The verifier's routing also carries
   the leg that merges findings one fix would resolve before they are
   verified. A finding a verifier raises while checking another is
   verified on the `proposer`'s routing when that runs on the other engine,
   so it too is checked by the family that did not raise it. A configured
   role overrides only the fields it sets, and a role that sets `model` must
   also set `engine`, the model's family: a model alone stops the
   configuration from loading.

   `[routing.concerns.<concern>]` gives one specialist concern — `failure`,
   `fit`, `logic`, `security` or `tests` — routing of its own. A concern
   left unset proposes on the `proposer`'s routing; a set concern starts
   from it, replaces the fields it sets, and takes the `proposer`'s default
   model for an engine it moves to without naming a model. Each finding is
   verified on the `verifier`'s routing unless the `verifier` runs on the
   engine that proposed it, in which case the `proposer`'s routing verifies
   it when that runs on the other engine; so a concern moved to Claude
   beside a Claude verifier still has its findings checked by Codex. A name
   outside the shipped concerns stops the configuration from loading.

   `[routing]` in `service.toml` is the service-wide routing. A
   repository's own file may carry `[routing]` too, and each role and each
   concern it sets replaces the service's whole: the fields it leaves out
   take the defaults above, not the service's values. Every role and
   concern the repository leaves out keeps the service's routing.
   On an upgrade, remove any `[routing.engagement-gate]` tables. Push
   inspection runs on the `judgement` role in scripted mode.

   Size every capacity decision — this cap, the slice envelope, the box
   itself — by **anonymous memory plus swap peak, never the journal's cgroup
   memory peak**. The journal's figure includes reclaimable page cache, so
   it can sit at `MemoryMax` well above the memory the run actually needs.
   Measure anonymous demand from the unit cgroup's
   `memory.stat` (`anon`) plus `memory.swap.current` at peak — the same
   metric the run's pressure watch reads.

   `MINOS_LEAD_SILENCE_TIMEOUT` optionally overrides the supervisor's
   3600-second backstop. The timer measures changes to files in the run
   directory outside the run's private home: a run whose files there have
   not changed for that long is stopped as a supervision failure. A Claude
   lead's session lives in the private home, so its output does not count;
   a Codex lead's output is written to `codex-lead.jsonl` and
   `codex-lead.stderr` in the run directory, so its output does. Keep the lifecycle's fallback wake shorter
   than this value.

   Choose one Claude authentication mode in `run-body.env`:

   - For direct subscription authentication, leave
     `MINOS_ANTHROPIC_CREDENTIAL_FILE` unset. The Claude seed must contain the
     subscription state. Before launching the lead, `run-body` removes ambient
     `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`,
     `ANTHROPIC_DEFAULT_HAIKU_MODEL`,
     `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY`,
     `CLAUDE_CODE_MAX_CONTEXT_TOKENS`, `CLAUDE_CODE_AUTO_COMPACT_WINDOW` and
     `MINOS_ANTHROPIC_CREDENTIAL_FILE`.
   - For gateway authentication, set `MINOS_ANTHROPIC_CREDENTIAL_FILE` to a
     readable file whose first line is the gateway token, and set
     `ANTHROPIC_BASE_URL` to the gateway URL. Keep any gateway-specific Claude
     settings, such as `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY`, in
     `run-body.env`. `run-body` exports the file token as
     `ANTHROPIC_AUTH_TOKEN`; a missing base URL or missing, unreadable or empty
     credential file stops the run before Claude launches.

   Provision the seed directory of every engine the deployment runs when the
   host is provisioned — the lead's engine at least. Each run copies
   their contents into its private `HOME`: Claude state goes to
   `$HOME/.claude` and Codex state to `$HOME/.codex`; no skills are
   installed into either home — the lead needs none, and review workers run
   with their skill surfaces stripped. After copying, `run-body` overwrites
   `$HOME/.claude/.claude.json` with an acceptance stub — onboarding and
   bypass-permissions accepted (`hasCompletedOnboarding` and
   `bypassPermissionsModeAccepted`), and the run directory the lead runs
   from trusted as the run's only workspace — so any seed content in that
   file is not preserved; put seed configuration in other files within the
   seed directory. Each run also gets a private disk-backed `TMPDIR` beneath the
   storage root's `tmp/` directory, alongside `runs/`, so workers' large
   temporary files land on that disk rather than in `/tmp`, and are removed
   with the run. The lead and Ensemble workers
   therefore inherit both
   engines' configured authentication without an interactive login. Gateway
   settings reach the Claude workers through the
   same inherited environment; Codex continues to use its own `CODEX_HOME`
   authentication.

   Each seed directory must contain only seed content readable by the run user.

   `MINOS_LIFECYCLE_INSTRUCTION` points to the vendored `lifecycle.md`: editable
   markdown fed to the lead session as its standing instructions and launch
   prompt. A review-panel-style walkthrough is the model, not a literal
   installed skill.

   Create the webhook secret and forge credential files separately. For a
   Forgejo forge the credential file holds the service account's access
   token. The token needs these Forgejo scopes: `read:user` (identity
   verification), `write:repository` (cloning, branch reads, commit
   statuses, requested reviewers, and reviews), and `write:issue`
   (reactions, labels, dependency lookups, and issue filing). The account
   Minos uses for this forge — the forge table's `bot-login` where it is
   set, otherwise `service.bot-login` — must match the token's
   authenticated identity; the claim adapter rejects a mismatch. The Forgejo instance must expose the
   issue-dependencies API (`/api/v1/repos/.../issues/.../dependencies`); a
   missing or disabled endpoint produces "dependency state unavailable" and
   defers review until the next sweep pass.

   For a GitHub forge, register a GitHub App (permissions: pull
   requests and commit statuses read and write, contents read — and write
   where a `file` filing destination or the failure ledger pushes to an
   installed repository — and issues read and write where a filing
   destination needs them; subscribe it
   to pull request, pull request review and issue comment events; one
   webhook pointing at `/hooks/github` with the secret), install it on each
   repository Minos reviews, and write the credential file as JSON:
   `{"app-id": "<App id>", "private-key-file": "/etc/minos/github-app.pem"}`
   beside the downloaded private key. The adaptation asks GitHub which
   installation serves each repository and caches its token until it nears
   expiry. One forge entry can serve repositories under several installation
   owners. The adaptation reads only `app-id` and `private-key-file`, and
   ignores any other field, such as an `installation-id`. The box needs `openssl` to sign the App JWT. GitHub shows the App's
   slug with the `[bot]` suffix (`minos-review[bot]`) on every write the
   App makes. Minos checks writes against that login. Set it as
   `service.bot-login` when GitHub is the only forge; when another forge's
   account holds `service.bot-login`, set it as `bot-login` in
   `[forges.<key>]` instead, which overrides the service's login on that
   forge alone. A GitHub App cannot be a requested reviewer, so the claim on
   GitHub is the in-flight marker alone.
5. Install the systemd user units. Run
   `minos install-units --config /etc/minos ~/.config/systemd/user` as the
   deployment user, then `systemctl --user daemon-reload` and enable
   `minos-receiver.service` and `minos-sweep.timer`. The units are embedded
   in the binary (the sources are `deploy/systemd/user`); the command
   writes every service whose Exec line runs the binary with the
   configuration root it was given, `minos-runs.slice` with the configured
   memory envelope, so runs share that envelope rather than a copied
   default. It installs the timer, sweep request path and
   `minos-sweep-after-run@.service` template verbatim.
6. Optionally, configure a forge webhook so a review starts as soon as a
   pull request changes rather than on the sweep's next pass; the sweep
   finds every pull request needing review without one. Point it at `/hooks/<forge key>` —
   `/hooks/forgejo` for the shipped Forgejo table, `/hooks/github` for a
   GitHub App — using the matching secret. The path suffix after `/hooks/`
   must match the key used in `[forges.<key>]` in `service.toml`; a
   mismatch returns "unknown forge".

## Pull request admission

Set fleet defaults in `[repositories.admission]` in `service.toml`. A
repository's `[admission]` table replaces the entire fleet table. Use an
empty table to restore the shipped defaults for one repository.

| Key | Default | Rule |
| --- | --- | --- |
| `labels` | `[]` | If set, require at least one of these labels. |
| `exclude-labels` | `[]` | Any of these labels excludes the pull request, even if an admission label matches. |
| `authors` | `[]` | If set, require a listed forge login. |
| `base-branches` | `[]` | If set, require an exact target branch name. |
| `drafts` | `"ready"` | Review ready pull requests (`ready`), both ready and draft (`any`), or only drafts (`only`). Forgejo's `WIP:` titles count as drafts. |
| `title-pattern` | `""` | If set, require a Go regular expression match anywhere in the title. Use anchors for a whole-title match. |
| `paths` | `[]` | If set, require at least one touched path to match. |
| `allow-forks` | `false` | Allow a source repository different from the enrolled repository. Minos always requires the source repository's identity. |

Empty lists impose no filter. All configured filters must pass. Author
logins are matched case-insensitively. Labels, branches and paths are
case-sensitive. Use separate labels for admission and Minos markers so
marker updates do not change admission.

In `paths`, a trailing slash specifies a literal directory prefix, such
as `internal/`, including nested files. Other entries use Go
`path.Match` syntax. For example, `docs/*.md` matches Markdown files
directly in `docs/`. A rename can match either its old or new path, and
a deletion can match the deleted path. Minos reads the pull request's
file list from the forge only when a path filter is needed. An incomplete
or unavailable file list leaves the pull request unstarted, and the sweep
names it in the deferral list. GitHub returns at most 3,000 files for a pull
request. This limit affects admission only when Minos needs the list to
evaluate a path filter.

Webhook deliveries and the periodic sweep both use current forge facts.
A pull request excluded by the rules gets no new run or forge write.
A run admitted earlier can still be in flight when the rules or pull
request change.

## How runs and the sweep behave

Run units, run directories and continuation handoffs include the configured
forge key in their names (for example `minos-run-forgejo-owner-repo-pr1`), so
the same owner and repository on different forges have separate run
names. Long repository prefixes keep a digest of the full identity so the
run unit, its exit hook and its archive have room for their suffixes.

A run that exits cleanly requests a sweep pass so the freed slot can refill
before the periodic timer fires. Its `OnSuccess=` hook starts an instance of
`minos-sweep-after-run@.service`. The instance leaves a request file under
the user's runtime directory, starts the path watcher and exits so the user
manager can collect the run unit. The watcher starts `minos-sweep.service`
when the file exists. The sweep clears the file before reading the forge.
If a run exits while a sweep is already active, the request stays pending
and the watcher starts a fresh pass once the active sweep exits.

A successful run exits after a `clean` marker, a continuation, or a recorded
`clean` or `attention` forge status. A run that leaves its head eligible
exits with status 75 and waits for the timer. An operator's
`systemctl --user stop` of a run unit also requests a sweep.

The shipped `run-body.env` selects the scripted driver. The script carries
the run from claim to conclusion and launches one-shot judgement stages
when a decision needs reading. An installation with either judgement
module missing falls back to the resident lead.

To use the resident lead, set `MINOS_RUN_DRIVER=""` in `run-body.env`. The
lead runs on the configured engine and model. After useful run activity
begins, `run-body` keeps resumable `done` and `blocked` turns alive. It
stops the session when the lead reports `failed` or `stopped`, when the
lead writes a terminal marker (`clean`, `non-clean` or `continuation`), or
when the run tree outside its private home reaches the silence timeout.
The supervisor records a failure if the lead blocks before its first
run-tree change or reaches the silence timeout.

The review and repository-brief workflows run through `adjudicated-review`.
The wrapper finds the launcher in the sibling `runtime/` directory. It
checks the run record for completion of every required leg and a verdict
on every finding. The transient systemd unit bounds the run at
`runs.duration-ceiling`.

A failure during run setup, such as an unreadable configuration or lifecycle
instruction, leaves the forge status untouched. The run body
attempts to append the failure to `runs.failure-log`; when the log path is
unset or unwritable, stderr carries the evidence instead. The run exits after
recording the failure. A run whose lead ends with a failing exit and no
terminal marker is appended to the same log as a `lead-supervision` failure.
When workspace
setup cannot fetch the admitted target SHA because the forge no longer
advertises it (typically because the base branch moved), the failure cause
carries that refusal; the next sweep pass re-derives from current forge state.
Workspace setup resolves clone URLs and credentials through the forge-neutral
`minos forge repository-metadata OWNER REPO` and
`minos forge clone-credential OWNER REPO` operations. Git reads the
repository's authorisation header from an owner-only temporary configuration
file. Uninstalled public GitHub forks can be cloned anonymously.

After the lead has finished its forge writes, `run-body` streams its report,
Claude transcripts, Codex rollout JSONLs and Ensemble run records as a zstd tar
archive to the configured destination. Before building the tarball it runs
`collect-timings`, which assembles the structured per-run timing record —
every timed command span from the run's `timings.ndjson` event log plus every
dispatched worker's timings from the Ensemble records — and after the tarball
is promoted it delivers that record a second time as a sidecar beside it —
the tarball's name with `.tar.zst` replaced by `.timings.json` — readable
without extracting anything. This presentation archive and its sidecar are
best-effort and cannot change the run result. Until the archive command
succeeds, the run directory carries a `.archive-pending` file and is kept for
the sweep to archive again; a run that cannot write that file exits with
status 74 and keeps its directory. Before each reconciliation pass,
the sweep records a digest for each dead run with crash evidence and removes its
scratch directory once the configured archive destination accepts its evidence.
A dead run already marked `.archive-pending` is archived again without a
second digest. Without an archive command, the sweep removes the directory
after recording the digest.
With `runs.failures-repo` configured, the sweep appends to the checkout's
existing regular `FAILURES.md` and attempts to commit and push it.
`FAILURES.md` must already exist in the repository. Create the checkout with
`provision-failure-checkout HTTPS_CLONE_URL DESTINATION CREDENTIAL_FILE`: it
clones the repository and checks with a dry-run push that it can push to
`main`. Its credential file is a plain access token on the first line, sent as
`Authorization: token …`. That is the Forgejo token file as it stands; a
GitHub App credential file does not work here, so give it a token that can
clone and push the ledger repository. Later pushes authenticate through the
forge adaptation as described below. Without
`runs.failures-repo`, the sweep appends to `runs.failure-log` and limits access to the file's owner
because a digest can contain transcript excerpts. Both destinations must be outside
`runs.dir`. A failed append or archive leaves the run directory for retry.
`runs.failures-forge` selects the forge whose credential authenticates
the ledger push; it defaults to the sole configured forge and is required
when several forges are configured. Active units
and directories named by valid pending continuation handoffs are preserved.
Each digest is limited to 256 KiB and reads only the run-local report and
failure log, Claude transcripts, Codex rollouts and exact Ensemble record tree.
The sweep fetches and rebases its append-only commit onto `origin/main` before
pushing. The push authenticates through the selected forge adaptation's
`clone-credential` operation using the separate
`runs.failures-credential-file`. For GitHub, Minos resolves the installation
for the repository in the ledger checkout's HTTP(S) push URL for `origin`.
Git reads the authorisation header from an owner-only temporary
configuration file.

The receiver handles new events immediately. The sweep periodically starts
eligible open pull requests whose current head carries no completion marker.
Once the admission rules pass, reconciliation strips stale Minos clean or
attention markers left on a head the author has replaced. Reviews and
statuses identify the head they cover. Reactions and labels apply to the
whole pull request. A label marker must already exist on the repository
or, on Forgejo, its organisation. If the label is missing, Minos refuses
the write and names the label and repository.

When a run sets a `clean` or `attention` status on a head, Minos records
which files the pull request changed and how, in `.reviewed/` under
`runs.dir`, one file per pull request. A later run compares the new head with
that record to decide how much to review again. The record is a cache.
Without it, Minos looks among the newest 20 commits of the pull request's
current history, other than the head, for one that carries Minos's `clean`
status or a Minos request-changes review, and compares against the first it
finds. When none of those commits qualifies, because the earlier head was
rewritten away or lies further back, the next head is reviewed in full. The
sweep deletes the record of a pull request that has closed, merged or gone from the forge, and
the records of a repository that no longer has a file under `repos/`.

## Reading run liveness

The obvious liveness signals invert during a correct yield, so a correctly
behaving lead can look dead. While the lead
waits on a background workflow it has ended its turn: the run tree's scratch
mtimes go stale, no lead processes are doing visible work, and the journal
prints nothing — exactly the picture a dead run would show. Do not judge
liveness from scratch mtime, process activity, or journal silence.

Find the run first. Its unit is named for the forge key, repository and pull
request, and its directory under `runs.dir` is the unit name with a random
suffix:

```sh
systemctl --user list-units 'minos-run-*'
ls -d /var/lib/minos/runs/minos-run-forgejo-owner-repo-pr1-*   # runs.dir as shipped
```

A unit that is neither `activating` nor `active` is not a live run,
whatever its files say; Minos itself counts both states as live.

For a **Claude lead**, the run body asks Claude Code itself: it runs
`claude agents --json --all` with the run's private home and reads the
`state` of the background session it launched (named
`minos-<owner>-<repo>-pr<n>`). `running` and `blocked` keep the run going,
except a lead blocked before its first change to the run tree; `failed` and
`stopped` end it. An operator can read the same roster:

```sh
run=/var/lib/minos/runs/minos-run-forgejo-owner-repo-pr1-XXXXXXXX
HOME="$run/home" CLAUDE_CONFIG_DIR="$run/home/.claude" claude agents --json --all
```

A lead session waiting on a background workflow between turns is live while
its unit is active.

For a **Codex lead**, the run body observes process liveness (`kill -0`) and
exit status: a Codex lead that is still running is live; one that has exited
with status 0 is stopped (success), and any other exit is failed. There is no
job-file state to inspect.

The run supervisor's own silence backstop (`MINOS_LEAD_SILENCE_TIMEOUT`)
already bounds a genuinely wedged lead on either engine — do not kill a
waiting run ahead of it on the strength of quiet files.

## The web page

The receiver serves a web page for the operator at the root of its listener
(`http://BOX:8919/` with the shipped `bind`), behind one sign-in. It opens on
an overview of the enrolled repositories. Each repository's Description
link opens the account Minos keeps of its structure, with the default-branch
commit it describes and when Minos recorded it. The settings pages show and
change the configuration. Nothing Minos does depends on the
page: a deployment that never opens it reviews exactly the same.

The page is served once the deployment holds an operator credential. Create
one, or write a passphrase of your own into the file:

```sh
umask 077 && head -c 24 /dev/urandom | base64 > /etc/minos/page.credential
```

The shipped `service.toml` already names that file; restart the receiver
after adding the key to a configuration that lacks it:

```toml
[listener]
bind = ":8919"
page-credential-file = "/etc/minos/page.credential"
```

Open the listener's address in a browser and sign in with the file's content.
The credential is the whole gate, so keep it as you would the forge token.

- A browser stays signed in for 30 days, and each visit in the second half
  of that time renews it. **Sign out** ends the session in that browser.
- Replacing the file's content changes the credential at once, with no
  restart, and signs every browser out. That is also how to end a session
  on a browser you no longer hold.
- The receiver checks at most one sign-in attempt a second, however many
  arrive.
- The listener speaks plain HTTP, so the credential crosses the network in
  the clear at sign-in. Keep the listener to a network you trust, or put a
  TLS-terminating proxy in front of it; behind a proxy that sends
  `X-Forwarded-Proto: https`, the session cookie is marked for TLS only.
- The page shares the listener with the webhook route. Where a forge reaches
  the listener from the internet, have the proxy forward `/hooks/` alone
  unless you mean the sign-in page to be public too.

**Settings** lists every setting of `service.toml`, with the value that
applies and where it comes from: the file, or the documented default. Each
enrolled repository has a settings page of its own, reached from there,
where each per-repository setting shows whether the repository's file, the
service-wide value in `service.toml` or the default sets it. Open a setting
to change it, or to remove it from that file so the next level out applies
again; a list or table is typed as the file would hold it after the key's
equals sign.

- A change is written to the same file you would edit by hand, `service.toml`
  or the repository's file under `repos/`. The surrounding comments and
  layout stay where they are. Replacing or removing a multiline value can
  discard comments inside it. The receiver needs write access to the
  configuration directory and `repos/` and reports a refusal if it lacks access.
- The service's loader validates the edited file before saving it. The
  setting stays open with the reason if the loader refuses the change.
  You can repair each file independently. The settings index shows where
  the loader stopped if the current configuration fails to load.
- A change takes effect from the next run, since the sweep, each webhook
  and each run read the files afresh. The page says where a setting is read
  only when the receiver starts, or sizes the runs slice and needs
  `minos install-units` again.

**Queue** shows the last completed sweep's own timestamp, pull requests
waiting for a run slot, and the reasons the sweep deferred or could not
read the others. **Live runs** shows what the service can currently read
about active pull requests, stages and dispatched workers.

**Recent runs** uses the same archive listing as `/runs/recent`. The
shipped archive command includes the outcome, elapsed time and available
skip or failure evidence in its timing sidecars. Sidecars written without
those details still appear with the recorded timing span where available.
The operator page and the authenticated recent-runs endpoint both have
access to that evidence.

Open a repository from **Overview** to see its inline finding responses
per pull request, including closed and merged requests. The page reads the
forge on demand and shows when a pull request's thread state is
unavailable.

Without the key the receiver serves no page, and its root answers 404.

## Serving run status to an operator surface

The receiver can serve a read-only projection of what is live: for each active
run, the pull request it serves, the head it is bound to, the lifecycle stage
it has reached, and the timing record `collect-timings` assembles from the
run's own residue. It is presentation only — nothing writes to it and no
decision reads it — and it exists so a dashboard can show a run's progress
without a shell on the box.

The route is mounted only when a token is configured for it, so a deployment
that wants no such surface simply leaves the keys out. To turn it on:

```sh
umask 077 && head -c 32 /dev/urandom | base64 > /etc/minos/status.token
```

then add both keys to `/etc/minos/service.toml` and restart the receiver:

```toml
[listener]
bind = ":8919"
status-token-file = "/etc/minos/status.token"

[runs]
timings-command = "/opt/minos/run-body/collect-timings"
```

`timings-command` is optional; without it each run reports a null timing block
and its stage ladder alone. Read the projection with the token as a bearer
credential:

```sh
curl -H "Authorization: Bearer $(cat /etc/minos/status.token)" \
  http://localhost:8919/status | jq
```

The token is the whole gate, and the projection names repositories, pull
requests and heads — keep it to the LAN and treat it as a credential.

The projection's `sweep` field carries the sweep-deferral document. At the
end of each completed pass the sweep writes `.sweep-deferrals.json` atomically
into the runs directory with a `completed_at` timestamp. The document records:

- **`deferrals`** — pull requests the pass deliberately left unstarted, each
  with a `reason` (the precedence-selected one) and `reasons` (the
  reasons observed on that pass), including admission rules,
  work-in-progress branch prefixes, completed review markers and unresolved
  dependencies. Path eligibility is checked when the metadata rules pass.
- **`suppressed`** — pull requests eligible for a run but blocked by the
  concurrency cap or their own active unit, with the blocking unit and detail.
- **`terminal`** — pull requests whose current head already carries a terminal
  outcome (`clean` or `attention`).
- **`readied`** — pull requests a run was started or continued for this pass.
- **`partial`** — true when one or more repositories could not be listed;
  `skipped_repositories` maps each `forge/owner/repo` to its reason.

Publication is best-effort: a failed write logs the error and leaves the
previous whole document in place. The status endpoint reads the file on each
request and serves it as `sweep`; a deployment with no completed pass yet
returns no `sweep` field.

The projection's `receiver` field carries a heartbeat the receiver publishes
on each webhook delivery and at startup. Each document carries `recorded_at`
(the timestamp of the event) and `activity` (`delivery` or `listening`).
A delivery heartbeat includes `forge` (the forge that sent the event); a
startup heartbeat includes `bind` (the listener address). Publication
replaces the whole document, so only the most recent event's fields are
present. The heartbeat grants no lease and is never read by admission — it
exists so an operator surface can tell whether the receiver is alive and how
recently it processed an event.

To include finished runs, point the same `[runs]` block at the archive listing
script and install it beside the others:

```toml
[runs]
recent-timings-command = "/opt/minos/run-body/list-recent-timings"
```

`GET /runs/recent?hours=24` then reports the timing sidecars `archive-run` has
delivered within the window — the same per-step and per-agent detail a live run
shows, for runs whose directories have already been swept. Without the key the
route answers with no runs rather than failing.

Listing the sidecars means reaching the archive host over SSH, so the route
holds each window's answer in memory and asks again only once it has aged out.
Two minutes is the default; `recent-timings-cache-seconds` in the same `[runs]`
block sets it. A cancelled request leaves the cached listing alone. The document's `generated_at` is when the archive host was last
listed, not when the response was built, so a reader can see how old the answer
is. Runs end minutes to hours apart, so an operator surface can poll this route
as often as it likes without the archive host hearing about it.

The calls that do reach the archive host share a connection where they can.
`archive-transport.sh` puts an SSH control socket in `$XDG_RUNTIME_DIR`, or in
`MINOS_ARCHIVE_CONTROL_DIR` when set, so a run's four delivery calls cost one
login between them and a steady listing caller holds one session open rather
than authenticating on each poll. Neither directory being available costs only
the sharing: each call falls back to its own connection.

## Repository review briefs

A reviewed repository can carry `.review/` briefs — Markdown files that tell
the review what to look for. The discovery rules and frontmatter syntax are
specific to Minos.

### Discovery

Briefs are regular files ending in lowercase `.md` under the `.review/`
directory, discovered recursively in deterministic path order. Symlinks and
files with uppercase extensions (`.MD`, `.Md`) are not discovered. A brief's
containing directory relative to `.review/` becomes its path scope: for
example, `.review/internal/security.md` scopes to `internal/`. A brief at
the root of `.review/` (such as `.review/prose.md`) has no path scope.

### Frontmatter

A brief may open with a frontmatter block delimited by `---` fences:

```markdown
---
relevance: Security-sensitive changes
occasion: audit
---
Review body starts here.
```

The opening `---` must be the first line of the file, followed by
LF-delimited `key: value` pairs and a closing `---`. Lines outside the
fences are body text. The parser is not a general YAML parser; it reads
scalar pairs only. Familiar YAML features — lists, quoted values, CRLF
line endings — produce different or default dispositions rather than an
error. Omitting the fences causes all fields to take their defaults.

Recognised fields:

- `title` — a display title for the brief.
- `relevance` — a prose description of when the brief applies; the review
  uses it as a trigger alongside path scope.
- `occasion` — a comma-separated list of occasion tags (for example
  `occasion: release, security-audit`). When declared, the brief runs only
  on a run that names a matching occasion; the ordinary lifecycle invocation
  supplies no occasion, so an occasion-restricted brief is skipped by default.
- `extent` — `diff` (the default) or `full`. A full-extent brief grounds the
  review on all tracked files within the brief's directory scope, not just
  the changed files; it does not escape the scope to the whole tree.
- `sweep` — `per-file` (the default) or `whole-tree`. It matters only for a
  full-extent brief. With `per-file`, a planning leg divides the files in
  the brief's scope into review units and each unit gets its own reviewer;
  with `whole-tree`, one reviewer reads the whole scope. `extent` decides
  what is read, `sweep` how many reviewers share it. Choose `whole-tree`
  when the brief's concern can only be judged across the scope at once.

### Engagement rules

A root brief with no `relevance`, no `occasion`, and no path scope is
deterministically skipped — it has no trigger to engage on. Add at least one
of those to make it run.

A brief engages when any of its triggers is satisfied: changed files within
its path scope, a matched `occasion`, or a `relevance` the change matches.
Declaring `relevance` is not enough on its own: when neither of the other
triggers fires, a planning leg judges whether the change meets the
relevance description, and the brief is skipped when it does not. A scoped
brief whose scope is untouched still engages when its relevance or occasion
matches — path scope limits the files the review reads, not whether the
brief runs at all.

When a brief declares `occasion`, the occasion acts as a veto: a brief that
names occasions runs only when the run's own occasion matches one of them;
a brief naming no occasion is unrestricted by occasion. A matched occasion
is itself a satisfied trigger.

A brief whose path scope names a directory that does not exist in the
workspace is reported as a misconfiguration rather than silently skipped.

### Guidance sources

The review is grounded on the guidance sources configured per repository:
a `[[guidance-sources]]` array in the repository's TOML file, each entry
naming a `path` and an optional `repository` (owner/name on the same forge).
When no sources are configured, the fallback reads the first non-empty file
from `AGENTS.md`, `CLAUDE.md`, `README.md` in the reviewed repository — the
first hit wins and later candidates are not combined. Each guidance document
must be at most 256 KiB (262,144 bytes): a larger one passes setup but stops
the review workflows' input from being built, so the run cannot review. A
configured source in a
secondary repository is cloned read-only beside the workspace; the clone's
push URL is disabled. A configured source that is missing or empty is recorded
as a misconfiguration and reported to the lead, never silently skipped; when
every configured source fails, setup stops the run.

## Filing deduplication

Each filed entry — whether to a file, an issue, or a pull-request comment —
carries a hidden HTML comment marker (`<!-- minos:… -->`). The marker's
identity depends on the entry kind: a review finding keys on kind, reviewed
repository, path, line, and title; a review-brief misconfiguration keys on
kind, reviewed repository, brief path, and title; a guidance-source
misconfiguration keys on kind, reviewed repository, source repository, path,
and reason — so changing a misconfiguration's reason changes its marker,
while changing its title does not. Deduplication checks the destination for
an existing marker before filing. Removing these markers during editing
removes the deduplication history: subsequent reviews can then file the same
finding again.
