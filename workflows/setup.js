export const meta = {
  name: "minos-setup",
  description: "Reconcile target conflicts and ready the repository environment",
  phases: [{ title: "Setup", detail: "resolve reconciliation conflicts and provision evidenced requirements" }],
};

const resultSchema = {
  type: "object",
  additionalProperties: false,
  required: ["reconciliation", "environment"],
  properties: {
    reconciliation: {
      type: "object",
      additionalProperties: false,
      required: ["attempted", "resolutions"],
      properties: {
        attempted: { type: "boolean" },
        resolutions: {
          type: "array",
          items: {
            type: "object",
            additionalProperties: false,
            required: ["path", "note"],
            properties: {
              path: { type: "string" },
              note: { type: "string" },
            },
          },
        },
      },
    },
    environment: {
      type: "object",
      additionalProperties: false,
      required: ["ready", "actions", "cause"],
      properties: {
        ready: { type: "boolean" },
        actions: { type: "array", items: { type: "string" } },
        cause: { type: ["string", "null"] },
      },
    },
  },
};

function validInput(input) {
  return Boolean(
    input &&
    (input.mode === "full" || input.mode === "reconcile-only") &&
    typeof input.workspace === "string" &&
    input.workspace !== "" &&
    Array.isArray(input.conflicts) &&
    input.conflicts.every((path) => typeof path === "string" && path !== "") &&
    (input.preimageDir === null || typeof input.preimageDir === "string") &&
    typeof input.buildCommand === "string" &&
    typeof input.testCommand === "string" &&
    input.guidance &&
    typeof input.guidance.content === "string" &&
    input.guidance.content.trim() !== "" &&
    input.setupBrief &&
    typeof input.setupBrief.content === "string" &&
    input.setupBrief.content.trim() !== "" &&
    (input.objections === null || Array.isArray(input.objections))
  );
}

function promptFor(input) {
  const conflictJob = input.conflicts.length === 0
    ? "There are no reconciliation conflicts. Report reconciliation.attempted as false and return no resolutions."
    : `Resolve these conflicted paths in order: ${JSON.stringify(input.conflicts)}.
The marker-bearing preimages are under: ${input.preimageDir}
Reconcile both parents' intents. Stage each resolution. Do not commit, push, or touch an unconflicted file.
Do not re-implement either side or add logic that exists in neither parent.`;
  const objections = input.objections === null
    ? "There are no lead objections from an earlier attempt."
    : `The lead rejected the earlier resolutions for these reasons: ${JSON.stringify(input.objections)}`;
  const environmentJob = input.mode === "reconcile-only"
    ? "This is a reconciliation-only retry. Do not inspect, install, upgrade, or otherwise alter the environment."
    : `After conflict resolution, verify and ready only what these configured commands need:
Build command: ${JSON.stringify(input.buildCommand)}
Test command: ${JSON.stringify(input.testCommand)}
Judge requirements from repository pins, manifests, and lockfiles. Verify working tools before installing anything.
Install missing global toolchains and per-checkout dependencies only when evidenced by those commands and repository files.
Do not modify repository files, regenerate a lockfile, invent a build or test command, or run either configured command.
When no tool or dependency action is needed, report the environment ready with an empty actions list.
When a requirement cannot be provisioned, return environment.ready false and state exactly what was needed, tried, and failed.`;

  return `Read and follow the shipped setup brief at ${input.setupBrief.readPath}.

<setup-brief path="${input.setupBrief.path}">
${input.setupBrief.content}
</setup-brief>

Judge the repository against its own guidance.

<project-guidance grounding="${input.guidance.grounding}" path="${input.guidance.path}">
${input.guidance.content}
</project-guidance>

Workspace: ${input.workspace}
Mode: ${input.mode}

First job — reconciliation:
${conflictJob}
${objections}

Second job — environment:
${environmentJob}

Return the required structured result.`;
}

const input = args && typeof args === "object" ? args : null;
if (!validInput(input))
  return { status: "incomplete", reason: "setup needs a valid deterministic input" };

phase("Setup");
const result = await agent(promptFor(input), {
  engine: "claude",
  schema: resultSchema,
  model: "claude-opus-5",
  effort: "high",
  label: "setup",
  phase: "Setup",
});

if (!result)
  return { status: "incomplete", reason: "setup agent returned no result" };
return { status: "complete", ...result };
