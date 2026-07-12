# Minos

Agent-native code verification. The first deliverable is an automated,
independent PR review-and-fix service — see `AGENTS.md` for the corrected
architecture stance.

The previous implementation was removed under a containment episode (see
`CONTAINMENT.md`), and this rebuild works from the corrected commission in the
sibling annexe. Quarantined code remains readable in git history as evidence,
not as a base.

## Service Shell

The rebuilt shell is deliberately small:

- `minos receive --config /etc/minos` listens for forge webhooks at
  `/hooks/{forge}` and reconciles the affected PR from current facts.
- `minos sweep --config /etc/minos` is the cron-runnable reconciliation pass.
- `minos run-wrap --config /etc/minos` renews the lifecycle lease heartbeat,
  prepares the workspace, executes the accountable lead, and releases the lease
  and transient execution resources on exit. Attempt evidence remains under
  `runs.dir`.
- `scripts/run-body/run-body` launches one fresh accountable lead session from
  the pinned lifecycle instruction. Infrastructure does not select review,
  repair, or finish stages.
- `minos run-guard` exposes fenced ownership, current-head checks, owner-followed
  head/target advancement, integration clearance, waiting, and presence release.
- `minos adapt` invokes a configured, service-owned forge adaptation with the
  dedicated forge credential. Lifecycle mutations are rejected unless their
  attempt token still owns the current lease and observed head/target pair.
- `minos review`, `minos marker`, `minos handle`, and `minos provenance`
  expose changed-line and anchor checks, stable finding identities, marker
  formatting, and resolved-model pin verification to the session.
- `minos capture-claude` records Claude's JSONL audit stream and validates the
  engine-reported lead model before the session may publish.
- `minos ws-exec --config /etc/minos -- command ...` runs PR-controlled build
  or test commands inside `MINOS_WORKSPACE` with configured service credentials
  scrubbed from the environment.

Webhook receiver and sweep both call the same pure reconciliation decision in
`internal/reconcile`. Deployment-local coordination lives in one WAL-mode
SQLite database under the configured runs directory. The forge remains the
durable product record.

## Coordination ledger

The ledger contains only coordination facts:

- one fenced repository/PR lease with observed head and target revisions;
- a monotonic attempt token and explicit heartbeat;
- PR-keyed wait fingerprints and operational backoff which may outlive a lease;
- attempt-local integration clearance;
- incident deduplication identity; and
- guarded post-merge branch-cleanup obligations.

Admission capacity is the number of lease rows. Duplicate deliveries race at
one `BEGIN IMMEDIATE` acquisition transaction; only the winner launches. A dead
owner is replaced only after its transient systemd unit is stopped and verified
inactive. Migration or integrity failure refuses receiver, sweep, and launch
work rather than replacing the database.

Minos does not read or write PR labels as control state. Its product surface is
one `Minos` status on the current head plus an ordinary service-authored review.
The status descriptions are the product vocabulary defined by the commission,
such as `Reviewing changes`, `Waiting for checks`, `Changes need attention`,
and `Changes approved`.

Machine-readable marker lines are trailing prose footers with this grammar:

```text
Minos: key=value another-key=value
```

Values contain no spaces. Unknown keys are ignored by consumers.

## Run-Body Contract

Every spawned run receives:

```text
MINOS_RUN_DIR        run directory containing run.log, meta.env, diff.patch
MINOS_OCCASION       triggering occasion, or reconcile
MINOS_FORGE          configured forge name
MINOS_REPO           owner/name
MINOS_OWNER          repository owner
MINOS_REPO_NAME      bare repository name
MINOS_PR             pull request number
MINOS_HEAD_SHA       full head SHA the run serves
MINOS_TARGET_SHA     target revision observed for the lease
MINOS_ATTEMPT_TOKEN  monotonic fenced ownership token
MINOS_BASE_REF       base branch
MINOS_WORKSPACE      prepared clone-shaped checkout
MINOS_DIFF           PR diff path
MINOS_ADAPTATION     forge adaptation scripts directory
MINOS_SKILL          run skill/prompt file path
MINOS_RUN_BODY       run-body executable path; empty means minos stub-run
MINOS_BRIEFS         brief directory
MINOS_CONFIG         configuration root
MINOS_UNIT           transient systemd unit name
```

The run directory is attempt-local evidence, not the ownership claim. Ownership
is the exact lease-token equality in SQLite. `run-wrap` removes the temporary
workspace and releases the lease on success, preparation failure, body failure,
or panic. Workspace-preparation and lead launch or execution failures record an
indefinite 15-minute-to-six-hour backoff and remain off the PR surface.

## End-To-End Harness

`go test ./...` includes real file-backed WAL races, old-token replay rejection,
wait and backoff currency, dead-session replacement ordering, all wrapper exit
paths, and a receiver-to-launch-to-release coordination journey.

The existing Docker `make e2e` harness still describes the retired stage loop
and is not a release gate for this transitional checkout. Its replacement needs
the parallel lifecycle instruction and behavioural Forgejo adapter; until that
wave-two integration lands, use `make check` and the Go coordination tests.

## Deployment

The repository carries production-shaped configuration templates, systemd user
units, and the ordered operator procedure in [`docs/go-live.md`](docs/go-live.md).
`scripts/e2e/deployment-smoke.sh` rehearses the receiver, an authenticated
synthetic delivery, the sweep, and unit-file validation without contacting a
real forge.
