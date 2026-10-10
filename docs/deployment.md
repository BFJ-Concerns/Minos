# Deployment

Minos runs on a disposable single-tenant Linux machine under a functioning
systemd user manager (systemd 250 or later). The machine needs Claude Code, the Codex CLI, Node.js,
Git, Go, Bash, curl, jq, openssl, OpenSSH client utilities, ssh-keygen, tar,
zstd, sha256sum and GNU find installed (openssl signs the GitHub App token;
`scripts/expected-tool-versions` is the declared set for the versioned tools).
The default routing uses both `claude-opus-5-5` and `gpt-6-sol`: a Claude Code
subscription or gateway credential for the Claude engine, and a Codex
(ChatGPT) account for the Codex engine. A box with only one engine provisioned
runs every role on it. The machine itself is the containment
boundary. Reviews never build or test the reviewed repository, so no repository
toolchains are needed.

1. Build and install `cmd/minos` as `/usr/local/bin/minos`.
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
   anything it checks the box's tool versions against
   `scripts/expected-tool-versions` — the set the repository gate runs
   against — and stops on any mismatch, naming each drifted tool with both
   versions; upgrade the box's tools (or record a deliberate divergence as
   `any` in that file) and re-run. It then installs the complete test-free
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

   The `.env` files (`run-body.env`, `archive.env`) are sourced as shell
   programs with `set -a` (automatic export), not parsed as generic dotenv
   files. Shell expansion and commands execute during loading; values must
   obey shell quoting rules.

   In `run-body.env`:

   - set `MINOS_CLAUDE` to the absolute path of the installed Claude
     executable — install Claude Code natively, not as an npm package,
     whose shim a lead can migrate away from mid-run. The lead uses this
     path directly; Ensemble workers resolve `claude` and `codex` by
     command name through the inherited PATH (the `environment.d` file
     from step 2), so both the absolute variable and PATH must be correct;
   - set `MINOS_LEAD_ENGINE` to `claude` or `codex` (`claude` by default)
     and `MINOS_LEAD_MODEL` to the model the lead runs on — `claude-opus-5-5`
     by default for Claude, `gpt-6-sol` for Codex; a Codex lead needs
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

   Per-repository knobs — the review threshold, work-in-progress branch
   prefixes, the guidance sources a review is grounded on, the filing
   destination for a clean run's advisory material and configuration diagnostics,
   and the markers Minos writes on a pull request — are set once under
   `[repositories]` in `service.toml` and overridden whole by any
   repository's own file; the shipped `service.toml` documents each knob
   and the defaults a repository gets when neither level sets it. A
   guidance source in a secondary repository is cloned read-only beside
   the workspace at setup.

   The filing destination controls where confirmed advisory findings and
   configuration diagnostics — brief misconfigurations and unreadable guidance
   sources — are delivered: `kind` is one of `file`
   (path required; repository optional, defaulting to the reviewed
   repository's default branch), `issue` (repository required — the
   owner/name that takes the issues; no path), `pull-request-comment`, or
   `none`. Unset at both levels, it defaults to `pull-request-comment`.
   The `pull-request-comment` kind deduplicates against the service
   account's existing review bodies on the pull request, then posts one
   comment-review with the remaining advisory entries; when none remain,
   nothing is posted — there is no all-clear comment. A
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
   label must already be defined on the repository or its organisation;
   Minos never creates labels, and refuses the marker write when the
   label is missing. A repository's own `[markers]` table replaces the
   service-level one whole.

   Every commit status Minos writes carries `service.status-context`
   (default `Minos`), and the sweep's completion marker is read under
   that context only. A status's Details link lands on the review Minos
   posted for the reviewed head, or on the pull request itself when it
   posted none. For a Forgejo forge, `[forges.<key>] web-base`
   sets the link's host and optional path prefix as an absolute HTTP(S)
   URL; unset, the link derives the web host from `api-base` by stripping
   a trailing `/api/v1`. GitHub links always derive the web host from the
   API base (`https://github.com` for the public API, the Enterprise host
   otherwise) and ignore `web-base`.

   The status is review information, not a merge gate: do not make this
   context a required status check in branch protection. It can be missing
   or stale on a head. A run that fails before the lead launches writes no
   status, a status write the forge does not confirm leaves the previous
   status in place, and a lead that dies can leave `pending` behind until a
   later run replaces it.

   `runs.max-concurrent` caps how many run units may be live at once, and
   defaults to one when unset; it must not exceed `runs.memory-envelope-gib`.
   That service-only knob defaults to 22 GiB. Run units share the configured
   whole-box memory envelope live, through the `minos-runs.slice` unit.
   The shipped slice holds `MemoryHigh=20G` and `MemoryMax=22G`, so no run feels any pressure
   until the runs *together* approach the envelope — a lone run may use all
   of it — reclaim then pushes them back, and only combined demand the
   envelope cannot hold kills, taking the biggest consumer. Each unit also
   carries its own `MemoryMax` at the whole envelope as the backstop for a
   box missing the slice unit. Size the cap against the machine's memory
   and cores. `minos install-units` (step 5) renders the installed slice
   from this knob — `MemoryMax` at the envelope, `MemoryHigh` two GiB below
   it — so after changing the envelope re-run it and reload the user
   manager. `runs.duration-ceiling` sets each run's hard
   duration limit (a Go duration of at least `1us`, default `12h`).
   `runs.pressure-threshold-percent` sets the continuation threshold as a
   percentage of the run ceiling (1–100, default 85); the watch signals after
   two consecutive samples of anonymous memory plus swap at that threshold.
   A healthy run's unreclaimable footprint is around 1.2 GiB,
   but each run also paces `ensemble.concurrency-claude` and
   `concurrency-codex` workers of its own (the loader defaults both to 2
   when neither is set; the shipped example comments both at 2).
   `ensemble.agent-ceiling` optionally caps a workflow's agents across
   both engines together; unset, the runtime applies no combined cap (the
   shipped example comments an illustrative value of 4).

   `[routing]` maps each workflow role — `exploration`, `proposer`,
   `verifier`, `engagement-gate`, `brief-planner` — to an engine, model
   and effort. A role left unset defaults at run time: with both engines
   provisioned, proposing and planning roles (`exploration`, `proposer`,
   `engagement-gate`, `brief-planner`) run on Codex (`gpt-6-sol`) and the
   `verifier` on Claude (`claude-opus-5-5`); with one engine provisioned,
   every role runs on it with that engine's default model. Default effort
   is `high` for `exploration`, `proposer` and `brief-planner`, and
   `medium` for `verifier` and `engagement-gate`. A configured role
   overrides only the fields it sets.

   Size every capacity decision — this cap, the slice envelope, the box
   itself — by **anonymous memory plus swap peak, never the journal's cgroup
   memory peak**. The journal's figure includes reclaimable page cache and
   pegs at `MemoryMax`, so it overstates the real footprint by an order of
   magnitude. Measure anonymous demand from the unit cgroup's
   `memory.stat` (`anon`) plus `memory.swap.current` at peak — the same
   metric the run's pressure watch reads.

   `MINOS_LEAD_SILENCE_TIMEOUT` optionally overrides the supervisor's
   3600-second no-output backstop. Keep the lifecycle's fallback wake shorter
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
   disposable box is launched — the lead's engine at least. Each run copies
   their contents into its private `HOME`: Claude state goes to
   `$HOME/.claude` and Codex state to `$HOME/.codex`; no skills are
   installed into either home — the lead needs none, and review workers run
   with their skill surfaces stripped. After copying, `run-body` overwrites
   `$HOME/.claude/.claude.json` with an onboarding-acceptance stub
   (`hasCompletedOnboarding` and `bypassPermissionsModeAccepted`), so any
   seed content in that file is not preserved — put seed configuration in
   other files within the seed directory. Each run also gets a private disk-backed `TMPDIR` beneath the
   storage root's `tmp/` directory, alongside `runs/` (the shared tmpfs `/tmp`
   cannot hold concurrent runs' artefacts). The lead and Ensemble workers
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
   (reactions, labels, dependency lookups, and alert issues). The account
   named by
   `service.bot-login` must match the token's authenticated identity; the
   claim adapter rejects a mismatch. The Forgejo instance must expose the
   issue-dependencies API (`/api/v1/repos/.../issues/.../dependencies`); a
   missing or disabled endpoint produces "dependency state unavailable" and
   defers review until the next sweep pass.

   For a GitHub forge, register a GitHub App (permissions: pull
   requests and commit statuses read and write, contents read — and write
   where a `file` filing destination or the failure ledger pushes to an
   installed repository — and issues read and write where a filing
   destination or alert needs them; subscribe it
   to pull request, pull request review and issue comment events; one
   webhook pointing at `/hooks/github` with the secret), install it on each
   repository Minos reviews, and write the credential file as JSON:
   `{"app-id": "<App id>", "installation-id": "<installation id>",
   "private-key-file": "/etc/minos/github-app.pem"}` beside the downloaded
   private key. The adaptation mints an installation token from it for each
   invocation and caches the token until it nears expiry; the box needs
   `openssl` for the App JWT. The App's login is its slug with the `[bot]`
   suffix (`minos-review[bot]`) — the login GitHub shows on every write the
   App makes, and the one the guards compare against. Set it as
   `service.bot-login` when GitHub is the only forge; when another forge's
   account holds `service.bot-login`, set it as `bot-login` in
   `[forges.<key>]` instead, which overrides the service's login on that
   forge alone. A GitHub App cannot be a requested reviewer, so the claim on
   GitHub is the in-flight marker alone.
