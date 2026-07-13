# Go Live with Minos

This runbook takes a bare systemd-based deployment box to a listening Minos
receiver and periodic reconciliation sweep. It runs as the deployment
account on a disposable box: the box is the container. Do not add a nested container runtime.

The commands do not activate the quarantined pre-2026-07-08 deployment. They
install the current repository contents afresh.

## Prerequisites

Prepare these before starting:

- a checkout of this repository at the approved release commit;
- root access on the deployment box;
- `go`, `git`, `curl`, and `jq`;
- the Forgejo base URL, repository owner, and repository name;
- the dedicated `minos` Forgejo bot token in a local file, with its effective
  access checked and limited to the opted-in repository before activation;
- a new high-entropy webhook secret in a different local file;
- a short-lived, dedicated repository-administration token for registering the
  hook (do not store this token in `/etc/minos`);
- the synced, pinned `review-panel` and `root-cause` skills;
- working Claude and Codex logins
  for the deployment user. These are the service's model
  credentials. This repository
  supplies the run-body executable;
- the `claude`, `codex`, and `ensemble` executables available on the deployment
  user's systemd-manager `PATH`. Interactive shell startup files do not set the
  environment of detached user units, so user-local installations need stable
  entry points such as `/usr/local/bin/{claude,codex,ensemble}`.

> [!WARNING]
> Never use the operator's personal Forgejo credential: the forge token must
> belong to the dedicated `minos` bot. Constrain its effective token scopes and
> prove that access before activation. Do not put any credential in the
> repository, shell history, webhook URL, or PR workspace.

Set the deployment values. `FORGEJO_BASE` is the instance root, without
`/api/v1`.

```sh
export DEPLOY_USER=bob
export MINOS_CHECKOUT=/path/to/Minos
export FORGEJO_BASE=https://forgejo.example.invalid
export FORGEJO_OWNER=REPLACE_WITH_OWNER
export FORGEJO_REPO=REPLACE_WITH_REPOSITORY
export MINOS_RECEIVER_URL=http://REPLACE_WITH_MINOS_LAN_ADDRESS:8919
export WEBHOOK_SECRET_SOURCE=/secure/path/to/new-webhook-secret
export FORGE_TOKEN_SOURCE=/secure/path/to/dedicated-forge-token
```

## 1. Create the deployment account and enable lingering

Every accountable lifecycle is a detached transient user unit. Lingering keeps
the user's service manager alive when nobody is
logged in.

```sh
id "$DEPLOY_USER" >/dev/null 2>&1
sudo loginctl enable-linger "$DEPLOY_USER"
loginctl show-user "$DEPLOY_USER" --property=Linger
```

The final command must print `Linger=yes`.

## 2. Build and install the binary, adaptations, and review run body

```sh
cd "$MINOS_CHECKOUT"
make check
go build -o /tmp/minos ./cmd/minos

sudo install -o root -g root -m 0755 /tmp/minos /usr/local/bin/minos
sudo install -d -o root -g root -m 0755 /opt/minos/adaptations/forgejo
sudo install -o root -g root -m 0755 scripts/adaptations/forgejo/* \
  /opt/minos/adaptations/forgejo/

sudo install -d -o root -g root -m 0755 \
  /opt/minos/run-body /opt/minos/review /opt/minos/lifecycle
sudo install -o root -g root -m 0755 scripts/run-body/* \
  /opt/minos/run-body/
sudo install -o root -g root -m 0755 scripts/review/* \
  /opt/minos/review/
sudo install -o root -g root -m 0644 lifecycle/*.md /opt/minos/lifecycle/
sudo install -o root -g root -m 0644 examples/config/pins.toml \
  /opt/minos/pins.toml
sudo install -o root -g root -m 0644 deploy/etc/minos/worker-models.json \
  /opt/minos/worker-models.json

# The fix run's skill is the one service-authored skill, shipped with this
# repository (unlike the sync-owned skills/foundry tree installed below).
sudo install -d -o root -g root -m 0755 /opt/minos/skills/service/fix
sudo install -o root -g root -m 0644 skills/service/fix/SKILL.md \
  /opt/minos/skills/service/fix/SKILL.md

sudo install -d -o root -g root -m 0755 /opt/minos/docs
sudo install -o root -g root -m 0644 docs/go-live.md /opt/minos/docs/go-live.md
```

