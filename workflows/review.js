export const meta = {
  name: "minos-review",
  description:
    "Planned pull-request review with bounded specialists and cross-family verification",
  phases: [
    { title: "Explore", detail: "produce a scoped, schema-validated review plan" },
    { title: "Specialise", detail: "run the planned concern specialists" },
    { title: "Verify", detail: "opposite-family verification of each proposed finding" },
  ],
};

const GPT_SPECIALIST_MODEL = "anthropic-gpt-5.6-sol";
const GPT_EXPLORER_MODEL = "anthropic-gpt-5.6-terra";
const CLAUDE_MODEL = "claude-opus-4-8";

const ROLE_BRIEFS = {
  exploration: "workflows/review-briefs/exploration.md",
  correctness: "workflows/review-briefs/correctness.md",
  security: "workflows/review-briefs/security.md",
  testing: "workflows/review-briefs/testing.md",
  design: "workflows/review-briefs/design.md",
  verifier: "workflows/review-briefs/verifier.md",
};

const SPECIALIST_FAMILY = {
  correctness: "gpt",
  security: "claude",
  testing: "gpt",
  design: "claude",
};

// These caps are the workflow's stage budgets. With two findings permitted per
// specialist, no valid run can dispatch more than 20 agents in total.
const STAGE_BUDGETS = {
  exploration: 1,
  specialists: 6,
  verification: 12,
  total: 19,
};
const MAX_PLANNED_SPECIALISTS = 6;
const MAX_FINDINGS_PER_SPECIALIST = 2;
const LOW_COMBINED_CONFIDENCE = 70;

function familyOf(modelId) {
  if (typeof modelId !== "string" || modelId === "") return "unknown";
  const id = modelId.toLowerCase();
  if (id.includes("gpt-")) return "gpt";
  if (id.includes("claude") || ["haiku", "sonnet", "opus", "fable"].includes(id))
    return "claude";
  return "unknown";
}

function modelForFamily(family) {
  return family === "gpt" ? GPT_SPECIALIST_MODEL : CLAUDE_MODEL;
}

function roleBriefsFromInput(input) {
  const entries = Array.isArray(input && input.instructionBriefs) ? input.instructionBriefs : [];
  return new Map(
    entries
      .filter((entry) =>
        entry &&
        typeof entry.path === "string" &&
        typeof entry.readPath === "string" &&
        typeof entry.content === "string"
      )
      .map((entry) => [entry.path, entry])
  );
}

function missingRoleBriefs(roleBriefs) {
  return [...new Set(Object.values(ROLE_BRIEFS))].filter((path) => !roleBriefs.has(path));
}

function projectGuidanceFromInput(input) {
  const guidance = input && input.guidance;
  if (
    !guidance ||
    typeof guidance.path !== "string" ||
    guidance.path === "" ||
    typeof guidance.content !== "string" ||
    guidance.content.trim() === "" ||
    typeof guidance.grounding !== "string"
  ) return null;
  return guidance;
}

function rolePrompt(roleBriefs, guidance, path, assignment) {
  const brief = roleBriefs.get(path) || { readPath: path, content: "" };
  return (
    `Read and follow the Markdown role brief at ${brief.readPath}. ` +
    `The deterministic input enumerator supplied the same content below so the Workflow script can bind the dispatched prompt to the shipped brief without reading files itself.\n\n` +
    `<role-brief path="${path}">\n${brief.content}\n</role-brief>\n\n` +
    `Judge the change against the reviewed project's checked-in commission or guidance below. ` +
    `This project intent governs whether behaviour is correct.\n\n` +
    `<project-guidance grounding="${guidance.grounding}" path="${guidance.path}">\n` +
    `${guidance.content}\n</project-guidance>\n\n` + assignment
  );
}

function clampPlan(plan, files) {
  const requested = Array.isArray(plan) ? plan : [];
  const clamps = [];
  const correctness = requested.find((unit) => unit.specialistType === "correctness");
  const fallback = {
    id: "correctness-floor",
    concern: "Correctness, edge cases, and error handling",
    scope: files.length > 0 ? files.map((file) => file.path) : ["the complete target-to-head diff"],
    specialistType: "correctness",
  };
  let ordered;
  if (correctness) {
    ordered = [correctness, ...requested.filter((unit) => unit !== correctness)];
  } else {
    clamps.push("added the mandatory correctness specialist");
    ordered = [fallback, ...requested];
  }
  if (ordered.length > MAX_PLANNED_SPECIALISTS)
    clamps.push(`capped planned specialists at ${MAX_PLANNED_SPECIALISTS}`);
  const dispatched = ordered.slice(0, MAX_PLANNED_SPECIALISTS).map((unit, index) => {
    const family = SPECIALIST_FAMILY[unit.specialistType];
    return {
      ...unit,
      kind: "planned",
      family,
      roleBrief: ROLE_BRIEFS[unit.specialistType],
      label: `specialist-${index + 1}-${unit.specialistType}-${family}`,
    };
  });
  return { requested, dispatched, clamps };
}