5. Install `minos-sweep-alert.service` (the sweep's `OnFailure=` hook, which
   files an operator alert issue on the repository named by the `[service]`
   `alert-forge`/`alert-owner`/`alert-repo` keys; the sweep files the same
   alert itself when a configured repo is skipped for six consecutive
   sweep passes — roughly an hour at the default ten-minute cadence, but
   pass-count-based, not time-based): run
   `minos install-units --config /etc/minos ~/.config/systemd/user` as the
   deployment user, then `systemctl --user daemon-reload` and enable
   `minos-receiver.service` and `minos-sweep.timer`. The units are embedded
   in the binary (the sources are `deploy/systemd/user`); the command
   writes every service whose Exec line runs the binary with the
   configuration root it was given, `minos-runs.slice` with the configured
   memory envelope, so runs share that envelope rather than a copied
   default, and the timer and the `minos-sweep-after-run@.service` template
   verbatim.
6. Configure the forge webhook to post to `/hooks/<forge key>` —
   `/hooks/forgejo` for the shipped Forgejo table, `/hooks/github` for a
   GitHub App — using the matching secret. The path suffix after `/hooks/`
   must match the key used in `[forges.<key>]` in `service.toml`; a
   mismatch returns "unknown forge".

Run units, run directories and continuation handoffs include the configured
forge key in their names (for example `minos-run-forgejo-owner-repo-pr1`), so
two forges reviewing the same owner/repository namespace cannot collide. Deploy
with no live run units and no preserved handoff directories carrying the
older names without the forge key.

