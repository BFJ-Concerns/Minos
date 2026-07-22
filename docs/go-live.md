# Deployment

Minos runs on a disposable single-tenant machine with Claude Code, the Codex
CLI, Node.js, Git, Go, curl, jq and the repository toolchains installed. The
machine itself is the containment boundary.

1. Build and install `cmd/minos` as `/usr/local/bin/minos`.
2. Install `scripts/adaptations/forgejo` under
   `/opt/minos/adaptations/forgejo`, `scripts/run-body/run-body` under
   `/opt/minos/run-body`, `lifecycle` under `/opt/minos/lifecycle`, and
   `skills/foundry/root-cause` under `/opt/minos/skills/foundry/root-cause`.
3. Copy the whole `workflows/` directory into `/opt/minos/workflows`. This step
   installs the workflow scripts and their input builders, including
   `review.js`, `review-briefs.js` and `fix.js`; the runtime installer in the
   next step does not install them.
4. Run `scripts/install-review-runtime /opt/minos`. This separate step installs
   the vendored Ensemble launcher as `/opt/minos/runtime/ensemble.mjs`, installs
   its provenance files, verifies the launcher against
   `ensemble.mjs.sha256`, and (re)installs `adjudicated-review` plus
   `run-record-adjudicator.mjs` under `/opt/minos/workflows`. Both steps 3 and 4
   are required for a runnable review runtime: `MINOS_REVIEW_WORKFLOW` points to
   the wrapper at `/opt/minos/workflows/adjudicated-review`, which invokes the
   workflow scripts installed in step 3.
5. Copy `deploy/etc/minos` to `/etc/minos`, replace the placeholder values,
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

   > **TODO (G1):** Decide whether the lead is guided by a literal installed
   > `SKILL.md` or by `lifecycle.md` as its launch prompt. The current
   > `MINOS_LIFECYCLE_INSTRUCTION` path must remain configured, but do not treat
   > either guidance mechanism as the final contract until this is commissioned.

   Create the webhook secret and forge token files separately.
6. Install and enable `minos-receiver.service` and `minos-sweep.timer` from
   `deploy/systemd/user` for the deployment user.
7. Configure the forge webhook to post to `/hooks/forgejo` using the matching
   secret.

Each run launches the `claude-opus-4-8` lead through `claude --bg`. The lead
invokes review and repository-brief workflows through
`/opt/minos/workflows/adjudicated-review`; the wrapper runs the selected script
with `/opt/minos/runtime/ensemble.mjs` and asks the sibling run-record adapter to
confirm the actual served model family for every required leg. Fix workflows
use the same launcher directly. The foreground run body waits for the exact
background session to finish and stops it. The transient systemd unit bounds a
wedged run at 12 hours.

> **TODO (G2):** The finishing root-cause helper's native engine and model have
> not been commissioned. Do not invent either value. Until G2 is resolved, a
> run that requires the helper stops incomplete as described in
> `lifecycle/lifecycle.md`.

The receiver handles new events immediately. The sweep periodically starts any
open, non-draft pull request whose current head has no Minos review.
