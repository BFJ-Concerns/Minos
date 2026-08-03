export const meta = {
  name: "minos-review",
  description: "Planned pull-request review with cross-family verification",
  phases: [
    { title: "Explore", detail: "produce a scoped, schema-validated review plan" },
    { title: "Specialise", detail: "run the planned concern specialists" },
    { title: "Verify", detail: "opposite-family verification of each proposed finding" },
  ],
};

const GPT_EXPLORER_MODEL = "gpt-5.6-terra";
const PROPOSER_MODEL = "gpt-5.6-terra";
const VERIFIER_MODEL = "claude-opus-5";
const MAX_FINDINGS_PER_VERIFIER = 6;
const MAX_ORIENTATION_PACKET_BYTES = 32 * 1024;

const ROLE_BRIEFS = {
  exploration: "workflows/review-briefs/exploration.md",
  correctness: "workflows/review-briefs/correctness.md",
  security: "workflows/review-briefs/security.md",
  testing: "workflows/review-briefs/testing.md",
  design: "workflows/review-briefs/design.md",
  verifier: "workflows/review-briefs/verifier.md",
};

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
    return {
      ...unit,
      kind: "planned",
      family: "gpt",
      roleBrief: ROLE_BRIEFS[unit.specialistType],
      label: `specialist-${index + 1}-${unit.specialistType}-gpt`,
    };
  });
  return { requested, dispatched, clamps };
}

// The Ensemble sandbox exposes no Buffer or TextEncoder global, so byte
// lengths must be computed in plain JavaScript.
function utf8ByteLength(text) {
  let bytes = 0;
  for (const character of text) {
    const code = character.codePointAt(0);
    if (code <= 0x7f) bytes += 1;
    else if (code <= 0x7ff) bytes += 2;
    else if (code <= 0xffff) bytes += 3;
    else bytes += 4;
  }
  return bytes;
}

function orientationPacket(target, head, files, units) {
  const full = {
    commitRange: `${target}...${head}`,
    changedFiles: files.map(({ path, added, deleted }) => ({ path, added, deleted })),
    ownership: units.map(({ id, concern, scope, specialistType }) => ({
      unit: id,
      concern,
      scope,
      specialistType,
    })),
  };
  const serialised = JSON.stringify(full);
  if (utf8ByteLength(serialised) <= MAX_ORIENTATION_PACKET_BYTES)
    return serialised;

  const ownership = units.map(({ id, scope }) => ({
    unit: id,
    scopeCount: scope.length,
  }));
  function candidate(fileCount, ownershipCount) {
    const changedFiles = full.changedFiles.slice(0, fileCount);
    if (fileCount < full.changedFiles.length)
      changedFiles.push(`+${full.changedFiles.length - fileCount} more files`);
    const ownershipSummary = ownership.slice(0, ownershipCount);
    if (ownershipCount < ownership.length)
      ownershipSummary.push(`+${ownership.length - ownershipCount} more units`);
    return JSON.stringify({
      commitRange: full.commitRange,
      changedFiles,
      ownership: ownershipSummary,
    });
  }
  function largestFittingPrefix(maximum, build) {
    let low = 0;
    let high = maximum;
    let best = 0;
    while (low <= high) {
      const middle = Math.floor((low + high) / 2);
      if (utf8ByteLength(build(middle)) <= MAX_ORIENTATION_PACKET_BYTES) {
        best = middle;
        low = middle + 1;
      } else {
        high = middle - 1;
      }
    }
    return best;
  }

  const ownershipCount = largestFittingPrefix(
    ownership.length,
    (count) => candidate(0, count),
  );
  const fileCount = largestFittingPrefix(
    full.changedFiles.length,
    (count) => candidate(count, ownershipCount),
  );
  return candidate(fileCount, ownershipCount);
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

const outOfScopeObservationShape = {
  type: "object",
  additionalProperties: false,
  required: ["title", "path", "line", "explanation"],
  properties: {
    title: { type: "string" },
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
    outOfScopeObservations: { type: "array", items: outOfScopeObservationShape },
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

const verifierVerdictShape = {
  type: "object",
  additionalProperties: false,
  required: ["findingId", "verdict", "confidence", "reason"],
  properties: {
    findingId: { type: "string" },
    verdict: { type: "string", enum: ["upheld", "refuted"] },
    confidence: { type: "integer", minimum: 0, maximum: 100 },
    reason: { type: "string" },
  },
};

const verifierSchema = {
  type: "object",
  additionalProperties: false,
  required: ["verdicts"],
  properties: {
    verdicts: { type: "array", items: verifierVerdictShape },
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
const priorFindings = input && input.priorFindings;
const validPriorEntry = (entry) => Boolean(
  entry && typeof entry === "object" && !Array.isArray(entry) &&
  typeof entry.key === "string" && entry.key !== "" &&
  entry.finding && typeof entry.finding === "object" && !Array.isArray(entry.finding) &&
  typeof entry.finding.title === "string" &&
  typeof entry.finding.path === "string" &&
  Number.isInteger(entry.finding.line) && entry.finding.line >= 1 &&
  typeof entry.finding.explanation === "string"
);
if (!priorFindings || typeof priorFindings !== "object" || Array.isArray(priorFindings) ||
    !Array.isArray(priorFindings.confirmedFixed) ||
    !Array.isArray(priorFindings.confirmedUnfixed) ||
    !priorFindings.confirmedFixed.every(validPriorEntry) ||
    !priorFindings.confirmedUnfixed.every(validPriorEntry))
  throw new Error("deterministic input needs priorFindings with valid confirmedFixed and confirmedUnfixed arrays");

const legs = [];
function addLeg(label, role, pinnedModel, findingIds = null) {
  const leg = { label, role, pinnedModel };
  if (findingIds) leg.findingIds = findingIds;
  legs.push(leg);
  return leg;
}

phase("Explore");
addLeg("exploration", "exploration", GPT_EXPLORER_MODEL);
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
    outOfScopeObservations: [],
    briefs: [],
    misconfigurations: [],
    dispatches: [],
    reviewers: [{ label: "exploration", role: "exploration", status: "no-result" }],
  };
}

const planned = normalisePlan(exploration.plan, exploration.files);
const specialistUnits = planned.dispatched;
const orientation = orientationPacket(target, head, exploration.files, specialistUnits);
phase("Specialise");
for (const unit of specialistUnits)
  addLeg(unit.label, "specialist", PROPOSER_MODEL);

function specialistPrompt(unit) {
  const hasPriorFindings = priorFindings.confirmedFixed.length > 0 ||
    priorFindings.confirmedUnfixed.length > 0;
  const suppressionContext = hasPriorFindings
    ? `\nPrior findings from earlier rounds: ${JSON.stringify(priorFindings)}\n` +
      "Do not present a substantially equivalent, already-dispositioned finding as a fresh discovery. " +
      "You must judge equivalence from the current code and evidence. A regression of a repair, a materially different nearby defect, or a genuinely new finding remains reportable."
    : "";
  return rolePrompt(
    roleBriefs,
    projectGuidance,
    unit.roleBrief,
    `Orientation packet: ${orientation}\n` +
      `Assigned concern: ${unit.concern}\nAssigned specialist type: ${unit.specialistType}\nAssigned scope: ${unit.scope.join(", ")}\nReview only that concern and scope against ${target}...${head}.` +
      suppressionContext
  );
}

const specialistResults = await parallel(
  specialistUnits.map((unit) => () =>
    agent(specialistPrompt(unit), {
      engine: "codex",
      schema: specialistSchema,
      model: PROPOSER_MODEL,
      effort: "medium",
      label: unit.label,
      phase: "Specialise",
    })
  )
);

const reviewerStates = [];
const proposed = [];
const outOfScopeObservations = [];
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
    pinnedModel: PROPOSER_MODEL,
    status: result ? "done" : "no-result",
  });
  if (!result) return;
  const observations = Array.isArray(result.outOfScopeObservations)
    ? result.outOfScopeObservations
    : [];
  observations.forEach((observation, observationIndex) => {
    outOfScopeObservations.push({
      id: `${unit.label}:observation:${observationIndex + 1}`,
      source: unit.concern,
      ...observation,
      observingLabel: unit.label,
      verified: false,
    });
  });
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
    proposed.push({
      id: `${unit.label}:${findingIndex + 1}`,
      unit,
      unitIndex,
      finding,
      findingIndex,
    });
  });
});

