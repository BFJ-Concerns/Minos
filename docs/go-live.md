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
   - keep `MINOS_LEAD_MODEL` pinned to `claude-opus-5`;
   - set `MINOS_CLAUDE_CONFIG_SEED` to a directory containing known-good,
     non-interactive Claude configuration;
   - set `MINOS_CODEX_CONFIG_SEED` to a directory containing known-good,
     non-interactive Codex ChatGPT authentication state, whose `config.toml`
     carries

     ```toml
     [sandbox_workspace_write]
     network_access = true
     ```

     Without it the fix agents cannot create sockets at all, so repairing any
     test that binds a port is impossible. Ensemble runs worktree-isolated
     agents under `workspace-write` regardless of the workflow's own sandbox
     setting, so this seed key is the only place the grant can be made; and
   - check that `MINOS_LIFECYCLE_INSTRUCTION`, `MINOS_REVIEW_WORKFLOW`,
     `MINOS_ROOT_CAUSE_SKILL` and the other installed paths match the deployment.

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
   `$HOME/.claude` and Codex state to `$HOME/.codex`. The lead and Ensemble
   workers therefore inherit both engines' configured authentication without an
   interactive login. Gateway settings reach the Claude workers through the
   same inherited environment; Codex continues to use its own `CODEX_HOME`
   authentication.

   Each seed directory must contain only seed content readable by the run user.

   `MINOS_LIFECYCLE_INSTRUCTION` points to the vendored `lifecycle.md`: editable
   markdown fed to the lead session as its standing instructions and launch
   prompt. A review-panel-style walkthrough is the model, not a literal
   installed skill.

   Create the webhook secret and forge token files separately.
5. Install and enable `minos-receiver.service` and `minos-sweep.timer` from
   `deploy/systemd/user` for the deployment user.
6. Configure the forge webhook to post to `/hooks/forgejo` using the matching
   secret.

Each run launches the `claude-opus-5` lead through `claude --bg`. `run-body`
keeps resumable `done` and `blocked` turns alive; it stops the session only
after Claude reports `failed` or `stopped`, the lead writes its clean terminal
marker, or the background-job timeline has produced no output for the silence
timeout. The lead invokes review and repository-brief workflows through
`/opt/minos/workflows/adjudicated-review`; the wrapper runs the selected script
with `/opt/minos/runtime/ensemble.mjs` and asks the sibling run-record adapter to
confirm every required leg ran to completion and every finding carries a
verdict. Fix workflows
use the same launcher directly. The foreground run body waits for the exact
background session and stops it at one of those terminal conditions. The
transient systemd unit bounds a wedged run at 12 hours.

When a required check is genuinely red or the pull request carries the
`Flaky Test` label, the lead dispatches a fix agent with the vendored root-cause
skill on the same `codex` / `gpt-5.6-sol` pin as the other fix agents. It
diagnoses the failure, fixes only what it proves, and pushes the result for
fresh build and test verification.

The receiver handles new events immediately. The sweep periodically starts any
open, non-draft pull request whose current head has no Minos review.
