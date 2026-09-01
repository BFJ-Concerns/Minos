# Deployment

Minos runs on a disposable single-tenant machine with Claude Code, the Codex
CLI, Node.js, Git, Go, curl and jq installed. The machine itself is the
containment boundary. Reviews never build or test the reviewed repository,
so no repository toolchains are needed.

1. Build and install `cmd/minos` as `/usr/local/bin/minos`.
2. Install `scripts/adaptations/forgejo` under
   `/opt/minos/adaptations/forgejo`; install the whole of `scripts/run-body/`
   under `/opt/minos/run-body`, each file keeping its basename and mode. Copy
   the directory rather than a named list: the scripts source their shared
   pieces — `cgroup-memory.sh`, `archive-transport.sh` — from beside
   themselves, and a run-body missing one fails at the step that needed it.
   Install `scripts/provision-archive-transport` and
   `scripts/provision-failure-checkout` under `/opt/minos`, with executable
   mode. Install `lifecycle`
   under `/opt/minos/lifecycle`, and `skills/foundry` under
   `/opt/minos/skills/foundry`. That tree holds the vendored skills one
   directory per agent tool — `codex/` and `claude-code/` — so a spawned
   session is handed the copy cast for the tool it runs on.
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
   and add one repository TOML file per opted-in repository. In `run-body.env`:

   - set `MINOS_CLAUDE` to the installed Claude executable;
   - keep `MINOS_LEAD_MODEL` pinned to `claude-opus-5`;
   - set `MINOS_CLAUDE_CONFIG_SEED` to a directory containing known-good,
     non-interactive Claude configuration;
   - set `MINOS_CODEX_CONFIG_SEED` to a directory containing known-good,
     non-interactive Codex ChatGPT authentication state; and
   - check that `MINOS_LIFECYCLE_INSTRUCTION`, `MINOS_REVIEW_WORKFLOW`,
   `MINOS_SKILLS_DIR`, `MINOS_ARCHIVE_RUN` and the
   other installed paths match the deployment. Configure `archive.env` with the archive SSH host,
   destination, identity and pinned known-hosts file.

   `runs.max-concurrent` caps how many run units may be live at once, and
   defaults to one when unset. Run units share a fixed 22 GiB whole-box
   memory envelope live, through the `minos-runs.slice` unit: the slice
   holds `MemoryHigh=20G` and `MemoryMax=22G`, so no run feels any pressure
   until the runs *together* approach the envelope — a lone run may use all
   of it — reclaim then pushes them back, and only combined demand the
   envelope cannot hold kills, taking the biggest consumer. Each unit also
   carries its own `MemoryMax` at the whole envelope as the backstop for a
   box missing the slice unit. Size the cap against the machine's memory
   and cores: a healthy run's unreclaimable footprint is around 1.2 GiB,
   but each run also paces `ensemble.concurrency-claude` and
   `concurrency-codex` workers of its own, and `ensemble.agent-ceiling`
   optionally caps a workflow's agents across both engines together.

   Size every capacity decision — this cap, the slice envelope, the box
   itself — by **anonymous memory plus swap peak, never the journal's cgroup
   memory peak**. The journal's figure includes reclaimable page cache and
   pegs at `MemoryMax`, so it overstates the real footprint by an order of
   magnitude. Raising the cap on the journal's cache-inflated figure has
   already thrashed the box into mass continuations once (2026-08-16,
   reverted the same day). Measure anonymous demand from the unit cgroup's
   `memory.stat` (`anon`) plus `memory.swap.current` at peak — the same
   metric the run's pressure watch reads. Review-only runs build nothing,
   so real demand sits far below the old build peaks.

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

   Provision both seed directories when the disposable box is launched. Each
   run copies their contents into its private `HOME`: Claude state goes to
   `$HOME/.claude` and Codex state to `$HOME/.codex`. `run-body` then copies
   the vendored skill tree named by `MINOS_SKILLS_DIR` into both homes —
   `claude-code/` skills to `$HOME/.claude/skills` and `codex/` skills to
   `$HOME/.codex/skills` — so every spawned session finds the casting made for
   its tool. Each run also gets a private disk-backed `TMPDIR` beneath the
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

   Create the webhook secret and forge token files separately.