phase("Verify");
const verifierGroups = [];
for (const unit of specialistUnits) {
  const unitFindings = proposed.filter((item) => item.unit === unit);
  for (let offset = 0; offset < unitFindings.length; offset += MAX_FINDINGS_PER_VERIFIER) {
    const items = unitFindings.slice(offset, offset + MAX_FINDINGS_PER_VERIFIER);
    const groupIndex = Math.floor(offset / MAX_FINDINGS_PER_VERIFIER) + 1;
    const label = `verify-${items[0].unitIndex + 1}-${groupIndex}-claude`;
    const findingIds = items.map((item) => item.id);
    const group = { items, label, findingIds };
    verifierGroups.push(group);
    items.forEach((item) => { item.verifyLabel = label; });
    addLeg(label, "verifier", VERIFIER_MODEL, findingIds);
  }
}

const verifierResults = await parallel(
  verifierGroups.map((group) => () =>
    agent(
      rolePrompt(
        roleBriefs,
        projectGuidance,
        ROLE_BRIEFS.verifier,
        `Try to disprove each proposed finding against ${target}...${head} and the cited code.\n` +
          `Proposing specialist: ${group.items[0].unit.label}\n` +
          `Findings: ${JSON.stringify(group.items.map((item) => ({ id: item.id, ...item.finding })))}`
      ),
      {
        engine: "claude",
        schema: verifierSchema,
        model: VERIFIER_MODEL,
        effort: "medium",
        label: group.label,
        phase: "Verify",
      }
    )
  )
);

const verifierByFinding = new Map(proposed.map((item) => [item.id, null]));
verifierGroups.forEach((group, groupIndex) => {
  const response = verifierResults[groupIndex];
  if (!response || !Array.isArray(response.verdicts)) return;
  const expected = new Set(group.findingIds);
  if (response.verdicts.some((verdict) => !expected.has(verdict && verdict.findingId))) return;
  const verdictsById = new Map();
  for (const verdict of response.verdicts) {
    const existing = verdictsById.get(verdict.findingId) || [];
    existing.push(verdict);
    verdictsById.set(verdict.findingId, existing);
  }
  for (const findingId of group.findingIds) {
    const matches = verdictsById.get(findingId) || [];
    if (matches.length !== 1) continue;
    const { findingId: omittedFindingId, ...rawVerifier } = matches[0];
    verifierByFinding.set(findingId, rawVerifier);
  }
});

const proposedFindings = proposed.map((item) => ({
  id: item.id,
  source: item.unit.concern,
  ...item.finding,
  proposingLabel: item.unit.label,
  verifyLabel: item.verifyLabel,
  rawVerifier: verifierByFinding.get(item.id),
}));

return {
  reviewed: { target, head, occasion },
  stage: "present",
  requiredModelEvidence: legs,
  proposedFindings,
  outOfScopeObservations,
  briefs,
  misconfigurations: [],
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
