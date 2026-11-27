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

// The input checks, prompt filling, merge and verification below are the
// same in review.js and review-briefs.js: a workflow script imports
// nothing, so each carries them.

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

// A prompt is its shipped file with the file slots already filled by the
// input builder (workflows/review-prompts.mjs); the run slots are filled
// here, in one pass, so that no value is itself read for slots. A prompt
// carrying a slot its stage does not fill fails the workflow before
// dispatch.
const SLOT = /\{\{([a-z-]+)\}\}/g;
const REVIEWED_CHANGE_SLOTS = ["target", "head", "project-documents", "pull-request"];
const STAGE_SLOTS = {
  exploration: [...REVIEWED_CHANGE_SLOTS, "concerns"],
  specialist: [...REVIEWED_CHANGE_SLOTS, "concern-name", "other-concerns", "planned-focus"],
  repository: [...REVIEWED_CHANGE_SLOTS, "brief-path", "brief", "assignment"],
  merge: ["target", "head", "findings"],
  verifier: [...REVIEWED_CHANGE_SLOTS, "findings"],
};

function promptFits(text, stage) {
  return typeof text === "string" && text.trim() !== "" &&
    [...text.matchAll(SLOT)].every((match) => STAGE_SLOTS[stage].includes(match[1]));
}

function fillPrompt(template, values) {
  return template.replace(SLOT, (whole, name) => {
    if (!Object.hasOwn(values, name)) throw new Error(`prompt slot ${whole} has no value`);
    return values[name];
  });
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

function pick(record, fields) {
  return Object.fromEntries(fields.filter((field) => record[field] !== undefined).map((field) => [field, record[field]]));
}

function legOutput(result, label) {
  if (!result || result.failure !== null) {
    incomplete.push(`agent ${result?.label || label} failed: ${result?.failure?.message || "no failure account returned"}`);
    return null;
  }
  if (result.output === null)
    incomplete.push(`agent ${result.label || label} answered without usable output`);
  return result.output;
}

function addLeg(label, role, pinnedModel, findingIds = null) {
  const leg = { label, role, pinnedModel };
  if (findingIds) leg.findingIds = findingIds;
  legs.push(leg);
}

function recordObservations(observations, source, label) {
  (Array.isArray(observations) ? observations : []).forEach((observation, index) => {
    outOfScopeObservations.push({
      id: `${label}:observation:${index + 1}`,
      source,
      ...observation,
      observingLabel: label,
      verified: false,
    });
  });
}

// The merge leg groups the proposed findings one fix would resolve, so
// that each fault is verified and published once. Fewer than two findings
// need no merge. A grouping that does not place every finding in exactly
// one group, under a primary of its own, is no usable answer: the leg
// counts as one that did not complete, and null stops the workflow.
async function mergeProposed(proposed, mergeLabel, phaseName) {
  if (proposed.length < 2) return proposed.map((item) => ({ primary: item, members: [] }));
  addLeg(mergeLabel, "merge", routing.verifier.model);
  const shown = proposed.map((item) => pick({
    id: item.id,
    reportedBy: item.reportedBy,
    outsideConcern: item.outsideConcern,
    ...item.finding,
  }, contracts.mergeFindingFields));
  const settlement = await agent(
    fillPrompt(prompts.merge, { target, head, findings: JSON.stringify(shown, null, 2) }),
    {
      engine: routing.verifier.engine,
      schema: contracts.mergeSchema,
      model: routing.verifier.model,
      effort: routing.verifier.effort,
      strip: ["skills", "agents"],
      identity: true,
      label: mergeLabel,
      phase: phaseName,
    }
  );
  const output = legOutput(settlement, mergeLabel);
  if (!output) return null;
  const byId = new Map(proposed.map((item) => [item.id, item]));
  const placed = new Set();
  const groups = [];
  for (const group of Array.isArray(output.groups) ? output.groups : []) {
    const ids = Array.isArray(group && group.findingIds) ? group.findingIds : [];
    if (ids.length === 0 || !ids.includes(group.primary) || ids.some((id) => !byId.has(id) || placed.has(id))) {
      incomplete.push(`agent ${mergeLabel} returned a grouping that does not place every finding exactly once under a primary of its group`);
      return null;
    }
    ids.forEach((id) => placed.add(id));
    groups.push({
      primary: byId.get(group.primary),
      members: ids.filter((id) => id !== group.primary).map((id) => byId.get(id)),
    });
  }
  if (placed.size !== proposed.length) {
    incomplete.push(`agent ${mergeLabel} returned a grouping that does not place every finding exactly once under a primary of its group`);
    return null;
  }
  return groups;
}

// One finding to verify: a merged group's primary write-up, with what the
// group's other write-ups add, or a finding a verifier raised.
function subjectOf({ primary, members }) {
  const writeUps = [primary, ...members];
  const dependsOn = [...new Set(writeUps
    .map((item) => typeof item.finding.dependsOn === "string" ? item.finding.dependsOn.trim() : "")
    .filter((fact) => fact !== ""))].join("; ");
  return {
    ...primary,
    finding: { ...primary.finding, dependsOn },
    members,
  };
}

function shownToVerifier(subject) {
  return pick({
    id: subject.id,
    ...subject.finding,
    alsoReportedAs: subject.members.length > 0
      ? subject.members.map(({ finding }) => ({ title: finding.title, explanation: finding.explanation }))
      : undefined,
    wholeScope: subject.wholeScope,
  }, contracts.verifierFindingFields);
}

// Verifies every subject, then each finding the verifiers raised, in
// rounds: what one round raises is merged like any proposed findings and
// verified by legs of the next, never by the leg that raised it. What the
// last round raises has no verdict, which leaves the run incomplete; it is
// returned unchecked for the record. Null means a merge failed, which has
// already left the run incomplete.
async function verifySubjects(subjects, labelPrefix, mergeLabel, phaseName) {
  const verified = [];
  let pending = subjects;
  for (let round = 1; round <= contracts.verificationRounds && pending.length > 0; round += 1) {
    const raised = await verificationRound(pending, round === 1 ? labelPrefix : `${labelPrefix}-round-${round}`, phaseName);
    verified.push(...pending);
    if (raised.length === 0 || round === contracts.verificationRounds) {
      pending = raised;
      break;
    }
    const groups = await mergeProposed(raised, `${mergeLabel}-round-${round + 1}`, phaseName);
    if (!groups) return { verified, unchecked: raised };
    pending = groups.map(subjectOf);
  }
  if (pending.length > 0)
    incomplete.push(`verification stopped after ${contracts.verificationRounds} rounds with ${pending.length} finding(s) raised and not verified`);
  return { verified, unchecked: pending };
}

async function verificationRound(subjects, labelPrefix, phaseName) {
  const batches = [];
  for (let offset = 0; offset < subjects.length; offset += contracts.verifierBatchSize) {
    const items = subjects.slice(offset, offset + contracts.verifierBatchSize);
    const label = `${labelPrefix}-${batches.length + 1}-${routing.verifier.engine}`;
    const findingIds = items.map((item) => item.id);
    items.forEach((item) => { item.verifyLabel = label; item.rawVerifier = null; });
    batches.push({ items, label, findingIds });
    addLeg(label, "verifier", routing.verifier.model, findingIds);
  }
  const settlements = await parallel(batches.map((batch) => () =>
    agent(
      fillPrompt(prompts.verifier, {
        ...reviewedChangeValues,
        findings: JSON.stringify(batch.items.map(shownToVerifier), null, 2),
      }),
      {
        engine: routing.verifier.engine,
        schema: contracts.verifierSchema,
        model: routing.verifier.model,
        effort: routing.verifier.effort,
        strip: ["skills", "agents"],
        identity: true,
        label: batch.label,
        phase: phaseName,
      }
    )
  ));
  const raised = [];
  batches.forEach((batch, index) => {
    const response = legOutput(settlements[index], batch.label);
    if (!response) return;
    // What a verifier established while checking survives even when its
    // verdict set is discarded as malformed.
    recordObservations(response.outOfScopeObservations, "verification", batch.label);
    (Array.isArray(response.newFindings) ? response.newFindings : []).forEach((finding, findingIndex) => {
      raised.push({
        id: `${batch.label}:raised:${findingIndex + 1}`,
        finding,
        source: "verification",
        reportedBy: "verification",
        outsideConcern: false,
        proposingLabel: batch.label,
        members: [],
      });
    });
    if (!Array.isArray(response.verdicts)) return;
    const expected = new Set(batch.findingIds);
    if (response.verdicts.some((verdict) => !expected.has(verdict && verdict.findingId))) return;
    for (const item of batch.items) {
      const matches = response.verdicts.filter((verdict) => verdict.findingId === item.id);
      if (matches.length !== 1) continue;
      const { findingId: omittedFindingId, ...rawVerifier } = matches[0];
      item.rawVerifier = rawVerifier;
    }
  });
  return raised;
}

// A verified finding as the adjudicator reads it: the primary write-up,
// the group's joined outside facts, and the write-ups merged into it.
function proposedFindingOf(subject) {
  return {
    id: subject.id,
    ...subject.finding,
    source: subject.source,
    ...(subject.outsideConcern ? { outsideConcern: true } : {}),
    ...(subject.wholeScope !== undefined ? { wholeScope: subject.wholeScope } : {}),
    ...(subject.members.length > 0 ? {
      mergedFrom: subject.members.map((member) => ({
        id: member.id,
        source: member.source,
        proposingLabel: member.proposingLabel,
        title: member.finding.title,
        path: member.finding.path,
        line: member.finding.line,
      })),
    } : {}),
    proposingLabel: subject.proposingLabel,
    verifyLabel: subject.verifyLabel,
    rawVerifier: subject.rawVerifier,
  };
}

function uncheckedFindingOf(subject) {
  return {
    id: subject.id,
    ...subject.finding,
    source: subject.source,
    proposingLabel: subject.proposingLabel,
  };
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

const incomplete = [];
const legs = [];
const outOfScopeObservations = [];

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

function specialistPrompt(unit) {
  return fillPrompt(prompts.specialist[unit.specialistType], {
    ...reviewedChangeValues,
    "concern-name": unit.specialistType,
    "other-concerns": concerns.filter((concern) => concern.name !== unit.specialistType).map(concernLine).join("\n"),
    "planned-focus": `${unit.concern}\n\n${unit.scope.map((path) => `- \`${path}\``).join("\n")}`,
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