const findingShape = {
  type: "object",
  additionalProperties: false,
  required: ["title", "severity", "confidence", "path", "line", "explanation"],
  properties: {
    title: { type: "string" },
    severity: { type: "string", enum: ["Critical", "High", "Medium", "Low"] },
    confidence: { type: "integer", minimum: 0, maximum: 100 },
    path: { type: "string" },
    line: { type: "integer", minimum: 1 },
    explanation: { type: "string" },
  },
};

const specialistSchema = {
  type: "object",
  additionalProperties: false,
  required: ["findings"],
  properties: {
    findings: { type: "array", maxItems: MAX_FINDINGS_PER_SPECIALIST, items: findingShape },
  },
};

const explorationSchema = {
  type: "object",
  additionalProperties: false,
  required: ["files", "plan"],
  properties: {
    files: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["path", "added", "deleted"],
        properties: {
          path: { type: "string" },
          added: { type: "integer", minimum: 0 },
          deleted: { type: "integer", minimum: 0 },
        },
      },
    },
    plan: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["id", "concern", "scope", "specialistType"],
        properties: {
          id: { type: "string" },
          concern: { type: "string" },
          scope: { type: "array", minItems: 1, items: { type: "string" } },
          specialistType: { type: "string", enum: ["correctness", "security", "testing", "design"] },
        },
      },
    },
  },
};

const verifierSchema = {
  type: "object",
  additionalProperties: false,
  required: ["verdict", "confidence", "reason"],
  properties: {
    verdict: { type: "string", enum: ["upheld", "refuted"] },
    confidence: { type: "integer", minimum: 0, maximum: 100 },
    reason: { type: "string" },
  },
};

function progressRecord(runRecord, label) {
  const records = Array.isArray(runRecord && runRecord.workflowProgress)
    ? runRecord.workflowProgress
    : [];
  return records.find((record) => record && record.type === "workflow_agent" && record.label === label) || null;
}

function actualModelEvidence(runRecord, leg) {
  const record = progressRecord(runRecord, leg.label);
  const actualModel = record && record.state === "done"
    ? (record.fallbackModel || record.model || null)
    : null;
  const actualFamily = familyOf(actualModel);
  return {
    label: leg.label,
    role: leg.role,
    expectedFamily: leg.expectedFamily,
    pinnedModel: leg.pinnedModel,
    actualModel,
    actualFamily,
    state: record ? record.state : "absent",
    confirmed: Boolean(record && record.state === "done" && actualFamily === leg.expectedFamily),
  };
}

function infrastructureFailure(runRecord) {
  const raw = runRecord && runRecord.terminal;
  const terminal = typeof raw === "string"
    ? raw
    : raw && typeof raw === "object"
      ? String(raw.status || raw.state || raw.reason || "")
      : "";
  const normalised = terminal.toLowerCase();
  if (normalised.includes("oom") || normalised.includes("out-of-memory") || normalised.includes("out of memory"))
    return { kind: "oom", detail: terminal };
  if (normalised.includes("cancel") || normalised.includes("killed"))
    return { kind: "cancelled", detail: terminal };
  return null;
}

function attemptAccounting(runRecord) {
  const explicit = Array.isArray(runRecord && runRecord.attempts) ? runRecord.attempts : [];
  const progress = Array.isArray(runRecord && runRecord.workflowProgress)
    ? runRecord.workflowProgress.filter((record) => record && record.type === "workflow_agent")
    : [];
  return {
    attempts: explicit,
    legs: progress.map((record) => ({
      label: record.label || null,
      state: record.state || "unknown",
      attempt: Number.isInteger(record.attempt) ? record.attempt : 1,
      lastAttemptReason: record.lastAttemptReason || null,
    })),
  };
}