5. Install `minos-sweep-alert.service` (the sweep's `OnFailure=` hook, which
   files an operator alert issue on the repository named by the `[service]`
   `alert-forge`/`alert-owner`/`alert-repo` keys; the sweep files the same
   alert itself when a configured repo goes unswept for an hour), then
   install and enable `minos-receiver.service` and `minos-sweep.timer` from
   `deploy/systemd/user` for the deployment user, and install
   `minos-runs.slice` alongside them so runs share the memory envelope.
6. Configure the forge webhook to post to `/hooks/forgejo` using the matching
   secret.

Each run launches the `claude-opus-5` lead through `claude --bg`. `run-body`
keeps resumable `done` and `blocked` turns alive after useful run activity has
begun. A lead blocked before its first run-tree change is stopped and recorded
as a supervision failure. The supervisor otherwise stops the session only
after Claude reports `failed` or `stopped`, the lead writes its clean terminal
marker, or the run tree outside its private home has not changed for the
silence timeout. The silence backstop records a supervision failure instead of
treating that stop as success. The lead invokes review and repository-brief
workflows through `/opt/minos/workflows/adjudicated-review`; the wrapper runs
the selected script with `/opt/minos/runtime/ensemble.mjs` and asks the sibling
run-record adapter to confirm every required leg ran to completion and every
finding carries a verdict. The foreground run body waits for the exact
background session and stops it at one of those terminal conditions. The
transient systemd unit bounds a wedged run at 12 hours.

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
the sweep also salvages dead run directories into the dedicated Minos annexe
checkout configured by `runs.failures-repo`, attempts to commit and push the
digest, archives their transcripts, and removes the scratch tree. Active units
and directories named by valid pending continuation handoffs are preserved.
Each digest is limited to 256 KiB and reads only the run-local report and
failure log, Claude transcripts, Codex rollouts and exact Ensemble record tree.
The sweep fetches and rebases its append-only commit onto `origin/main` before
pushing with the configured Forgejo token header.

The receiver handles new events immediately. The sweep periodically starts any
open, non-draft pull request whose current head carries no completion marker.
Before reconciling a pull request, the sweep strips any stale Minos approval
reaction (👍) left on a head the author has since replaced: reviews are
SHA-bound evidence, but the PR-wide reaction is not, so it is removed before
another reconciliation decision is made.

## Reading run liveness

The obvious liveness signals invert during a correct yield, so a *correctly
behaving* lead is the case most likely to be misread as dead. While the lead
waits on a background workflow it has ended its turn: the run tree's scratch
mtimes go stale, no lead processes are doing visible work, and the journal
prints nothing — exactly the picture a dead run would show. Do not judge
liveness from scratch mtime, process activity, or journal silence.

The reliable discriminator is the lead session's own job state plus the unit:
in the run's private home, the job directory's `state.json`
(`$MINOS_RUN_DIR/home/.claude/jobs/<job>/state.json`) shows whether a wake is
armed — a lead waiting on `session_cron` with `selfWake: true` is armed and
healthy, and `inFlight` names any background task still owned — and the
per-pull-request systemd unit must be `active`. An armed waiting lead with an
active unit is a live run, however stale its scratch looks; a unit that has
exited, or a job state with nothing in flight and no armed wake and no
terminal marker, is the dead case. The run supervisor's own silence backstop
(`MINOS_LEAD_SILENCE_TIMEOUT`) already bounds a genuinely wedged lead — do
not kill a waiting run ahead of it on the strength of quiet files.

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
  http://minos.example:8919/status | jq
```

The token is the whole gate, and the projection names repositories, branches
and heads — keep it to the LAN and treat it as a credential.

The projection's `sweep` field carries the sweep-deferral document. At the
end of each completed pass the sweep attempts to write
`.sweep-deferrals.json` atomically into the runs directory, recording every
pull request that was deliberately deferred and its reason — work-in-progress
branch prefix, completed review marker, or unresolved dependency — with a
`completed_at` timestamp. Publication is best-effort: a failed write logs
the error and leaves the previous whole document (or none, if no earlier pass
succeeded) in place, so the served record can lag the latest pass. The status
endpoint reads this file on each request and serves it as `sweep`. A
deployment with no completed pass yet returns no `sweep` field.

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
`MINOS_ARCHIVE_CONTROL_DIR` when set, so a run's five delivery calls cost one
login between them and a steady listing caller holds one session open rather
than authenticating on each poll. Neither directory being available costs only
the sharing: each call falls back to its own connection.
