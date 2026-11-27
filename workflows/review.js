export const meta = {
  name: "minos-review",
  description: "Planned pull-request review with cross-family verification",
  phases: [
    { title: "Explore", detail: "produce a scoped, schema-validated review plan" },
    { title: "Specialise", detail: "run the planned concern specialists" },
    { title: "Merge", detail: "group the proposed findings one fix would resolve" },
    { title: "Verify", detail: "opposite-family verification of each merged finding" },
  ],
};

// A workflow script cannot import statically, so what this one shares with
// review-briefs.js is loaded from beside it: the checks of the input each builder
// attached, beside the builder's own module; the prompt slots; and the
// proposing, merge and verification stages every review runs.
const { contractsFromInput } = await import("./review-contracts.mjs");
const { routingFromInput } = await import("./role-routing.mjs");
const { projectGuidanceFromInput, projectGuidanceSection } = await import("./orientation-guidance.mjs");
const { pullRequestFromInput, pullRequestSection } = await import("./pull-request-description.mjs");
const { promptFits, fillPrompt } = await import("./review-prompts.mjs");
const { reviewStages, proposalsOf, subjectOf, proposedFindingOf, uncheckedFindingOf } = await import("./review-stages.mjs");

function concernsFromInput(input) {
  const concerns = input && input.concerns;
  if (!Array.isArray(concerns) || concerns.length === 0) return null;
  const names = new Set();
  for (const concern of concerns) {
    if (
      !concern || typeof concern.name !== "string" || concern.name === "" ||
      typeof concern.summary !== "string" || concern.summary === "" ||
      names.has(concern.name)
    ) return null;
    names.add(concern.name);
  }
  return concerns;
}

function promptsFromInput(input, concerns) {
  const prompts = input && input.prompts;
  if (!prompts || typeof prompts !== "object" || Array.isArray(prompts)) return null;
  if (!prompts.specialist || typeof prompts.specialist !== "object" || Array.isArray(prompts.specialist)) return null;
  if (
    !promptFits(prompts.exploration, "exploration") ||
    !promptFits(prompts.merge, "merge") ||
    !promptFits(prompts.verifier, "verifier") ||
    !concerns.every((concern) => promptFits(prompts.specialist[concern.name], "specialist"))
  ) return null;
  return prompts;
}

function concernLine(concern) {
  return `- \`${concern.name}\`: ${concern.summary}`;
}

function validExplorationResult(result) {
  if (
    !result || typeof result !== "object" || Array.isArray(result) ||
    !result.applicability || typeof result.applicability.reason !== "string" ||
    !Array.isArray(result.files) || !Array.isArray(result.plan)
  ) return false;
  const changedPaths = new Set();
  for (const file of result.files) {
    if (
      !file || typeof file !== "object" || Array.isArray(file) ||
      typeof file.path !== "string" || file.path === "" ||
      !Number.isInteger(file.added) || file.added < 0 ||
      !Number.isInteger(file.deleted) || file.deleted < 0
    ) return false;
    changedPaths.add(file.path);
  }
  return result.plan.every((unit) =>
    unit && typeof unit === "object" && !Array.isArray(unit) &&
    typeof unit.id === "string" && unit.id !== "" &&
    typeof unit.concern === "string" && unit.concern !== "" &&
    concerns.some((concern) => concern.name === unit.specialistType) &&
    Array.isArray(unit.scope) && unit.scope.length > 0 &&
    unit.scope.every((scopePath) => typeof scopePath === "string" && changedPaths.has(scopePath))
  );
}

const input = args && typeof args === "object" && !Array.isArray(args) ? args : null;
const target = input && typeof input.target === "string" ? input.target : null;
const head = input && typeof input.head === "string" ? input.head : null;
const occasion = input && typeof input.occasion === "string" && input.occasion !== "" ? input.occasion : null;
if (!target || !head)
  throw new Error("review workflow needs args {target, head, guidance, concerns, prompts, contracts}");
const contracts = contractsFromInput(input);
if (!contracts) throw new Error("deterministic input omitted or malformed the review contracts");
const concerns = concernsFromInput(input);
if (!concerns) throw new Error("deterministic input omitted or malformed the review concerns");
const prompts = promptsFromInput(input, concerns);
if (!prompts) throw new Error("deterministic input omitted or malformed the review prompts");
const projectGuidance = projectGuidanceFromInput(input);
if (!projectGuidance) throw new Error("deterministic input omitted reviewed-project guidance");
const routing = routingFromInput(input);
if (!routing) throw new Error("deterministic input omitted the role routing");
const reviewedChangeValues = {
  target,
  head,
  "project-documents": projectGuidanceSection(projectGuidance),
  "pull-request": pullRequestSection(pullRequestFromInput(input)),
};

const {
  incomplete, legs, outOfScopeObservations,
  addLeg,
  proposeUnits, mergeProposed, verifySubjects,
} = reviewStages({ agent, parallel, routing, contracts, prompts, reviewedChangeValues });