function emptyResult(target, head, occasion, fault) {
  return {
    reviewed: { target: target || null, head: head || null, occasion: occasion || null },
    status: "incomplete",
    complete: false,
    verdictsComplete: false,
    incomplete: [fault],
    infrastructureFailure: null,
    plan: { requested: [], dispatched: [], clamps: [] },
    reviewers: [],
    findings: [],
    confirmedFindings: [],
    operatorAttention: [],
    requiredModelEvidence: [],
    modelEvidence: [],
    accounting: {
      budgets: {
        exploration: { maximum: STAGE_BUDGETS.exploration, used: 0 },
        specialists: { maximum: STAGE_BUDGETS.specialists, used: 0 },
        verification: { maximum: STAGE_BUDGETS.verification, used: 0 },
        total: { maximum: STAGE_BUDGETS.total, used: 0 },
      },
      attempts: { attempts: [], legs: [] },
    },
  };
}

const input = args && typeof args === "object" ? args : null;
const target = input && typeof input.target === "string" ? input.target : null;
const head = input && typeof input.head === "string" ? input.head : null;
const occasion = input && typeof input.occasion === "string" && input.occasion !== "" ? input.occasion : null;
if (!target || !head)
  return emptyResult(target, head, occasion, "review workflow needs args {target, head, guidance, instructionBriefs}");

const roleBriefs = roleBriefsFromInput(input);
const missingBriefs = missingRoleBriefs(roleBriefs);
if (missingBriefs.length > 0)
  return emptyResult(target, head, occasion, `deterministic input omitted shipped role briefs: ${missingBriefs.join(", ")}`);
const projectGuidance = projectGuidanceFromInput(input);
if (!projectGuidance)
  return emptyResult(target, head, occasion, "deterministic input omitted reviewed-project guidance");

const legs = [];
function addLeg(label, role, expectedFamily, pinnedModel) {
  const leg = { label, role, expectedFamily, pinnedModel };
  legs.push(leg);
  return leg;
}

phase("Explore");
addLeg("exploration", "exploration", "gpt", GPT_EXPLORER_MODEL);
const exploration = await agent(
  rolePrompt(
    roleBriefs,
    projectGuidance,
    ROLE_BRIEFS.exploration,
    `Review ${target}...${head}. Return the change inventory and a partitioned review plan for Minos's planned specialists.`
  ),
  { schema: explorationSchema, model: GPT_EXPLORER_MODEL, effort: "high", label: "exploration", phase: "Explore" }
);

if (!exploration) {
  const result = emptyResult(target, head, occasion, "the exploration stage returned no result, so no specialists ran");
  result.reviewers = [{ label: "exploration", role: "exploration", status: "no-result" }];
  result.requiredModelEvidence = legs;
  result.modelEvidence = legs.map((leg) => actualModelEvidence(input.runRecord, leg));
  result.accounting.budgets.exploration.used = 1;
  result.accounting.budgets.total.used = 1;
  result.accounting.attempts = attemptAccounting(input.runRecord);
  return result;
}

const planned = clampPlan(exploration.plan, exploration.files);
const specialistUnits = planned.dispatched;
phase("Specialise");
for (const unit of specialistUnits)
  addLeg(unit.label, "specialist", unit.family, modelForFamily(unit.family));

function specialistPrompt(unit) {
  return rolePrompt(
    roleBriefs,
    projectGuidance,
    unit.roleBrief,
    `Assigned concern: ${unit.concern}\nAssigned specialist type: ${unit.specialistType}\nAssigned scope: ${unit.scope.join(", ")}\nReview only that concern and scope against ${target}...${head}.`
  );
}

const specialistResults = await parallel(
  specialistUnits.map((unit) => () =>
    agent(specialistPrompt(unit), {
      schema: specialistSchema,
      model: modelForFamily(unit.family),
      effort: "high",
      label: unit.label,
      phase: "Specialise",
    })
  )
);

const reviewerStates = [];
const proposed = [];
specialistUnits.forEach((unit, unitIndex) => {
  const result = specialistResults[unitIndex];
  reviewerStates.push({
    label: unit.label,
    role: "specialist",
    kind: unit.kind,
    concern: unit.concern,
    scope: unit.scope,
    family: unit.family,
    pinnedModel: modelForFamily(unit.family),
    status: result ? "done" : "no-result",
  });
  if (!result) return;
  result.findings.slice(0, MAX_FINDINGS_PER_SPECIALIST).forEach((finding, findingIndex) => {
    proposed.push({ unit, unitIndex, finding, findingIndex });
  });
});

phase("Verify");
for (const item of proposed) {
  const expectedFamily = item.unit.family === "gpt" ? "claude" : "gpt";
  item.verifyLabel = `verify-${item.unitIndex + 1}-${item.findingIndex + 1}-${expectedFamily}`;
  item.expectedFamily = expectedFamily;
  addLeg(item.verifyLabel, "verifier", expectedFamily, modelForFamily(expectedFamily));
}

