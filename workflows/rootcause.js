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