function envelope(fields) {
  return {
    reviewed: { target, head, occasion },
    stage: "present",
    ...(incomplete.length > 0 ? { incomplete } : {}),
    requiredModelEvidence: legs,
    proposedFindings: [],
    uncheckedFindings: [],
    outOfScopeObservations,
    briefs: [],
    misconfigurations: [],
    dispatches: [],
    reviewers: [],
    ...fields,
  };
}

function incompleteExploration(reason) {
  if (reason) incomplete.push(reason);
  return envelope({
    reviewers: [{ label: "exploration", role: "exploration", status: "no-result", ...(reason ? { reason } : {}) }],
  });
}

phase("Explore");
addLeg("exploration", "exploration", routing.exploration.model);
const explorationResult = await agent(
  fillPrompt(prompts.exploration, {
    ...reviewedChangeValues,
    concerns: concerns.map(concernLine).join("\n"),
  }),
  {
    engine: routing.exploration.engine,
    schema: contracts.explorationSchema,
    model: routing.exploration.model,
    effort: routing.exploration.effort,
    strip: ["skills", "agents"],
    label: "exploration",
    phase: "Explore",
  }
);

if (!explorationResult) return incompleteExploration();
if (!validExplorationResult(explorationResult))
  return incompleteExploration("exploration returned a malformed review plan");

const exploration = {
  ...explorationResult,
  applicability: {
    status: explorationResult.plan.length === 0 ? "inapplicable" : "applicable",
    reason: explorationResult.applicability.reason,
  },
};
const specialistUnits = exploration.plan.map((unit, index) => ({
  ...unit,
  kind: "planned",
  family: routing.proposer.engine,
  roleBrief: `workflows/review-briefs/concerns/${unit.specialistType}.md`,
  label: `specialist-${index + 1}-${unit.specialistType}-${routing.proposer.engine}`,
}));

phase("Specialise");

// A planned unit's focus sentence and its scope paths, as the specialist
// prompt shows its own unit and each other unit of its concern.
function plannedFocus(unit) {
  return `${unit.concern}\n\n${unit.scope.map((path) => `- \`${path}\``).join("\n")}`;
}

function specialistPrompt(unit) {
  return fillPrompt(prompts.specialist[unit.specialistType], {
    ...reviewedChangeValues,
    "concern-name": unit.specialistType,
    "other-concerns": concerns.filter((concern) => concern.name !== unit.specialistType).map(concernLine).join("\n"),
    "planned-focus": plannedFocus(unit),
    "same-concern-units": specialistUnits
      .filter((other) => other !== unit && other.specialistType === unit.specialistType)
      .map(plannedFocus)
      .join("\n\n"),
  });
}

const specialistResults = await proposeUnits(
  specialistUnits.map((unit) => ({ ...unit, prompt: specialistPrompt(unit), source: unit.concern })),
  "Specialise",
);

const reviewerStates = [];
const proposed = [];
const briefs = [];
for (const { unit, completed, output: result } of specialistResults) {
  reviewerStates.push({
    label: unit.label,
    role: "specialist",
    kind: unit.kind,
    concern: unit.concern,
    scope: unit.scope,
    family: unit.family,
    pinnedModel: routing.proposer.model,
    status: completed ? "done" : "no-result",
  });
  if (!result) continue;
  let count = 0;
  const propose = (findings, outsideConcern) => proposed.push(...proposalsOf(findings, () => `${unit.label}:${count += 1}`, {
    source: unit.concern,
    reportedBy: unit.specialistType,
    outsideConcern,
    proposingLabel: unit.label,
  }));
  // A specialist with nothing of its own kind to judge may still have met
  // another concern's problem on the way.
  if (result.applicability.status === "inapplicable") {
    briefs.push({
      brief: unit.roleBrief,
      title: unit.concern,
      status: "skipped",
      skipKind: "inapplicable",
      reason: result.applicability.reason,
    });
  } else {
    propose(result.findings, false);
  }
  propose(result.outsideConcernFindings, true);
}

const dispatches = specialistUnits.map((unit) => ({
  id: unit.id,
  label: unit.label,
  concern: unit.concern,
  scope: unit.scope,
  specialistType: unit.specialistType,
  family: unit.family,
  roleBrief: unit.roleBrief,
}));

phase("Merge");
const groups = await mergeProposed(proposed, "merge", "Merge");
if (!groups) return envelope({ exploration, briefs, dispatches, reviewers: reviewerStates });

phase("Verify");
const { verified, unchecked } = await verifySubjects(groups.map(subjectOf), "verify", "merge", "Verify");

return envelope({
  exploration,
  proposedFindings: verified.map(proposedFindingOf),
  uncheckedFindings: unchecked.map(uncheckedFindingOf),
  briefs,
  dispatches,
  reviewers: reviewerStates,
});
