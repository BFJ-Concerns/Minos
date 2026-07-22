# Deployment

Minos runs on a disposable single-tenant machine with Claude Code, the Codex
CLI, Node.js, Git, Go, curl, jq and the repository toolchains installed. The
machine itself is the containment boundary.

1. Build and install `cmd/minos` as `/usr/local/bin/minos`.
2. Install `scripts/adaptations/forgejo` under
   `/opt/minos/adaptations/forgejo`, `scripts/run-body/run-body` under
   `/opt/minos/run-body`, `lifecycle` under `/opt/minos/lifecycle`, and
   `skills/foundry/root-cause` under `/opt/minos/skills/foundry/root-cause`.
3. Run `scripts/install-review-runtime /opt/minos`. It installs the complete
   test-free review runtime under `/opt/minos`: the vendored Ensemble launcher
   and its provenance files, the adjudication wrapper and adapter, and every
   non-test workflow file. It also verifies the launcher against
   `ensemble.mjs.sha256`.
4. Copy `deploy/etc/minos` to `/etc/minos`, replace the placeholder values,
   and add one repository TOML file per opted-in repository. In `run-body.env`:

   - set `MINOS_CLAUDE` to the installed Claude executable;
   - keep `MINOS_LEAD_MODEL` pinned to `claude-opus-4-8`;
   - set `MINOS_CLAUDE_CONFIG_SEED` to a directory containing known-good,
     non-interactive Claude subscription state;
   - set `MINOS_CODEX_CONFIG_SEED` to a directory containing known-good,
     non-interactive Codex ChatGPT authentication state; and
   - check that `MINOS_LIFECYCLE_INSTRUCTION`, `MINOS_REVIEW_WORKFLOW`,
     `MINOS_ROOT_CAUSE_SKILL` and the other installed paths match the deployment.

   Provision both seed directories when the disposable box is launched. Each
   run copies their contents into its private `HOME`: Claude state goes to
   `$HOME/.claude` and Codex state to `$HOME/.codex`. The lead and Ensemble
   workers therefore inherit both engines' configured authentication without an
   interactive login or a proxy.

   `MINOS_LIFECYCLE_INSTRUCTION` points to the vendored `lifecycle.md`: editable
   markdown fed to the lead session as its standing instructions and launch
   prompt. A review-panel-style walkthrough is the model, not a literal
   installed skill.

   Create the webhook secret and forge token files separately.
5. Install and enable `minos-receiver.service` and `minos-sweep.timer` from
   `deploy/systemd/user` for the deployment user.
6. Configure the forge webhook to post to `/hooks/forgejo` using the matching
   secret.

Each run launches the `claude-opus-4-8` lead through `claude --bg`. The lead
invokes review and repository-brief workflows through
`/opt/minos/workflows/adjudicated-review`; the wrapper runs the selected script
with `/opt/minos/runtime/ensemble.mjs` and asks the sibling run-record adapter to
confirm the actual served model family for every required leg. Fix workflows
use the same launcher directly. The foreground run body waits for the exact
background session to finish and stops it. The transient systemd unit bounds a
wedged run at 12 hours.

When a required check is genuinely red or the pull request carries the
`Flaky Test` label, the lead dispatches a fix agent with the vendored root-cause
skill on the same `codex` / `gpt-5.6-sol` pin as the other fix agents. It
diagnoses the failure, fixes only what it proves, and pushes the result for
fresh build and test verification.

The receiver handles new events immediately. The sweep periodically starts any
open, non-draft pull request whose current head has no Minos review.