The adaptation and review scripts call `curl`, `git`, and `jq`; keep those
commands on the deployment account's normal system path. The installed pins
file governs the **lead** role — the accountable session's own model,
operator-approved 2026-07-10 (`claude-opus-4-8`) and enforced at runtime by the
launch wrapper's early-stream interlock. Re-confirm at go-live that it still
names a current model generation. The worker roster entries (specialist,
verifier, bar-judge) are reference only: the Foundry review workflows carry
their own worker engine/model pins (see the pins file header). The
placeholder-rejection machinery remains: the launcher fails before model use
while a `REPLACE_WITH_…` placeholder remains in the role set.

`worker-models.json` is the separate admission policy for the pins carried by
the synced Foundry workflows. The Ensemble launcher relocates each durable run
record under the Minos attempt directory, and the lifecycle admits every
recognised review, verification, bar, and repair `agent.json` against this
policy before its judgement counts. Other labels carry no lifecycle authority.
Reconcile this file whenever the synced workflow pins change; it validates
served models and does not select them.

After Foundry sync populates `skills/foundry/`, install that sync-owned tree:

```sh
sudo install -d -o root -g root -m 0755 /opt/minos/skills/foundry
sudo cp -a skills/foundry/. /opt/minos/skills/foundry/
```

Confirm the detached-unit environment can resolve every engine-side executable:

```sh
sudo -u "$DEPLOY_USER" env \
  PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin \
  sh -c 'command -v claude && command -v codex && command -v ensemble'
```

## 3. Install configuration and credentials

Create the production paths and install the inactive templates. The repository
example deliberately ends in `.toml.example`: only `repos/*.toml` files opt a
repository in.

```sh
sudo install -d -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0750 \
  /etc/minos /etc/minos/repos \
  /var/lib/minos/runs /var/lib/minos/incidents /var/log/minos
sudo install -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0640 \
  deploy/etc/minos/service.toml /etc/minos/service.toml
sudo install -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0640 \
  deploy/etc/minos/run-body.env /etc/minos/run-body.env
sudo install -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0640 \
  deploy/etc/minos/preflight.toml /etc/minos/preflight.toml
sudo install -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0640 \
  deploy/etc/minos/repos/owner--repository.toml.example \
  /etc/minos/repos/owner--repository.toml.example
sudo install -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0600 \
  "$WEBHOOK_SECRET_SOURCE" /etc/minos/webhook.secret
sudo install -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0600 \
  "$FORGE_TOKEN_SOURCE" /etc/minos/forgejo.token
```

Edit `/etc/minos/service.toml` and replace
`REPLACE_WITH_FORGEJO_BASE_URL` with `FORGEJO_BASE`. Keep the receiver bound to
the LAN interface only; `:8919` is appropriate when the container itself has no
non-LAN route.

Do not activate a repository yet. First sync the grown `review-panel` and
general `root-cause` skills into the checkout, install them under
`/opt/minos/skills/foundry/`, complete the lifecycle instruction's verify-on-arrival checks,
obtain operator approval for every model pin, and verify both subscription
logins as the deployment user:

```sh
sudo -u "$DEPLOY_USER" /home/"$DEPLOY_USER"/.local/bin/claude auth status
sudo -u "$DEPLOY_USER" /home/"$DEPLOY_USER"/.local/bin/codex login status
```

### Recover a stale Claude OAuth refresh token

Two machines using the same Claude subscription can rotate the shared refresh
token out from under each other. The characteristic failure is an immediate,
zero-cost HTTP 403 claiming that the organisation disabled Claude subscription
access. Minos reports this as `stale-oauth`; it is distinct from quota
exhaustion and a genuinely disabled subscription only when the probe observes
both immediate failure and zero recorded cost. If either discriminator is
absent, Minos reports `unknown` rather than guessing from the misleading text.

Prefer re-authenticating directly as the deployment user:

```sh
sudo -u "$DEPLOY_USER" /home/"$DEPLOY_USER"/.local/bin/claude auth login
sudo -u "$DEPLOY_USER" /home/"$DEPLOY_USER"/.local/bin/claude auth status
sudo -u "$DEPLOY_USER" /usr/local/bin/minos preflight \
  --config /etc/minos/preflight.toml
```

If authentication must happen on another trusted machine, stop before copying
anything until `claude auth status` succeeds there. Transfer
`~/.claude/.credentials.json` through the operator's protected secret-transfer
channel, install it as the deployment user with mode `0600`, and delete the
transfer copy. Never print, paste, source, or place this file in the checkout,
a command argument, a PR workspace, or a diagnostic log. Then run the same
status and preflight commands as the deployment user. `minos ws-exec` remains
the boundary that removes service credentials from PR-controlled commands.

