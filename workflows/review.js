export const meta = {
  name: "minos-review",
  description: "Planned pull-request review with cross-family verification",
  phases: [
    { title: "Explore", detail: "produce a scoped, schema-validated review plan" },
    { title: "Specialise", detail: "run the planned concern specialists" },
    { title: "Verify", detail: "opposite-family verification of each proposed finding" },
  ],
};

const GPT_SPECIALIST_MODEL = "gpt-5.6-sol";
const GPT_EXPLORER_MODEL = "gpt-5.6-terra";
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

function modelForFamily(family) {
  return family === "gpt" ? GPT_SPECIALIST_MODEL : CLAUDE_MODEL;
}

function engineForFamily(family) {
  return family === "gpt" ? "codex" : "claude";
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
    `The deterministic input enumerator supplied the same content below so this workflow can bind the dispatched prompt to the shipped brief without reading files itself.\n\n` +
    `<role-brief path="${path}">\n${brief.content}\n</role-brief>\n\n` +
    `Judge the change against the reviewed project's checked-in commission or guidance below. ` +
    `This project intent governs whether behaviour is correct.\n\n` +
    `<project-guidance grounding="${guidance.grounding}" path="${guidance.path}">\n` +
    `${guidance.content}\n</project-guidance>\n\n${assignment}`
  );
}

function normalisePlan(plan, files) {
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
  if (correctness) ordered = [correctness, ...requested.filter((unit) => unit !== correctness)];
  else {
    clamps.push("added the mandatory correctness specialist");
    ordered = [fallback, ...requested];
  }
  const dispatched = ordered.map((unit, index) => {
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

const applicabilityShape = {
  type: "object",
  additionalProperties: false,
  required: ["status", "reason"],
  properties: {
    status: { type: "string", enum: ["applicable", "inapplicable"] },
    reason: { type: "string" },
  },
};

const specialistSchema = {
  type: "object",
  additionalProperties: false,
  required: ["applicability", "findings"],
  properties: {
    applicability: applicabilityShape,
    findings: { type: "array", items: findingShape },
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

const input = args && typeof args === "object" && !Array.isArray(args) ? args : null;
const target = input && typeof input.target === "string" ? input.target : null;
const head = input && typeof input.head === "string" ? input.head : null;
const occasion = input && typeof input.occasion === "string" && input.occasion !== "" ? input.occasion : null;
if (!target || !head)
  throw new Error("review workflow needs args {target, head, guidance, instructionBriefs}");

const roleBriefs = roleBriefsFromInput(input);
const missingBriefs = missingRoleBriefs(roleBriefs);
if (missingBriefs.length > 0)
  throw new Error(`deterministic input omitted shipped role briefs: ${missingBriefs.join(", ")}`);
const projectGuidance = projectGuidanceFromInput(input);
if (!projectGuidance) throw new Error("deterministic input omitted reviewed-project guidance");

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
  {
    engine: "codex",
    schema: explorationSchema,
    model: GPT_EXPLORER_MODEL,
    effort: "high",
    label: "exploration",
    phase: "Explore",
  }
);

if (!exploration) {
  return {
    reviewed: { target, head, occasion },
    stage: "present",
    requiredModelEvidence: legs,
    proposedFindings: [],
    briefs: [],
    dispatches: [],
    reviewers: [{ label: "exploration", role: "exploration", status: "no-result" }],
  };
}

const planned = normalisePlan(exploration.plan, exploration.files);
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
      engine: engineForFamily(unit.family),
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
const briefs = [];
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
  if (result.applicability.status === "inapplicable") {
    briefs.push({
      brief: unit.roleBrief,
      title: unit.concern,
      status: "skipped",
      skipKind: "inapplicable",
      reason: result.applicability.reason,
    });
    return;
  }
  result.findings.forEach((finding, findingIndex) => {
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
        engine: engineForFamily(item.expectedFamily),
        schema: verifierSchema,
        model: modelForFamily(item.expectedFamily),
        effort: "high",
        label: item.verifyLabel,
        phase: "Verify",
      }
    )
  )
);

const proposedFindings = proposed.map((item, index) => ({
  id: `${item.unit.label}:${item.findingIndex + 1}`,
  source: item.unit.concern,
  ...item.finding,
  proposingLabel: item.unit.label,
  verifyLabel: item.verifyLabel,
  rawVerifier: verifierResults[index] || null,
}));

return {
  reviewed: { target, head, occasion },
  stage: "present",
  requiredModelEvidence: legs,
  proposedFindings,
  briefs,
  dispatches: planned.dispatched.map((unit) => ({
    id: unit.id,
    label: unit.label,
    concern: unit.concern,
    scope: unit.scope,
    specialistType: unit.specialistType,
    family: unit.family,
    roleBrief: unit.roleBrief,
  })),
  reviewers: reviewerStates,
};