A run that exits cleanly triggers one sweep pass, so the freed slot is filled
without waiting for the next periodic cycle: each run unit carries
`OnSuccess=minos-sweep-after-run@minos-run-<forge>-<owner>-<repo>-pr<n>.service`,
and that instance enqueues `minos-sweep.service` and
exits. The indirection is what lets the user manager collect the exited run
unit — a unit stays loaded while an exit hook it fired names a unit that
stays loaded, so a hook on the sweep service itself kept every exited run
unit loaded and refused the pull request's next run as already loaded — and
the template must be installed (step 5) on any box that spawns runs. A failed
exit does not trigger a sweep; an operator's `systemctl --user stop` of a run
unit ends it cleanly and does. The unit's exit status says whether the head is
spent: run-body exits successfully after a `clean` marker (the lead writes it
only once the clean status landed), after a continuation, or when the last
status the run applied — `minos forge status` records it in the run
directory — is `clean` or `attention`. An incomplete outcome, or a lead that
failed or stopped with neither status applied, exits 75 and triggers no pass
— that head waits for the timer, so one that keeps ending incomplete re-runs
at timer pace, never at run pace. A clean exit that
lands while a pass is already running joins that pass rather than starting
another, so a pull request the running pass has already walked waits for the
timer.

Each run launches the lead on the configured engine and model. `run-body`
keeps resumable `done` and `blocked` turns alive after useful run activity has
begun. A lead blocked before its first run-tree change is stopped and recorded
as a supervision failure. The supervisor otherwise stops the session only
after the lead reports `failed` or `stopped`, the lead writes a terminal marker
(`clean`, `non-clean`, or `continuation`), or the run tree outside its private
home has not changed for the silence timeout. The silence backstop records a supervision failure instead of
treating that stop as success. The lead invokes review and repository-brief
workflows through `/opt/minos/workflows/adjudicated-review`; the wrapper runs
the selected script with `/opt/minos/runtime/ensemble.mjs` and asks the sibling
run-record adapter to confirm every required leg ran to completion and every
finding carries a verdict. The foreground run body waits for the exact
background session and stops it at one of those terminal conditions. The
transient systemd unit bounds a wedged run at `runs.duration-ceiling`.

A run that fails before the lead launches — workspace setup, configuration
loading, lifecycle instruction — writes no status to the forge. The run body
attempts to append the failure to `runs.failure-log`; when the log path is
unset or unwritable, stderr carries the evidence instead. The run exits after
recording the failure. When workspace
setup cannot fetch the admitted target SHA because the forge no longer
advertises it (typically because the base branch moved), the failure cause
carries that refusal; the next sweep pass re-derives from current forge state.
Workspace setup resolves clone URLs and credentials through the forge-neutral
`minos forge repository-metadata` and `minos forge clone-credential` operations
rather than reading the forge API directly; each adaptation implements both,
and the credential reaches git only as a `GIT_CONFIG_*` authorisation header,
never on argv.