The lead launcher uses the explicit `MINOS_CLAUDE` path from `run-body.env` and
depends on Claude JSONL exposing a served model in a model-bearing early
`system/init` or `assistant` event. Update the deployment CLI during go-live and
prove that event before activation. If a deployed worker engine exposes no
served-model field in its archived output, worker provenance honestly records
`model-unknown`; the lead has no such degradation path. The repository template
points at the real run body, so an incomplete deployment fails loudly instead
of falling back to `stub-run`.

## 4. Install and validate the systemd user units

```sh
sudo install -d -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0755 \
  "/home/$DEPLOY_USER/.config/systemd/user"
sudo install -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0644 \
  deploy/systemd/user/minos-receiver.service \
  deploy/systemd/user/minos-sweep.service \
  deploy/systemd/user/minos-sweep.timer \
  deploy/systemd/user/minos-heartbeat.service \
  deploy/systemd/user/minos-heartbeat.timer \
  "/home/$DEPLOY_USER/.config/systemd/user/"

DEPLOY_UID="$(id -u "$DEPLOY_USER")"
sudo -u "$DEPLOY_USER" env XDG_RUNTIME_DIR="/run/user/$DEPLOY_UID" \
  systemd-analyze --user verify \
  "/home/$DEPLOY_USER/.config/systemd/user/minos-receiver.service" \
  "/home/$DEPLOY_USER/.config/systemd/user/minos-sweep.service" \
  "/home/$DEPLOY_USER/.config/systemd/user/minos-sweep.timer" \
  "/home/$DEPLOY_USER/.config/systemd/user/minos-heartbeat.service" \
  "/home/$DEPLOY_USER/.config/systemd/user/minos-heartbeat.timer"
sudo systemctl --user --machine="$DEPLOY_USER@.host" daemon-reload
```

The first sweep becomes due two minutes after the user manager starts; enabling
the timer after that point may run it immediately. Later sweeps are scheduled
10 minutes after the previous activation with up to two minutes of jitter. The
sweep service cannot overlap itself.

## 5. Rehearse before activation

Run readiness preflight before any expensive lifecycle or live activation:

```sh
sudo -u "$DEPLOY_USER" /usr/local/bin/minos preflight \
  --config /etc/minos/preflight.toml
```

The command emits one JSON report containing a pass/fail result and diagnostic
for forge identity and permissions, every pinned engine model, the optional
registry, the incident stream, and required toolchain binaries. A failed probe
also makes the command exit non-zero.

Admission repeats this gate automatically before every detached lifecycle
launch. The report is cached for five minutes under `runs.dir`, keyed by the
exact `preflight.toml` content, so webhook bursts do not repeatedly spend engine
quota. An expired or changed configuration is probed again. Failure refuses the
launch and always updates the PR-keyed `readiness-preflight` incident in the
coordination ledger. When `preflight.toml` remains readable, it also updates the
configured incident stream; a malformed or unreadable preflight file cannot
supply that stream path and remains visible through the ledger and service log.
No launch-gate failure writes a PR error.

From the checkout, run the offline deployment smoke:

```sh
scripts/e2e/deployment-smoke.sh
```

It installs the binary and adaptation scripts to a temporary prefix, starts the
receiver with dummy secrets, replays a signed Forgejo 14.0.5 fixture, runs a
sweep against the same config root, and validates temporary copies of the
systemd units. Its expected delivery response is `202 not opted in`: this proves
the listener, HMAC verification, event normalisation, and opt-in boundary without
contacting a forge or spawning a run.

For a full disposable-Forgejo exercise, run `make e2e`. That deterministic mode
uses Docker and Forgejo 14.0.5 and spends no engine quota. Run the deliberately
small representative journeys separately when real pinned-engine evidence is
required:

```sh
MINOS_E2E_LIVE=clean make e2e
MINOS_E2E_LIVE=stopped make e2e
```

The harness prints the retained temporary directory on completion. Every mode
keeps automatic `instrumentation.json` evidence under `runs/attempts/`; the live
engine modes additionally require the lead log and `lifecycle-index.md`. These
are development gates against a disposable forge; none contacts or mutates the
deployed Minos service, and they do not replace the live smoke below.

## 6. Start the receiver and sweep timer

Do this only after `/etc/minos/service.toml` contains the real Forgejo URL and
both installed secret files are populated.

```sh
sudo systemctl --user --machine="$DEPLOY_USER@.host" \
  enable --now minos-receiver.service
sudo systemctl --user --machine="$DEPLOY_USER@.host" \
  enable --now minos-sweep.timer
sudo systemctl --user --machine="$DEPLOY_USER@.host" \
  enable --now minos-heartbeat.timer
sudo systemctl --user --machine="$DEPLOY_USER@.host" status --no-pager \
  minos-receiver.service minos-sweep.timer minos-heartbeat.timer
```

### Wire the external dead-man monitor

