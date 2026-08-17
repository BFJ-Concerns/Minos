export const meta = {
  name: "minos-root-cause-repair",
  description: "Diagnose an evidenced failure and commit only a proved repair",
  phases: [{ title: "Root cause", detail: "prove the failure cause and commit its repair" }],
};

const ROOT_CAUSE_MODEL = "gpt-5.6-sol";

const rootCauseResultSchema = {
  type: "object",
  additionalProperties: false,
  required: ["diagnosis", "commit", "writeUp", "cause"],
  properties: {
    diagnosis: { type: "string" },
    commit: { type: "string" },
    writeUp: { type: "string" },
    cause: {
      type: "object",
      additionalProperties: false,
      required: ["side", "determinism", "locus"],
      properties: {
        side: { enum: ["pull-request", "target", "infrastructure", "unproven"] },
        determinism: { enum: ["deterministic", "intermittent", "unproven"] },
        locus: { enum: ["test-expectation", "product", "unproven"] },
      },
    },
  },
};

const unprovenCause = { side: "unproven", determinism: "unproven", locus: "unproven" };

function absolutePathOrNull(value) {
  return value === null || (typeof value === "string" && value.startsWith("/"));
}

function validInput(input) {
  return Boolean(
    input &&
    typeof input.skillPath === "string" &&
    input.skillPath.startsWith("/") &&
    typeof input.command === "string" &&
    (input.exitStatus === null ||
      (Number.isInteger(input.exitStatus) && input.exitStatus >= 0)) &&
    absolutePathOrNull(input.evidencePath) &&
    absolutePathOrNull(input.excerptPath) &&
    (input.evidencePath === null) === (input.excerptPath === null) &&
    (input.command !== "" || input.exitStatus === null) &&
    (input.command !== "" || input.evidencePath !== null)
  );
}

function failedResult(reason) {
  return { status: "incomplete", reason, diagnosis: "", commit: "", writeUp: "", cause: unprovenCause };
}

function repairPrompt(input) {
  const failure = input.command === ""
    ? "No failing local command is available; start from the caller-supplied finishing evidence below."
    : `Failing command: ${JSON.stringify(input.command)}\nExit status: ${input.exitStatus}`;
  const evidence = input.evidencePath === null
    ? "The caller supplied no captured output. Reproduce the failure before diagnosing it."
    : input.excerptPath === input.evidencePath
      ? `Caller evidence: read the captured output at ${input.evidencePath}.`
      : `Caller evidence: read ${input.excerptPath} — a failure-relevant excerpt of the captured output (the lines around failing, flaky, and retried tests, plus the harness summary; elided passages are marked). The complete original is at ${input.evidencePath}; consult it only where the excerpt lacks context you need.`;

  return `Read and follow the vendored root-cause skill at ${input.skillPath}/SKILL.md.

${failure}

${evidence}

Treat the supplied evidence as diagnostic input, not proof of a cause. Reproduce the failure, prove its actual cause, and report that cause on three axes in \`cause\`.

\`side\` — where the cause lives relative to the reconciliation: \`pull-request\` for the pull request's own changes, \`target\` for commits that arrived from the reconciled target, \`infrastructure\` when the evidence excludes the reconciled tree entirely — the failure was caused by the environment the check or command ran in (a runner dying, a network outage mid-download, toolchain provisioning failing before the tree's own code ran), not by anything either branch contains. A pull-request-side cause is yours to repair: fix only what you prove.

The bar for \`infrastructure\` is exclusion, not attribution: the evidence must establish that neither the pull request's changes nor the target's code produced the failure — a build that died before compiling anything, a fetch that timed out, a runner that vanished mid-job — and that is enough even when you cannot prove which infrastructure piece failed. Do not use it for a failure that merely *looks* environmental: a test that touches the network is the tree's own code failing. An \`infrastructure\` verdict has nothing in the tree to repair, so return an empty commit and a diagnosis stating the exclusion evidence; \`determinism\` and \`locus\` may honestly stay \`unproven\`.

\`determinism\` — \`deterministic\` when the failure reproduces every run under the conditions you identified; \`intermittent\` only once you have measured a failure rate the way the skill's \`references/intermittent-failures.md\` directs. A single observed failure is not a measured rate.

\`locus\` — for an intermittent failure, where the nondeterminism lives: \`test-expectation\` when the test asserts an ordering or timing the product never guaranteed, so the production behaviour is correct and the expectation is wrong; \`product\` when it is a real race, an unguarded await, or a shared-state conflict in the code under test.

Report \`unproven\` on any axis you have not proved. An unproven axis is an honest answer; a guessed one is the prohibited output.

A proven target-side cause is out of scope for this repair — the pull-request branch must not carry fixes for its target's breakage, so never revert or override target-side changes and never retarget or remove the pull request's test expectations to fit a broken target. For a target-side cause, return an empty commit and a diagnosis naming the target-side commit and mechanism: that report is the deliverable — unless it is the single exception in the next paragraph, so read that before concluding you have nothing to commit.

One exception, and only this one: a target-side cause proven \`intermittent\` with locus \`test-expectation\` is yours to repair. Such a test blocks every pull request that reconciles with it while its own branch stays green, and the correction belongs to the expectation rather than the product — so relax it to what the product actually guarantees, and let the fix ride this pull request. Never widen the exception past its proof: a \`product\` locus stays out of scope even when intermittent, because that test is the only witness to a real bug and retrying, quarantining, weakening, or deleting it ships that bug.

Delivery geometry: your commit is delivered by pushing to the pull-request branch, so a repair counts only if it applies to that branch's own tree. Your working tree may be a reconciliation merge of the pull-request head with its target, so a file being present in the working tree does not prove it is deliverable — check the pull-request side of the history (git log or git ls-tree on the reviewed head, not the working tree) for the files the fix must change. When the only fix lands in files that exist solely on the target side, the repair cannot be delivered through this pull request: return an empty commit and a writeUp saying the fix cannot be delivered through the pull-request branch — never that the file does not exist, since it is the delivery path that is absent, not the file. Do not author an undeliverable fix.

If your repair resolves the failure by retargeting or removing a test expectation, the writeUp must say so plainly: name the superseding commit that made the old expectation unsatisfiable, and state that coverage was reduced and where — so a recognised supersession is distinguishable from a weakened test without re-deriving the history. A repaired target-side flaky expectation carries the same disclosure: name the target-side commit that introduced it, the timing or ordering assumption you relaxed, and the measured rate that proved the flake.

Commit any repair with the configured Minos identity. Return the diagnosis, the cause verdict, the commit SHA, and a concise code-only writeUp explaining the repair. Never push. If no repository mutation is proved and committed, return an empty commit string.`;
}

const input = args && typeof args === "object" ? args : null;
if (!validInput(input))
  return failedResult("root-cause repair needs args {skillPath, command, exitStatus, evidencePath, excerptPath}");

phase("Root cause");

const result = await agent(repairPrompt(input), {
  engine: "codex",
  schema: rootCauseResultSchema,
  model: ROOT_CAUSE_MODEL,
  effort: "high",
  isolation: "worktree",
  label: "root-cause-repair",
  phase: "Root cause",
});

return result || failedResult("root-cause repair agent returned no result");
