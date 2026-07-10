# Go Live with Pump-19

This runbook takes a bare systemd-based deployment box to a listening Pump-19
receiver and periodic reconciliation sweep. It is written for the dedicated
Pump-19 account on a disposable box: the box is the container.
Do not add a nested container runtime.

The commands do not activate the quarantined pre-2026-07-08 deployment. They
install the current repository contents afresh.

## Prerequisites

Prepare these before starting:

- a checkout of this repository at the approved release commit;
- root access on the deployment box;
- `go`, `git`, `curl`, and `jq`;
- the Forgejo base URL, repository owner, and repository name;
- a dedicated, hard-capped Forgejo service token in a local file, restricted to
  the opted-in repository with `write:repository` and `write:issue` scopes;
- a new high-entropy webhook secret in a different local file;
- a short-lived, dedicated repository-administration token for registering the
  hook (do not store this token in `/etc/pump19`);
- the real run-body executable, pinned skill, and model-backend credentials from
  their owning unit when they are ready.

> [!WARNING]
> Never use the operator's personal Forgejo or model credentials. The service
> credentials must be dedicated and hard-capped. Do not put any credential in
> the repository, shell history, webhook URL, or PR workspace.

Set the deployment values. `FORGEJO_BASE` is the instance root, without
`/api/v1`.

```sh
export DEPLOY_USER=pump19
export PUMP19_CHECKOUT=/path/to/Pump-19
export FORGEJO_BASE=https://forgejo.example.invalid
export FORGEJO_OWNER=REPLACE_WITH_OWNER
export FORGEJO_REPO=REPLACE_WITH_REPOSITORY
export PUMP19_RECEIVER_URL=http://REPLACE_WITH_PUMP_LAN_ADDRESS:8919
export WEBHOOK_SECRET_SOURCE=/secure/path/to/new-webhook-secret
export FORGE_TOKEN_SOURCE=/secure/path/to/dedicated-forge-token
```

## 1. Create the deployment account and enable lingering

Every review, fix, and finish run is a detached transient user unit. Lingering
keeps the user's service manager alive when nobody is logged in.

```sh
id "$DEPLOY_USER" >/dev/null 2>&1 || \
  sudo useradd --create-home --shell /bin/bash "$DEPLOY_USER"
sudo loginctl enable-linger "$DEPLOY_USER"
loginctl show-user "$DEPLOY_USER" --property=Linger
```

The final command must print `Linger=yes`.

## 2. Build and install the binary and adaptations

```sh
cd "$PUMP19_CHECKOUT"
make check
go build -o /tmp/pump19 ./cmd/pump19

sudo install -o root -g root -m 0755 /tmp/pump19 /usr/local/bin/pump19
sudo install -d -o root -g root -m 0755 /opt/pump19/adaptations/forgejo
sudo install -o root -g root -m 0755 scripts/adaptations/forgejo/* \
  /opt/pump19/adaptations/forgejo/

sudo install -d -o root -g root -m 0755 /opt/pump19/docs
sudo install -o root -g root -m 0644 docs/go-live.md /opt/pump19/docs/go-live.md
```

The adaptation scripts call `curl`, `git`, and `jq`; keep those commands on the
deployment account's normal system path.

## 3. Install configuration and credentials

Create the production paths and install the inactive templates. The repository
example deliberately ends in `.toml.example`: only `repos/*.toml` files opt a
repository in.

```sh
sudo install -d -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0750 \
  /etc/pump19 /etc/pump19/repos /etc/pump19/secrets \
  /var/lib/pump19/runs /var/log/pump19
sudo install -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0640 \
  deploy/etc/pump19/service.toml /etc/pump19/service.toml
sudo install -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0640 \
  deploy/etc/pump19/repos/owner--repository.toml.example \
  /etc/pump19/repos/owner--repository.toml.example
sudo install -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0600 \
  "$WEBHOOK_SECRET_SOURCE" /etc/pump19/secrets/forgejo-webhook
sudo install -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0600 \
  "$FORGE_TOKEN_SOURCE" /etc/pump19/secrets/forgejo-token
```

Edit `/etc/pump19/service.toml` and replace
`REPLACE_WITH_FORGEJO_BASE_URL` with `FORGEJO_BASE`. Keep the receiver bound to
the LAN interface only; `:8919` is appropriate when the container itself has no
non-LAN route.

Do not activate a repository yet. First install the real agent-session run body,
pinned skill, and its dedicated model credentials. Until `run-body` is filled,
the shipped default is `pump19 stub-run`; that default is useful for mechanical
proof but is not the review service.

## 4. Install and validate the systemd user units

```sh
sudo install -d -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0755 \
  "/home/$DEPLOY_USER/.config/systemd/user"
sudo install -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0644 \
  deploy/systemd/user/pump19-receiver.service \
  deploy/systemd/user/pump19-sweep.service \
  deploy/systemd/user/pump19-sweep.timer \
  "/home/$DEPLOY_USER/.config/systemd/user/"

DEPLOY_UID="$(id -u "$DEPLOY_USER")"
sudo -u "$DEPLOY_USER" env XDG_RUNTIME_DIR="/run/user/$DEPLOY_UID" \
  systemd-analyze --user verify \
  "/home/$DEPLOY_USER/.config/systemd/user/pump19-receiver.service" \
  "/home/$DEPLOY_USER/.config/systemd/user/pump19-sweep.service" \
  "/home/$DEPLOY_USER/.config/systemd/user/pump19-sweep.timer"
sudo systemctl --user --machine="$DEPLOY_USER@.host" daemon-reload
```