The heartbeat and incident stream are useful only when something outside Minos
checks them. Install `deploy/systemd/host/minos-{deadman,incidents}@.{service,timer}`
on the container host (or use an equivalent external monitor), install the `minos`
binary there, and create
`/etc/minos-deadman/minos.env` with the host-visible path:

```sh
MINOS_HEARTBEAT_PATH=/host/path/to/the/container/rootfs/var/lib/minos/heartbeat.json
MINOS_HEARTBEAT_MAX_AGE=3m
MINOS_INCIDENT_STORE=/host/path/to/the/container/rootfs/var/lib/minos/incidents
```

The host path depends on the container storage backend, so the repository cannot name
it honestly. `deployment-cutover` must determine that path, install and enable
`minos-deadman@minos.timer` and `minos-incidents@minos.timer`, and connect failed
units to the operator's alerting transport. The incident monitor exits non-zero
while any incident is open, making the failure visible outside the Minos service
and scheduler. No pager or notification transport is commissioned in the current
configuration; until one is chosen, the container host's failed units are the active
operator signal.

Each incident is identified by forge, owner, repository, pull request, and
failure category. Attempt number, observed revisions, and retry disposition are
updated evidence, not identity, so retries update the same record instead of
producing another alert. Operators can inspect the JSON stream and make
acknowledgement and recovery explicit:

```sh
minos incident list --store /var/lib/minos/incidents
minos incident acknowledge --store /var/lib/minos/incidents \
  --forge forgejo --owner OWNER --repo REPOSITORY --pr NUMBER \
  --category FAILURE_CATEGORY --actor OPERATOR
minos incident recover --store /var/lib/minos/incidents \
  --forge forgejo --owner OWNER --repo REPOSITORY --pr NUMBER \
  --category FAILURE_CATEGORY
```

Incident diagnostics must use a service-owned failure description and point to
the relevant `run.log`; never pass raw backend output or credentials to
`--diagnostic`. Pull-request reviews, comments, and statuses must not contain
incident or backend error text.

Confirm the receiver journal contains `minos receiver listening on :8919`:

```sh
sudo journalctl --user --machine="$DEPLOY_USER@.host" \
  --unit=minos-receiver.service --lines=20 --no-pager
```

## 7. Register the Forgejo webhook

The deployed receiver consumes Forgejo pull-request, label, approval, rejection,
and issue-comment deliveries. Register all five event selectors below: Forgejo
uses dedicated headers for the granular review and label events, and the
receiver relies on those headers to select the corresponding trigger.

**First, allow Forgejo to dial the receiver.** Forgejo's SSRF guard
(`[webhook] ALLOWED_HOST_LIST` in `app.ini`, default `external`) silently
refuses webhook targets on loopback and private addresses — and a LAN
receiver is exactly that. The failure is invisible from the receiver's side:
every delivery is recorded as failed in Forgejo's own delivery history
("webhook can only call allowed HTTP servers") while the hook itself saves
and tests without complaint. Add the receiver's address before registering
the hook:

```ini
[webhook]
ALLOWED_HOST_LIST = 192.0.2.41
```

Restart Forgejo, then confirm end-to-end with the test-delivery step in
section 9 — a delivery must show as succeeded in Forgejo's delivery
history, not merely accepted at creation.

Load the same webhook secret without echoing it, and supply a short-lived
dedicated token whose account administers this repository. Restrictive temporary
files keep both credentials out of process arguments:

```sh
printf 'Forgejo hook token: ' >&2
read -rs FORGEJO_HOOK_TOKEN
printf '\n'
WEBHOOK_SECRET="$(sudo cat /etc/minos/webhook.secret)"
export WEBHOOK_SECRET
umask 077
HOOK_BODY="$(mktemp)"
CURL_CONFIG="$(mktemp)"
trap 'rm -f "$HOOK_BODY" "$CURL_CONFIG"' EXIT

jq -n \
  --arg url "$MINOS_RECEIVER_URL/hooks/forgejo" \
  '{
    type: "gitea",
    config: {url: $url, content_type: "json", secret: env.WEBHOOK_SECRET},
    events: [
      "pull_request", "pull_request_label", "pull_request_rejected",
      "pull_request_approved", "issue_comment"
    ],
    active: true
  }' >"$HOOK_BODY"
printf 'header = "Authorization: token %s"\n' "$FORGEJO_HOOK_TOKEN" \
  >"$CURL_CONFIG"

curl --fail-with-body --request POST \
  --config "$CURL_CONFIG" \
  --header 'Content-Type: application/json' \
  --data-binary @"$HOOK_BODY" \
  "$FORGEJO_BASE/api/v1/repos/$FORGEJO_OWNER/$FORGEJO_REPO/hooks"

rm -f "$HOOK_BODY" "$CURL_CONFIG"
trap - EXIT
unset WEBHOOK_SECRET FORGEJO_HOOK_TOKEN
```