const verifierResults = await parallel(
  proposed.map((item) => () =>
    agent(
      rolePrompt(
        roleBriefs,
        projectGuidance,
        ROLE_BRIEFS.verifier,
        `Try to disprove this proposed finding against ${target}...${head} and the cited code.\n` +
          `Proposing specialist: ${item.unit.label}\nFinding data: ${JSON.stringify(item.finding)}`
      ),
      {
        schema: verifierSchema,
        model: modelForFamily(item.expectedFamily),
        effort: "high",
        label: item.verifyLabel,
        phase: "Verify",
      }
    )
  )
);

const modelEvidence = legs.map((leg) => actualModelEvidence(input.runRecord, leg));
const evidenceByLabel = new Map(modelEvidence.map((evidence) => [evidence.label, evidence]));
const findings = proposed.map((item, index) => {
  const check = verifierResults[index];
  const reviewEvidence = evidenceByLabel.get(item.unit.label);
  const verifyEvidence = evidenceByLabel.get(item.verifyLabel);
  let verdict = "no-verdict";
  let verification = "verifier returned no result";
  if (check && reviewEvidence.confirmed && verifyEvidence.confirmed) {
    verdict = check.verdict === "upheld" ? "confirmed" : "refuted";
    verification = check.reason;
  } else if (check) {
    const failed = [reviewEvidence, verifyEvidence].filter((evidence) => !evidence.confirmed);
    verification = `actual-model evidence did not confirm ${failed.map((evidence) => `${evidence.label} as ${evidence.expectedFamily}`).join(" and ")}`;
  }
  const combinedConfidence = check
    ? Math.round((item.finding.confidence + check.confidence) / 2)
    : null;
  const operatorAttention = verdict === "confirmed" && combinedConfidence < LOW_COMBINED_CONFIDENCE;
  return {
    id: `${item.unit.label}:${item.findingIndex + 1}`,
    source: item.unit.concern,
    ...item.finding,
    verifierConfidence: check ? check.confidence : null,
    combinedConfidence,
    operatorAttention,
    verdict,
    verification,
    review: reviewEvidence,
    verify: verifyEvidence,
  };
});

const incomplete = [];
for (const state of reviewerStates)
  if (state.status === "no-result") incomplete.push(`specialist ${state.label} returned no result`);
for (const evidence of modelEvidence)
  if (!evidence.confirmed)
    incomplete.push(`actual model for ${evidence.label} was not confirmed as ${evidence.expectedFamily}`);
for (const finding of findings)
  if (finding.verdict === "no-verdict")
    incomplete.push(`finding "${finding.title}" has no complete verdict (${finding.verification})`);

const infrastructure = infrastructureFailure(input.runRecord);
const complete = incomplete.length === 0 && !infrastructure;
const status = infrastructure ? "infrastructure-failure" : complete ? "complete" : "incomplete";
const confirmedFindings = findings.filter((finding) => finding.verdict === "confirmed");
const operatorAttention = confirmedFindings
  .filter((finding) => finding.operatorAttention)
  .map((finding) => ({
    finding: finding.id,
    title: finding.title,
    combinedConfidence: finding.combinedConfidence,
    threshold: LOW_COMBINED_CONFIDENCE,
  }));
const used = legs.length;

return {
  reviewed: { target, head, occasion },
  status,
  complete,
  verdictsComplete: complete,
  incomplete,
  infrastructureFailure: infrastructure,
  plan: {
    requested: planned.requested,
    dispatched: planned.dispatched.map((unit) => ({
      id: unit.id,
      label: unit.label,
      concern: unit.concern,
      scope: unit.scope,
      specialistType: unit.specialistType,
      family: unit.family,
      roleBrief: unit.roleBrief,
    })),
    clamps: planned.clamps,
  },
  reviewers: reviewerStates,
  findings,
  confirmedFindings,
  operatorAttention,
  requiredModelEvidence: legs,
  modelEvidence,
  accounting: {
    budgets: {
      exploration: { maximum: STAGE_BUDGETS.exploration, used: 1 },
      specialists: { maximum: STAGE_BUDGETS.specialists, used: specialistUnits.length },
      verification: { maximum: STAGE_BUDGETS.verification, used: proposed.length },
      total: { maximum: STAGE_BUDGETS.total, used },
    },
    attempts: attemptAccounting(input.runRecord),
  },
};