After the lead has finished its forge writes, `run-body` streams its report,
Claude transcripts, Codex rollout JSONLs and Ensemble run records as a zstd tar
archive to the configured destination. Before building the tarball it runs
`collect-timings`, which assembles the structured per-run timing record —
every timed command span from the run's `timings.ndjson` event log plus every
dispatched worker's timings from the Ensemble records — and after the tarball
is promoted it delivers that record a second time as a sidecar beside it —
the tarball's name with `.tar.zst` replaced by `.timings.json` — readable
without extracting anything. This presentation archive and its sidecar are
best-effort and cannot change the run result. Before each reconciliation pass,
the sweep also salvages dead run directories into the dedicated checkout
configured by `runs.failures-repo`, attempts to commit and push the
digest, archives their transcripts, and removes the scratch tree. The
checkout must be outside `runs.dir` (the service rejects one inside it)
and must already contain a regular `FAILURES.md` — the provisioner
(`provision-failure-checkout`) clones and configures git but does not
create that file; an empty repository or one without it stops the salvage.
Residue from a failed salvage is preserved in the run directory.
`runs.failures-forge` selects the forge whose credential authenticates
the ledger push; it defaults to the sole configured forge and is required
when several forges are configured. Active units
and directories named by valid pending continuation handoffs are preserved.
Each digest is limited to 256 KiB and reads only the run-local report and
failure log, Claude transcripts, Codex rollouts and exact Ensemble record tree.
The sweep fetches and rebases its append-only commit onto `origin/main` before
pushing. The push authenticates through the selected forge adaptation's
`clone-credential` operation using `runs.failures-credential-file` — a
separate credential from the reviewed-forge file — and reaches git as a
Basic authorisation header via `GIT_CONFIG_*`, never on argv.

The receiver handles new events immediately. The sweep periodically starts any
open, non-draft pull request whose current head carries no completion marker.
Before reconciling a pull request, the sweep strips any stale Minos clean or
attention marker — a reaction or a label, in the repository's configured
form — left on a head the author has since replaced: reviews and statuses are
SHA-bound evidence, but the PR-wide marker is not, so it is removed before
another reconciliation decision is made. A label marker must already be
defined on the repository or its organisation; a missing one refuses the
marker write with a reason naming the label and repository.

## Reading run liveness

The obvious liveness signals invert during a correct yield, so a *correctly
behaving* lead is the case most likely to be misread as dead. While the lead
waits on a background workflow it has ended its turn: the run tree's scratch
mtimes go stale, no lead processes are doing visible work, and the journal
prints nothing — exactly the picture a dead run would show. Do not judge
liveness from scratch mtime, process activity, or journal silence.

For a **Claude lead**, the reliable discriminator is the lead session's own
job state plus the unit: in the run's private home, the job directory's
`state.json` (`$MINOS_RUN_DIR/home/.claude/jobs/<job>/state.json`) shows
whether a wake is armed — a lead waiting on `session_cron` with
`selfWake: true` is armed and healthy, and `inFlight` names any background
task still owned — and the per-pull-request systemd unit must be `active`. An
armed waiting lead with an active unit is a live run, however stale its
scratch looks; a unit that has exited, or a job state with nothing in flight
and no armed wake and no terminal marker, is the dead case.

For a **Codex lead**, the run body observes process liveness (`kill -0`) and
exit status: a Codex lead that is still running is live; one that has exited
with status 0 is stopped (success), and any other exit is failed. There is no
job-file state to inspect.

The run supervisor's own silence backstop (`MINOS_LEAD_SILENCE_TIMEOUT`)
already bounds a genuinely wedged lead on either engine — do not kill a
waiting run ahead of it on the strength of quiet files.

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

The token is the whole gate, and the projection names repositories, branches
and heads — keep it to the LAN and treat it as a credential.

The projection's `sweep` field carries the sweep-deferral document. At the
end of each completed pass the sweep writes `.sweep-deferrals.json` atomically
into the runs directory with a `completed_at` timestamp. The document records:

- **`deferrals`** — pull requests the pass deliberately left unstarted, each
  with a `reason` (the precedence-selected one) and `reasons` (every
  applicable reason from that snapshot): draft, work-in-progress branch prefix,
  completed review marker, or unresolved dependency.
- **`suppressed`** — pull requests eligible for a run but blocked by the
  concurrency cap or their own active unit, with the blocking unit and detail.
- **`terminal`** — pull requests whose current head already carries a terminal
  outcome (`clean` or `attention`).
- **`readied`** — pull requests a run was started or continued for this pass.
- **`partial`** — true when one or more repositories could not be listed;
  `skipped_repositories` names each and its reason.

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
block sets it. The document's `generated_at` is when the archive host was last
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
- `sweep` — `per-file` (the default) or `whole-tree`.

### Engagement rules

A root brief with no `relevance`, no `occasion`, and no path scope is
deterministically skipped — it has no trigger to engage on. Add at least one
of those to make it run.

A brief engages when any of its triggers is satisfied: changed files within
its path scope, a declared `relevance`, or a matched `occasion`. A scoped
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
first hit wins and later candidates are not combined. A configured source in a
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
