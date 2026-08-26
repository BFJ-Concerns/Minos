export const meta = {
  name: "minos-setup",
  description: "Reconcile target conflicts and ready the repository environment",
  phases: [{ title: "Setup", detail: "resolve reconciliation conflicts and provision evidenced requirements" }],
};

const RECONCILIATION_MODEL = "claude-opus-5";
const PROVISIONING_MODEL = "gpt-5.6-terra";

const reconciliationSchema = {
  type: "object",
  additionalProperties: false,
  required: ["reconciliation"],
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
  },
};

const provisioningSchema = {
  type: "object",
  additionalProperties: false,
  required: ["environment", "commandExecutions"],
  properties: {
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

function contextFor(input) {
  return `Read and follow the shipped setup brief at ${input.setupBrief.readPath}.

<setup-brief path="${input.setupBrief.path}">
${input.setupBrief.content}
</setup-brief>

Judge the repository against its own guidance.

<project-guidance grounding="${input.guidance.grounding}" path="${input.guidance.path}">
${input.guidance.content}
</project-guidance>

Workspace: ${input.workspace}
Mode: ${input.mode}`;
}

function reconciliationPrompt(input) {
  const objections = input.objections === null
    ? "There are no lead objections from an earlier attempt."
    : `The lead rejected the earlier resolutions for these reasons: ${JSON.stringify(input.objections)}`;
  return `${contextFor(input)}

Resolve these conflicted paths in order: ${JSON.stringify(input.conflicts)}.
The marker-bearing preimages are under: ${input.preimageDir}
Reconcile both parents' intents. Stage each resolution. Do not commit, push, or touch an unconflicted file.
Do not re-implement either side or add logic that exists in neither parent.
For this reconciliation leg, do not inspect, install, upgrade, warm, or otherwise alter the environment, and do not run configured build or test commands. This limitation takes precedence over any environment instructions in the shipped setup brief; provisioning is a separate leg.
${objections}`;
}

function provisioningPrompt(input) {
  return `${contextFor(input)}

Reconciliation is already clean or has completed separately. Do not inspect or modify conflicted files.
Verify and ready only what these configured commands need:
Build command: ${JSON.stringify(input.buildCommand)}
Test command: ${JSON.stringify(input.testCommand)}
Judge requirements from repository pins, manifests, and lockfiles. Verify working tools before installing anything.
Install missing global toolchains and per-checkout dependencies only when evidenced by those commands and repository files.
Do not modify repository files, regenerate a lockfile, invent a build or test command, commit, or push.
After provisioning, run each non-empty configured command exactly once to completion, build first and then test. One exception: when a command's run fails on an environment fault you then prove and repair, re-run that command after the evidenced repair and record the repair as an action; never re-run without a proven, repaired environment fault between attempts. Record commandExecutions with head ${JSON.stringify(input.head)}, each exact command string, and its final integer exit status. For an empty command, record its exact empty string and a null exit status without running anything. These raw outcomes are evidence only; do not classify them as passed, failed, or skipped.
When no tool or dependency action is needed, report the environment ready with an empty actions list.
When a requirement cannot be provisioned, return environment.ready false and state exactly what was needed, tried, and failed.`;
}

const input = args && typeof args === "object" ? args : null;
if (!validInput(input))
  return { status: "incomplete", reason: "setup needs a valid deterministic input" };

phase("Setup");
let reconciliation = { attempted: false, resolutions: [] };
if (input.conflicts.length > 0) {
  const reconciliationResult = await agent(reconciliationPrompt(input), {
    engine: "claude",
    schema: reconciliationSchema,
    model: RECONCILIATION_MODEL,
    effort: "high",
    label: "setup-reconciliation",
    phase: "Setup",
  });
  if (!reconciliationResult)
    return { status: "incomplete", reason: "setup reconciliation agent returned no result" };
  reconciliation = reconciliationResult.reconciliation;
}

if (input.mode === "reconcile-only") {
  return {
    status: "complete",
    reconciliation,
    environment: { ready: true, actions: [], cause: null },
    commandExecutions: {
      head: input.head,
      build: { command: input.buildCommand, exitStatus: null },
      test: { command: input.testCommand, exitStatus: null },
    },
  };
}

const provisioningResult = await agent(provisioningPrompt(input), {
  engine: "codex",
  schema: provisioningSchema,
  model: PROVISIONING_MODEL,
  effort: "low",
  label: "setup-provision",
  phase: "Setup",
});

if (!provisioningResult)
  return { status: "incomplete", reason: "setup provisioning agent returned no result" };
const executions = provisioningResult.commandExecutions;
const validExecution = (execution, command) =>
  execution && execution.command === command &&
  (command.trim() === ""
    ? execution.exitStatus === null
    : Number.isInteger(execution.exitStatus) && execution.exitStatus >= 0);
if (!executions || executions.head !== input.head ||
  !validExecution(executions.build, input.buildCommand) ||
  !validExecution(executions.test, input.testCommand))
  return { status: "incomplete", reason: "setup agent returned command outcomes outside the requested head and commands" };
return { status: "complete", reconciliation, ...provisioningResult };