The relative API operation is
`POST /repos/{owner}/{repo}/hooks` beneath Forgejo's `/api/v1` base. Forgejo
accepts both `Authorization: token …` and bearer authentication. Creating the
hook requires the `write:repository` scope and an account with
repository-administrator permission.

## 8. Opt the repository in

Copy the inactive example and replace every placeholder. Eligibility selects
the PR authors this deployment serves; current forge state then decides whether
the PR needs a lifecycle. There are no review, repair, finish, or flaky-test
trigger stanzas, and PR labels grant no authority.

```sh
sudo -u "$DEPLOY_USER" cp \
  /etc/minos/repos/owner--repository.toml.example \
  "/etc/minos/repos/${FORGEJO_OWNER}--${FORGEJO_REPO}.toml"
sudoedit "/etc/minos/repos/${FORGEJO_OWNER}--${FORGEJO_REPO}.toml"
sudo systemctl --user --machine="$DEPLOY_USER@.host" \
  restart minos-receiver.service
```

Opt-in is immediate for the next webhook or sweep. The coordination database is
created beneath `runs.dir`; opening, migrating, or verifying it must succeed
before Minos admits work. Do not delete or replace it to recover a deployment:
its leases and fencing tokens prevent an old attempt from writing after
replacement.

`runs.max-concurrent` is the host-wide run admission cap. The shipped value is
`3`: receiver deliveries beyond it return an accepted capacity deferral, and
the reconciliation sweep starts the eligible run after a slot clears. Keep the
value aligned with the host's measured capacity; it counts complete run process
trees, not the individual reviewer sessions inside each run.

## 9. Perform the live smoke check

Use Forgejo's webhook test-delivery control, or open/synchronise a harmless test
PR. Then check both sides:

```sh
sudo journalctl --user --machine="$DEPLOY_USER@.host" \
  --unit=minos-receiver.service --since='5 minutes ago' --no-pager
sudo systemctl --user --machine="$DEPLOY_USER@.host" start minos-sweep.service
sudo systemctl --user --machine="$DEPLOY_USER@.host" show \
  minos-sweep.service --property=Result --property=ExecMainStatus
sudo journalctl --user --machine="$DEPLOY_USER@.host" \
  --unit=minos-sweep.service --since='5 minutes ago' --no-pager
sudo tail -n 50 /var/log/minos/sweep.log
```

The delivery must receive HTTP `202`. The body names the reconciliation
decision, such as `admit`, `live`, or `nothing`; a capacity deferral remains an
accepted delivery. The receiver journal records one line when a
delivery passes signature authentication and one line when a bad signature is
rejected, including the forge event and delivery identifiers. Also read the
Forgejo delivery record itself: a red/failed delivery whose
response says "webhook can only call allowed HTTP servers" means the
`ALLOWED_HOST_LIST` step in section 7 was missed, and every event will fail
silently until it is fixed. The sweep service must finish successfully and
its journal must show a completed invocation. The application sweep log records
work it finds; an empty file is valid when there are no open in-scope PRs.

Finally, confirm the timer and lingering state:

```sh
sudo systemctl --user --machine="$DEPLOY_USER@.host" \
  list-timers minos-sweep.timer
loginctl show-user "$DEPLOY_USER" --property=Linger
```

At this point the deployment mechanics are live. A real review is ready only
after the synced skill has passed its interface re-check, the operator-approved
pins and subscription logins are verified, and a full run has completed against
a disposable PR.

## 10. Upgrade an existing deployment

Before installing an updated checkout, compare every shipped configuration
file with the deployed tree. This deliberately reports local repository files
and configured values as differences: review them, preserve intentional local
settings, and carry across every newly shipped key. In particular, do not copy
a template wholesale over credentials or deployment-specific values.

```sh
cd "$MINOS_CHECKOUT"
sudo diff -ru \
  --exclude=webhook.secret --exclude=forgejo.token \
  deploy/etc/minos/ /etc/minos/ || {
    status=$?
    [ "$status" -eq 1 ] || exit "$status"
  }
```

Only after resolving the displayed drift should you repeat the binary, script,
lifecycle, skill, configuration, and systemd-unit installation steps above. Run
`make check` and the offline deployment smoke before restarting either service,
then repeat the live smoke check in section 9. This makes newly required values
such as additions to `run-body.env` visible before a live run spends its retry
budget discovering them.
