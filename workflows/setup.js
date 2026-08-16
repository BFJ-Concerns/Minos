export const meta = {
  name: "minos-setup",
  description: "Reconcile target conflicts and ready the repository environment",
  phases: [{ title: "Setup", detail: "resolve reconciliation conflicts and provision evidenced requirements" }],
};

const resultSchema = {
  type: "object",
  additionalProperties: false,
  required: ["reconciliation", "environment", "commandExecutions"],
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
    commandExecutions: {
      type: "object",
      additionalProperties: false,
      required: ["head", "build", "test"],
      properties: {
        head: { type: "string" },
        build: {
          type: "object",
          additionalProperties: false,
          required: ["command", "exitStatus"],
          properties: {
            command: { type: "string" },
            exitStatus: { type: ["integer", "null"], minimum: 0 },
          },
        },
        test: {
          type: "object",
          additionalProperties: false,
          required: ["command", "exitStatus"],
          properties: {
            command: { type: "string" },
            exitStatus: { type: ["integer", "null"], minimum: 0 },
          },
        },
      },
    },
  },
};

function validInput(input) {
  return Boolean(
    input &&
    (input.mode === "full" || input.mode === "reconcile-only") &&
    typeof input.head === "string" &&
    input.head !== "" &&
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
    ? `This is a reconciliation-only retry. Do not inspect, install, upgrade, or otherwise alter the environment.
Return commandExecutions with head ${JSON.stringify(input.head)}, both exact configured command strings, and null exit statuses; this retry record is not reusable execution evidence.`
    : `After conflict resolution, verify and ready only what these configured commands need:
Build command: ${JSON.stringify(input.buildCommand)}
Test command: ${JSON.stringify(input.testCommand)}
Judge requirements from repository pins, manifests, and lockfiles. Verify working tools before installing anything.
Install missing global toolchains and per-checkout dependencies only when evidenced by those commands and repository files.
Do not modify repository files, regenerate a lockfile, or invent a build or test command.
After provisioning, run each non-empty configured command exactly once to completion, build first and then test. One exception: when a command's run fails on an environment fault you then prove and repair, re-run that command after the evidenced repair and record the repair as an action; never re-run without a proven, repaired environment fault between attempts. Record commandExecutions with head ${JSON.stringify(input.head)}, each exact command string, and its final integer exit status. For an empty command, record its exact empty string and a null exit status without running anything. These raw outcomes are evidence only; do not classify them as passed, failed, or skipped.
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
const executions = result.commandExecutions;
const validExecution = (execution, command) =>
  execution && execution.command === command &&
  (input.mode === "reconcile-only" || command.trim() === ""
    ? execution.exitStatus === null
    : Number.isInteger(execution.exitStatus) && execution.exitStatus >= 0);
if (!executions || executions.head !== input.head ||
    !validExecution(executions.build, input.buildCommand) ||
    !validExecution(executions.test, input.testCommand))
  return { status: "incomplete", reason: "setup agent returned command outcomes outside the requested head and commands" };
return { status: "complete", ...result };