The first sweep becomes due two minutes after the user manager starts; enabling
the timer after that point may run it immediately. Later sweeps are scheduled
10 minutes after the previous activation with up to two minutes of jitter. The
sweep service cannot overlap itself.

## 5. Rehearse before activation

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

For a full disposable-Forgejo exercise, run `make e2e`. That test uses Docker
and Forgejo 14.0.5; it is a development gate, not a substitute for the live
smoke below.

## 6. Start the receiver and sweep timer

Do this only after `/etc/pump19/service.toml` contains the real Forgejo URL and
both installed secret files are populated.

```sh
sudo systemctl --user --machine="$DEPLOY_USER@.host" \
  enable --now pump19-receiver.service
sudo systemctl --user --machine="$DEPLOY_USER@.host" \
  enable --now pump19-sweep.timer
sudo systemctl --user --machine="$DEPLOY_USER@.host" status --no-pager \
  pump19-receiver.service pump19-sweep.timer
```

Confirm the receiver journal contains `pump19 receiver listening on :8919`:

```sh
sudo journalctl --user --machine="$DEPLOY_USER@.host" \
  --unit=pump19-receiver.service --lines=20 --no-pager
```

## 7. Register the Forgejo webhook

The deployed receiver consumes Forgejo pull-request and issue-comment
deliveries. Forgejo's `pull_request` hook selector is the umbrella for opened,
updated, label, approval, and rejection events; the receiver distinguishes the
delivered event using its headers and payload.

Load the same webhook secret without echoing it, and supply a short-lived
dedicated token whose account administers this repository. Restrictive temporary
files keep both credentials out of process arguments:

```sh
printf 'Forgejo hook token: ' >&2
read -rs FORGEJO_HOOK_TOKEN
printf '\n'
WEBHOOK_SECRET="$(sudo cat /etc/pump19/secrets/forgejo-webhook)"
export WEBHOOK_SECRET
umask 077
HOOK_BODY="$(mktemp)"
CURL_CONFIG="$(mktemp)"
trap 'rm -f "$HOOK_BODY" "$CURL_CONFIG"' EXIT

jq -n \
  --arg url "$PUMP19_RECEIVER_URL/hooks/forgejo" \
  '{
    type: "gitea",
    config: {url: $url, content_type: "json", secret: env.WEBHOOK_SECRET},
    events: ["pull_request", "issue_comment"],
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
accepts both `Authorization: token …` and bearer authentication. Use the
`write:repository` scope and an account with repository-administrator permission
are required to create the hook.

## 8. Opt the repository in

Copy the inactive example, replace every placeholder, and install the real run
body and pinned skill paths supplied by their owning unit. Review the trigger
actors particularly carefully: they are authority, not display names.

```sh
sudo -u "$DEPLOY_USER" cp \
  /etc/pump19/repos/owner--repository.toml.example \
  "/etc/pump19/repos/${FORGEJO_OWNER}--${FORGEJO_REPO}.toml"
sudoedit "/etc/pump19/repos/${FORGEJO_OWNER}--${FORGEJO_REPO}.toml"
sudo systemctl --user --machine="$DEPLOY_USER@.host" \
  restart pump19-receiver.service
```

Opt-in is immediate for the next webhook or sweep. There is no database or
resident state to migrate.

## 9. Perform the live smoke check

Use Forgejo's webhook test-delivery control, or open/synchronise a harmless test
PR. Then check both sides:

```sh
sudo journalctl --user --machine="$DEPLOY_USER@.host" \
  --unit=pump19-receiver.service --since='5 minutes ago' --no-pager
sudo systemctl --user --machine="$DEPLOY_USER@.host" start pump19-sweep.service
sudo systemctl --user --machine="$DEPLOY_USER@.host" show \
  pump19-sweep.service --property=Result --property=ExecMainStatus
sudo journalctl --user --machine="$DEPLOY_USER@.host" \
  --unit=pump19-sweep.service --since='5 minutes ago' --no-pager
sudo tail -n 50 /var/log/pump19/sweep.log
```

The delivery must receive HTTP `202`. With the repository active and a matching
trigger, the body is `spawned review`; duplicate or non-triggering deliveries
have other explicit `202` bodies. The sweep service must finish successfully and
its journal must show a completed invocation. The application sweep log records
work it finds; an empty file is valid when there are no open in-scope PRs.

Finally, confirm the timer and lingering state:

```sh
sudo systemctl --user --machine="$DEPLOY_USER@.host" \
  list-timers pump19-sweep.timer
loginctl show-user "$DEPLOY_USER" --property=Linger
```

At this point the deployment mechanics are live. A real review is ready only
when the placeholder run-body, pinned skill, and model credentials have also
been installed and a full run has completed against a disposable PR.
