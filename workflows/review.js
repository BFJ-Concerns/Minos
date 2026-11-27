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
// review-briefs.js is loaded from beside it: the prompt slots, and the merge
// and verification stages every review runs on its proposed findings.
const { promptFits, fillPrompt } = await import("./review-prompts.mjs");
const { reviewStages, subjectOf, proposedFindingOf, uncheckedFindingOf } = await import("./review-stages.mjs");

// The input checks below are the same in review.js and review-briefs.js.

// The finding, merge and verifier contracts arrive as `contracts`, built
// once in workflows/review-contracts.mjs by the input builder; this script
// keeps no copy, and a missing or malformed set fails the workflow before
// dispatch.
function contractsFromInput(input) {
  const contracts = input && input.contracts;
  if (!contracts || typeof contracts !== "object" || Array.isArray(contracts)) return null;
  if (!Number.isInteger(contracts.verifierBatchSize) || contracts.verifierBatchSize < 1) return null;
  if (!Number.isInteger(contracts.verificationRounds) || contracts.verificationRounds < 1) return null;
  for (const name of ["mergeFindingFields", "verifierFindingFields"])
    if (!Array.isArray(contracts[name]) || !contracts[name].includes("id")) return null;
  for (const name of [
    "explorationSchema",
    "findingShape",
    "outOfScopeObservationShape",
    "applicabilityShape",
    "specialistSchema",
    "mergeSchema",
    "verifierVerdictShape",
    "verifierSchema",
  ]) {
    const shape = contracts[name];
    if (
      !shape || typeof shape !== "object" || Array.isArray(shape) ||
      shape.type !== "object" ||
      !Array.isArray(shape.required) ||
      !shape.properties || typeof shape.properties !== "object" || Array.isArray(shape.properties)
    ) return null;
  }
  return contracts;
}

const ROUTING_ROLES = ["exploration", "proposer", "verifier", "engagement-gate", "brief-planner"];
const ROUTING_ENGINES = ["claude", "codex"];

// The resolved routing the input builder attached: every role's engine,
// model and effort, already defaulted for what the deployment provisioned.
// Nothing here is fixed in source; a missing or malformed table fails the
// workflow before any dispatch.
function routingFromInput(input) {
  const routing = input && input.routing;
  if (!routing || typeof routing !== "object" || Array.isArray(routing)) return null;
  for (const role of ROUTING_ROLES) {
    const entry = routing[role];
    if (
      !entry ||
      !ROUTING_ENGINES.includes(entry.engine) ||
      typeof entry.model !== "string" || entry.model === "" ||
      typeof entry.effort !== "string" || entry.effort === ""
    ) return null;
  }
  return routing;
}

function projectGuidanceFromInput(input) {
  const guidance = input && input.guidance;
  if (!Array.isArray(guidance) || guidance.length === 0) return null;
  for (const entry of guidance) {
    if (
      !entry ||
      typeof entry.path !== "string" ||
      entry.path === "" ||
      (entry.repository !== null && (typeof entry.repository !== "string" || entry.repository === "")) ||
      (entry.origin !== "configured" && entry.origin !== "checked-in") ||
      typeof entry.content !== "string" ||
      entry.content.trim() === ""
    ) return null;
  }
  return guidance;
}

// One block per guidance document, in the configured order: the reviewed
// project's declared intent, as the repository owner names it.
function projectGuidanceSection(guidance) {
  return guidance.map((entry) =>
    `<project-guidance origin="${entry.origin}"${entry.repository === null ? "" : ` repository="${entry.repository}"`} path="${entry.path}">\n` +
    `${entry.content}\n</project-guidance>`
  ).join("\n\n");
}

function pullRequestFromInput(input) {
  const pullRequest = input && input.pullRequest;
  if (
    !pullRequest ||
    typeof pullRequest.title !== "string" ||
    typeof pullRequest.body !== "string" ||
    (pullRequest.title.trim() === "" && pullRequest.body.trim() === "")
  ) return null;
  return { title: pullRequest.title, body: pullRequest.body };
}

function pullRequestSection(pullRequest) {
  if (!pullRequest) return "";
  return `<pull-request-description title=${JSON.stringify(pullRequest.title)}>\n${pullRequest.body}\n</pull-request-description>`;
}

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
  addLeg, legOutput, recordObservations,
  mergeProposed, verifySubjects,
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
for (const unit of specialistUnits)
  addLeg(unit.label, "specialist", routing.proposer.model);

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

const specialistResults = await parallel(
  specialistUnits.map((unit) => () =>
    agent(specialistPrompt(unit), {
      engine: routing.proposer.engine,
      schema: contracts.specialistSchema,
      model: routing.proposer.model,
      effort: routing.proposer.effort,
      strip: ["skills", "agents"],
      identity: true,
      label: unit.label,
      phase: "Specialise",
    })
  )
);

const reviewerStates = [];
const proposed = [];
const briefs = [];
specialistUnits.forEach((unit, unitIndex) => {
  const settlement = specialistResults[unitIndex];
  const result = legOutput(settlement, unit.label);
  reviewerStates.push({
    label: unit.label,
    role: "specialist",
    kind: unit.kind,
    concern: unit.concern,
    scope: unit.scope,
    family: unit.family,
    pinnedModel: routing.proposer.model,
    status: settlement && settlement.failure === null ? "done" : "no-result",
  });
  if (!result) return;
  recordObservations(result.outOfScopeObservations, unit.concern, unit.label);
  let count = 0;
  const propose = (findings, outsideConcern) =>
    (Array.isArray(findings) ? findings : []).forEach((finding) => {
      count += 1;
      proposed.push({
        id: `${unit.label}:${count}`,
        finding,
        source: unit.concern,
        reportedBy: unit.specialistType,
        outsideConcern,
        proposingLabel: unit.label,
      });
    });
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
});

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
