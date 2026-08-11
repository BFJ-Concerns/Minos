export const meta = {
  name: "minos-root-cause-repair",
  description: "Diagnose an evidenced failure and commit only a proved repair",
  phases: [{ title: "Root cause", detail: "prove the failure cause and commit its repair" }],
};

const ROOT_CAUSE_MODEL = "gpt-5.6-sol";

const rootCauseResultSchema = {
  type: "object",
  additionalProperties: false,
  required: ["diagnosis", "commit", "writeUp"],
  properties: {
    diagnosis: { type: "string" },
    commit: { type: "string" },
    writeUp: { type: "string" },
  },
};

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
  return { status: "incomplete", reason, diagnosis: "", commit: "", writeUp: "" };
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

Treat the supplied evidence as diagnostic input, not proof of a cause. Reproduce the failure and prove its actual cause, and say which side of the reconciliation it lives on: the pull request's own changes, or commits that arrived from the reconciled target. A pull-request-side cause is yours to repair: fix only what you prove. A proven target-side cause is out of scope for this repair — the pull-request branch must not carry fixes for its target's breakage, so never revert or override target-side changes and never retarget or remove the pull request's test expectations to fit a broken target. For a target-side cause, return an empty commit and a diagnosis naming the target-side commit and mechanism: that report is the deliverable.

Delivery geometry: your commit is delivered by pushing to the pull-request branch, so a repair counts only if it applies to that branch's own tree. Your working tree may be a reconciliation merge of the pull-request head with its target, so a file being present in the working tree does not prove it is deliverable — check the pull-request side of the history (git log or git ls-tree on the reviewed head, not the working tree) for the files the fix must change. When the only fix lands in files that exist solely on the target side, the repair cannot be delivered through this pull request: return an empty commit and a writeUp saying the fix cannot be delivered through the pull-request branch — never that the file does not exist, since it is the delivery path that is absent, not the file. Do not author an undeliverable fix.

If your repair resolves the failure by retargeting or removing a test expectation, the writeUp must say so plainly: name the superseding commit that made the old expectation unsatisfiable, and state that coverage was reduced and where — so a recognised supersession is distinguishable from a weakened test without re-deriving the history.

Commit any repair with the configured Minos identity. Return the diagnosis, the commit SHA, and a concise code-only writeUp explaining the repair. Never push. If no repository mutation is proved and committed, return an empty commit string.`;
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
