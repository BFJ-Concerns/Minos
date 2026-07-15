# Deployment

Minos runs on a disposable single-tenant machine with Codex, Ensemble, Git,
Go, jq and the repository toolchains installed. The machine itself is the
containment boundary.

1. Build and install `cmd/minos` as `/usr/local/bin/minos`.
2. Install `scripts/adaptations/forgejo` under
   `/opt/minos/adaptations/forgejo`, `scripts/run-body/run-body` under
   `/opt/minos/run-body`, `lifecycle` under `/opt/minos/lifecycle`, and
   `workflows` under `/opt/minos/workflows`.
3. Copy `deploy/etc/minos` to `/etc/minos`, replace the placeholder values,
   create the webhook secret and forge token files, and add one repository TOML
   file per opted-in repository.
4. Install and enable `minos-receiver.service` and `minos-sweep.timer` from
   `deploy/systemd/user` for the deployment user.
5. Configure the forge webhook to post to `/hooks/forgejo` using the matching
   secret.

The receiver handles new events immediately. The sweep periodically starts any
open, non-draft pull request whose current head has no Minos review.
