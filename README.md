# Minos

Agent-native code verification. The first deliverable is an automated,
independent PR review-and-fix service — see `AGENTS.md` for the corrected
architecture stance.

This rebuild follows the current commission in the sibling annexe. Superseded
designs remain readable in the annexe's containment archive and git history as
evidence, not as architectural authority.

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
- `minos run-guard` exposes fenced ownership, current-head checks, integration
  clearance, wait state, post-compaction revalidation, and presence release.
- `minos forge` exposes authenticated snapshots and typed, guarded status,
  review, push, merge, and cleanup operations. Lifecycle mutations are rejected
  unless their attempt token owns the current lease and observed head/target.
- `minos review`, `minos marker`, `minos handle`, and `minos provenance`
  expose changed-line and anchor checks, the product package's stable finding
  identities and hidden records, and resolved-model pin verification.
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

Machine-readable lineage and re-entry records are hidden trailing lines with
this grammar:

```text
<!-- Minos: key=value another-key=value -->
```

Keys use lower-case letters, digits, and hyphens. Values are restricted to
letters, digits, `.`, `_`, `/`, `@`, and `-`. Unknown keys are ignored by
consumers.

Each service review also carries one complete base64url disposition index
immediately before that trailing product record. It names every verified
occurrence, lineage, priority, assurance, publication, repair, and delivery
state. Destination receipts appear only after authenticated read-back. The
index is a discovery aid and never grants clearance or proves delivery by
itself.

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
MINOS_BUILD_CMD      trusted repository build command
MINOS_TEST_CMD       trusted repository test command
MINOS_AUTO_MERGE     repository auto-merge policy
MINOS_GOVERNING_IDENTITY immutable trusted-input identity for this attempt
MINOS_DEPLOYMENT_PROFILE deployment/readiness identity for re-entry
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

`make e2e` runs the current disposable-Forgejo lifecycle harness. Its normal
mode proves receiver → reconciliation → detached lead launch → guarded product
and cleanup wiring with deterministic engine fixtures. Use
`MINOS_E2E_LIVE=clean make e2e` or `MINOS_E2E_LIVE=stopped make e2e` for the
small real-engine representative journeys. The harness prints its retained
temporary directory; each attempt directory below `runs/attempts/` contains
measured token, artefact, and compaction evidence.

## Deployment

The repository carries production-shaped configuration templates, systemd user
units, and the ordered operator procedure in [`docs/go-live.md`](docs/go-live.md).
`scripts/e2e/deployment-smoke.sh` rehearses the receiver, an authenticated
synthetic delivery, the sweep, and unit-file validation without contacting a
real forge.

Destination-mode repositories use the strict discover/read/create adaptation
contract in [`docs/finding-destination.md`](docs/finding-destination.md). A
quiet finding is complete only after authenticated full-record read-back; the
forge index and local attempt files are discovery aids, not delivery proof.
