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

function validInput(input) {
  return Boolean(
    input &&
    typeof input.skillPath === "string" &&
    input.skillPath.startsWith("/") &&
    typeof input.command === "string" &&
    (input.exitStatus === null ||
      (Number.isInteger(input.exitStatus) && input.exitStatus >= 0)) &&
    (input.evidence === null || typeof input.evidence === "string") &&
    (input.command !== "" || input.exitStatus === null) &&
    (input.command !== "" || input.evidence !== null)
  );
}

function failedResult(reason) {
  return { status: "incomplete", reason, diagnosis: "", commit: "", writeUp: "" };
}

function repairPrompt(input) {
  const failure = input.command === ""
    ? "No failing local command is available; start from the caller-supplied finishing evidence below."
    : `Failing command: ${JSON.stringify(input.command)}\nExit status: ${input.exitStatus}`;
  const evidence = input.evidence === null
    ? "The caller supplied no captured output. Reproduce the failure before diagnosing it."
    : `<caller-evidence>\n${input.evidence}\n</caller-evidence>`;

  return `Read and follow the vendored root-cause skill at ${input.skillPath}/SKILL.md.

${failure}

${evidence}

Treat the supplied evidence as diagnostic input, not proof of a cause. Reproduce the failure and prove its actual cause. Whether the break is the change's own defect or exists only against the reconciled target makes no difference: fix only what you prove, and say which kind it was when the evidence supports that conclusion.

Delivery geometry: your commit is delivered by pushing to the pull-request branch, so a repair counts only if it applies to that branch's own tree. Your working tree may be a reconciliation merge of the pull-request head with its target, so a file being present in the working tree does not prove it is deliverable — check the pull-request side of the history (git log or git ls-tree on the reviewed head, not the working tree) for the files the fix must change. When the only fix lands in files that exist solely on the target side, the repair cannot be delivered through this pull request: return an empty commit and a writeUp saying the fix cannot be delivered through the pull-request branch — never that the file does not exist, since it is the delivery path that is absent, not the file. Do not author an undeliverable fix.

If your repair resolves the failure by retargeting or removing a test expectation, the writeUp must say so plainly: name the superseding commit that made the old expectation unsatisfiable, and state that coverage was reduced and where — so a recognised supersession is distinguishable from a weakened test without re-deriving the history.

Commit any repair with the configured Minos identity. Return the diagnosis, the commit SHA, and a concise code-only writeUp explaining the repair. Never push. If no repository mutation is proved and committed, return an empty commit string.`;
}

const input = args && typeof args === "object" ? args : null;
if (!validInput(input))
  return failedResult("root-cause repair needs args {skillPath, command, exitStatus, evidence}");

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
