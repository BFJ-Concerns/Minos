# Deployment

Minos runs on a disposable single-tenant machine with Claude Code, Git, Go,
curl, jq and the repository toolchains installed. The machine itself is the
containment boundary.

1. Build and install `cmd/minos` as `/usr/local/bin/minos`.
2. Install `scripts/adaptations/forgejo` under
   `/opt/minos/adaptations/forgejo`, `scripts/run-body/run-body` under
   `/opt/minos/run-body`, `lifecycle` under `/opt/minos/lifecycle`, and
   `workflows` under `/opt/minos/workflows`. Install
   `skills/foundry/root-cause` under
   `/opt/minos/skills/foundry/root-cause`.
3. Copy `deploy/etc/minos` to `/etc/minos`, replace the placeholder values,
   and add one repository TOML file per opted-in repository. In
   `run-body.env`, set `MINOS_CLAUDE` to the installed Claude executable,
   set `ANTHROPIC_BASE_URL` to the operator proxy, and check the lifecycle,
   workflow and root-cause paths for the deployment. Create the proxy token file
   named by `MINOS_ANTHROPIC_CREDENTIAL_FILE`, along with the webhook secret
   and forge token files. The shipped runtime values pin
   `MINOS_LEAD_MODEL=gpt-5.6-sol` and
   `ANTHROPIC_DEFAULT_HAIKU_MODEL=gpt-5.6-luna`, enable
   `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY`, and set
   `CLAUDE_CODE_MAX_CONTEXT_TOKENS` and
   `CLAUDE_CODE_AUTO_COMPACT_WINDOW` to `265000`.
4. Install and enable `minos-receiver.service` and `minos-sweep.timer` from
   `deploy/systemd/user` for the deployment user.
5. Configure the forge webhook to post to `/hooks/forgejo` using the matching
   secret.

Each run reads the proxy token into `ANTHROPIC_AUTH_TOKEN`, launches the
`gpt-5.6-sol` lead through `claude --bg`, and keeps its Claude configuration
inside the run's scratch directory. The foreground run body waits for the exact
background session to finish and stops it. The transient systemd unit bounds a
wedged run at 12 hours.

The receiver handles new events immediately. The sweep periodically starts any
open, non-draft pull request whose current head has no Minos review.
