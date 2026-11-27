export const meta = {
  name: "minos-repository-review-briefs",
  description: "Run applicable repository review briefs after the main pull-request review",
  phases: [
    { title: "Relevance", detail: "select the briefs this change gives work to" },
    { title: "Partition", detail: "divide broad brief scopes into coherent review units" },
    { title: "Review", detail: "run repository-concern specialists" },
    { title: "Merge", detail: "group the proposed findings one fix would resolve" },
    { title: "Verify", detail: "opposite-family verification of each merged finding" },
  ],
};


function slug(value) {
  return String(value).toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "review";
}

// A workflow script cannot import statically, so what this one shares with
// review.js is loaded from beside it: the checks of the input each builder
// attached, beside the builder's own module; the prompt slots; and the
// proposing, merge and verification stages every review runs.
const { contractsFromInput } = await import("./review-contracts.mjs");
const { routingFromInput } = await import("./role-routing.mjs");
const { projectGuidanceFromInput, projectGuidanceSection } = await import("./orientation-guidance.mjs");
const { repositoryDescriptionFromInput, repositoryDescriptionSection } = await import("./repository-description.mjs");
const { pullRequestFromInput, pullRequestSection } = await import("./pull-request-description.mjs");
const { promptFits, fillPrompt } = await import("./review-prompts.mjs");
const { reviewStages, proposalsOf, subjectOf, proposedFindingOf, uncheckedFindingOf } = await import("./review-stages.mjs");

// The deterministic brief pass arrives computed: the input builder parsed
// each brief's frontmatter, named it and settled its path-scope and
// occasion triggers through workflows/brief-dispositions.mjs, the one
// source push inspection uses to decide whether briefs engage. This workflow
// adds the judged legs and re-derives nothing; a brief missing any of it fails the
// workflow before dispatch.
function briefsFromInput(input) {
  const briefs = input && input.briefs;
  if (!Array.isArray(briefs)) return null;
  for (const brief of briefs) {
    const front = brief && brief.front;
    const disposition = brief && brief.disposition;
    if (
      !brief ||
      typeof brief.path !== "string" || brief.path === "" ||
      typeof brief.content !== "string" ||
      (brief.scope !== null && typeof brief.scope !== "string") ||
      typeof brief.title !== "string" || brief.title === "" ||
      !front || typeof front !== "object" ||
      (front.extent !== "diff" && front.extent !== "full") ||
      (front.sweep !== "per-file" && front.sweep !== "whole-tree") ||
      !Array.isArray(front.occasion) ||
      (front.relevance !== null && typeof front.relevance !== "string") ||
      !disposition || typeof disposition !== "object" ||
      !(disposition.status === "skipped"
        ? typeof disposition.skipKind === "string" && typeof disposition.reason === "string"
        : typeof disposition.triggered === "boolean") ||
      (disposition.misconfiguration !== undefined && disposition.misconfiguration !== null &&
        (typeof disposition.misconfiguration.skipKind !== "string" || typeof disposition.misconfiguration.reason !== "string"))
    ) return null;
  }
  return briefs;
}

function promptsFromInput(input) {
  const prompts = input && input.prompts;
  if (!prompts || typeof prompts !== "object" || Array.isArray(prompts)) return null;
  const assignment = prompts.assignment;
  if (!assignment || typeof assignment !== "object" || Array.isArray(assignment)) return null;
  if (
    !promptFits(prompts.repository, "repository") ||
    !promptFits(prompts.briefRelevance, "brief-relevance") ||
    !promptFits(prompts.briefPartition, "brief-partition") ||
    !["changedPart", "wholePart", "unit", "files"].every((part) => promptFits(assignment[part], "assignment")) ||
    !promptFits(prompts.merge, "merge") ||
    !promptFits(prompts.verifier, "verifier")
  ) return null;
  return prompts;
}

function emptyEnvelope(input) {
  return {
    reviewed: {
      target: input && typeof input.target === "string" ? input.target : null,
      head: input && typeof input.head === "string" ? input.head : null,
      occasion: input && typeof input.occasion === "string" ? input.occasion : null,
    },
    stage: "absent",
    requiredModelEvidence: [],
    proposedFindings: [],
    uncheckedFindings: [],
    outOfScopeObservations: [],
    briefs: [],
    misconfigurations: [],
    dispatches: [],
    reviewers: [],
  };
}

const relevanceSchema = {
  type: "object",
  additionalProperties: false,
  required: ["decisions"],
  properties: {
    decisions: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["brief", "applicable", "reason"],
        properties: {
          brief: { type: "string" },
          applicable: { type: "boolean" },
          reason: { type: "string" },
        },
      },
    },
  },
};

const partitionSchema = {
  type: "object",
  additionalProperties: false,
  required: ["units"],
  properties: {
    units: {
      type: "array",
      minItems: 1,
      items: {
        type: "object",
        additionalProperties: false,
        required: ["id", "concern", "files"],
        properties: {
          id: { type: "string" },
          concern: { type: "string" },
          files: { type: "array", minItems: 1, items: { type: "string" } },
        },
      },
    },
  },
};

function normalisePartition(plan, inventory) {
  const known = new Set(inventory.map((entry) => entry.path));
  const assigned = new Set();
  const units = [];
  for (const unit of plan.units) {
    const files = [];
    for (const path of unit.files) {
      if (known.has(path) && !assigned.has(path)) {
        assigned.add(path);
        files.push(path);
      }
    }
    if (files.length > 0) units.push({ id: unit.id, concern: unit.concern, files });
  }
  const missing = inventory.map((entry) => entry.path).filter((path) => !assigned.has(path));
  if (missing.length > 0) {
    units.push({
      id: "unassigned-files",
      concern: "Files omitted from the proposed partition",
      files: missing,
    });
  }
  return units;
}

const input = args && typeof args === "object" && !Array.isArray(args) ? args : null;
if (!input || typeof input.target !== "string" || typeof input.head !== "string")
  throw new Error("brief workflow needs args {target, head, briefs, changedPaths, trackedFiles, guidance, prompts, contracts}");
const target = input.target;
const head = input.head;
const contracts = contractsFromInput(input);
if (!contracts) throw new Error("deterministic input omitted or malformed the review contracts");
if (input.hasReviewDirectory === false) return emptyEnvelope(input);
const briefs = briefsFromInput(input);
if (!briefs || !Array.isArray(input.changedPaths) || !Array.isArray(input.trackedFiles))
  throw new Error("deterministic brief enumeration is incomplete: every brief carries its frontmatter, title and disposition");

const prompts = promptsFromInput(input);
if (!prompts) throw new Error("deterministic input omitted or malformed the review prompts");
const guidance = projectGuidanceFromInput(input);
if (!guidance) throw new Error("deterministic input omitted reviewed-project guidance");
const routing = routingFromInput(input);
if (!routing) throw new Error("deterministic input omitted the role routing");
const reviewedChangeValues = {
  target,
  head,
  "project-documents": projectGuidanceSection(guidance),
  "repository-description": repositoryDescriptionSection(repositoryDescriptionFromInput(input)),
  "pull-request": pullRequestSection(pullRequestFromInput(input)),
};

const {
  incomplete, legs, outOfScopeObservations,
  addLeg, legOutput,
  proposeUnits, mergeProposed, verifySubjects,
} = reviewStages({ agent, parallel, routing, contracts, prompts, reviewedChangeValues });
const reports = new Map();
const misconfigurations = [];
const candidates = [];
for (const brief of briefs) {
  const { front, disposition } = brief;
  const base = { brief: brief.path, title: brief.title };
  const { misconfiguration, ...recordedDisposition } = disposition;
  if (disposition.status === "skipped") reports.set(brief.path, { ...base, ...recordedDisposition });
  else
    candidates.push({ brief, front, base, triggered: disposition.triggered });
  if (misconfiguration)
    misconfigurations.push({
      ...base,
      kind: misconfiguration.skipKind,
      reason: misconfiguration.reason,
    });
}

// Only briefs not already settled by a deterministic trigger need the judged
// relevance evaluation; a triggered brief runs regardless of relevance.
const relevanceCandidates = candidates.filter((candidate) => candidate.front.relevance && !candidate.triggered);
const relevanceDecisions = new Map();
if (relevanceCandidates.length > 0) {
  phase("Relevance");
  addLeg("brief-relevance", "relevance", routing["brief-planner"].model);
  const relevanceResult = await agent(
    fillPrompt(prompts.briefRelevance, {
      ...reviewedChangeValues,
      "changed-paths": JSON.stringify(input.changedPaths, null, 2),
      "relevance-conditions": JSON.stringify(
        relevanceCandidates.map(({ brief, front }) => ({ brief: brief.path, relevance: front.relevance })), null, 2),
    }),
    {
      engine: routing["brief-planner"].engine,
      schema: relevanceSchema,
      model: routing["brief-planner"].model,
      effort: routing["brief-planner"].effort,
      strip: ["skills", "agents"],
      label: "brief-relevance",
      phase: "Relevance",
    },
  );
  for (const decision of relevanceResult && Array.isArray(relevanceResult.decisions) ? relevanceResult.decisions : [])
    if (relevanceCandidates.some(({ brief }) => brief.path === decision.brief) && !relevanceDecisions.has(decision.brief))
      relevanceDecisions.set(decision.brief, decision);
}

const runnable = [];
for (const candidate of candidates) {
  const { brief, front, base, triggered } = candidate;
  if (!triggered && front.relevance) {
    const decision = relevanceDecisions.get(brief.path);
    if (!decision) {
      reports.set(brief.path, { ...base, status: "not-run", reason: "relevance judgement returned no decision" });
      continue;
    }
    if (!decision.applicable) {
      reports.set(brief.path, { ...base, status: "skipped", skipKind: "relevance", reason: decision.reason });
      continue;
    }
  }
  runnable.push(candidate);
}

function scopeInventory(scope) {
  return input.trackedFiles.filter((entry) => !scope || entry.path === scope || entry.path.startsWith(scope + "/"));
}

function makeUnit(candidate, files, suffix, concern) {
  const { brief, front, base } = candidate;
  return {
    brief: brief.path,
    briefContent: brief.content,
    title: base.title,
    extent: front.extent,
    scope: brief.scope,
    files,
    concern,
    label: `repository-${slug(brief.path)}${suffix ? `-${suffix}` : ""}-${routing.proposer.engine}`,
  };
}

const partitionRequests = runnable.map((candidate, index) => {
  const { brief, front } = candidate;
  const inventory = front.extent === "full" ? scopeInventory(brief.scope) : [];
  if (front.extent !== "full" || front.sweep !== "per-file" || inventory.length === 0) return null;
  return {
    candidate,
    inventory,
    label: `brief-partition-${index + 1}-${slug(brief.path)}-${routing["brief-planner"].engine}`,
  };
}).filter(Boolean);

const partitions = new Map();
if (partitionRequests.length > 0) {
  phase("Partition");
  for (const request of partitionRequests)
    addLeg(request.label, "partition", routing["brief-planner"].model);
  const partitionResults = await parallel(partitionRequests.map((request) => () => agent(
    fillPrompt(prompts.briefPartition, {
      ...reviewedChangeValues,
      "brief-path": request.candidate.brief.path,
      brief: request.candidate.brief.content,
      "file-inventory": JSON.stringify(request.inventory.map((entry) => entry.path), null, 2),
    }),
    {
      engine: routing["brief-planner"].engine,
      schema: partitionSchema,
      model: routing["brief-planner"].model,
      effort: routing["brief-planner"].effort,
      strip: ["skills", "agents"],
      identity: true,
      label: request.label,
      phase: "Partition",
    },
  )));
  partitionRequests.forEach((request, index) => {
    const result = legOutput(partitionResults[index], request.label);
    if (!result || !Array.isArray(result.units)) {
      reports.set(request.candidate.brief.path, {
        ...request.candidate.base,
        status: "not-run",
        reason: "partition exploration returned no usable result",
      });
      return;
    }
    partitions.set(request.candidate.brief.path, normalisePartition(result, request.inventory));
  });
}

const dispatched = [];
for (const candidate of runnable) {
  const { brief, front } = candidate;
  if (front.extent !== "full") {
    dispatched.push(makeUnit(candidate, [], null, null));
    continue;
  }
  const inventory = scopeInventory(brief.scope);
  if (front.sweep === "per-file" && inventory.length > 0) {
    const partition = partitions.get(brief.path);
    if (!partition) continue;
    partition.forEach((unit, index) =>
      dispatched.push(makeUnit(candidate, unit.files, index + 1, unit.concern)));
  } else {
    dispatched.push(makeUnit(candidate, inventory.map((entry) => entry.path), null, null));
  }
}

// The directory a whole-scope brief audits: the brief's own directory, or
// the repository root for a brief at the top of `.review/`.
function wholeScopeOf(unit) {
  return unit.scope ? `${unit.scope}/` : "./";
}

// What a brief's reviewer is given to judge: the change within the brief's
// directory, or for a whole-scope brief all of that directory as it stands,
// narrowed to a partition unit's files where the scope was partitioned.
// Each applicable part is filled in a pass of its own, so a value is never
// itself read for slots, and the parts are joined in their shipped order.
function assignment(unit) {
  const values = {
    target,
    head,
    "brief-directory": wholeScopeOf(unit),
    "unit-concern": unit.concern || "",
    "unit-files": unit.files.map((path) => `- \`${path}\``).join("\n"),
  };
  const parts = [unit.extent === "full" ? prompts.assignment.wholePart : prompts.assignment.changedPart];
  if (unit.concern) parts.push(prompts.assignment.unit);
  if (unit.files.length > 0) parts.push(prompts.assignment.files);
  return parts.map((part) => fillPrompt(part, values)).join("\n\n");
}

phase("Review");
const specialistResults = await proposeUnits(
  dispatched.map((unit) => ({
    ...unit,
    prompt: fillPrompt(prompts.repository, {
      ...reviewedChangeValues,
      "brief-path": unit.brief,
      brief: unit.briefContent,
      assignment: assignment(unit),
    }),
    source: unit.title,
  })),
  "Review",
);

const reviewerStates = [];
const proposed = [];
const inapplicableByBrief = new Map();
specialistResults.forEach(({ unit, completed, output: result }, unitIndex) => {
  reviewerStates.push({
    label: unit.label,
    role: "specialist",
    brief: unit.brief,
    family: routing.proposer.engine,
    pinnedModel: routing.proposer.model,
    status: completed ? "done" : "no-result",
  });
  if (!result) {
    reports.set(unit.brief, {
      brief: unit.brief,
      title: unit.title,
      status: "not-run",
      reason: "specialist returned no result",
    });
    return;
  }
  const propose = (findings, kind, fields) => proposed.push(...proposalsOf(
    findings,
    (findingIndex) => `repository-brief-unit-${unitIndex + 1}:${kind}:${findingIndex + 1}`,
    { source: unit.title, reportedBy: unit.title, proposingLabel: unit.label, ...fields },
  ));
  // A problem outside the brief's concern that the change introduced is a
  // finding about the change, whatever the brief's extent or applicability.
  propose(result.outsideConcernFindings, "outside", { outsideConcern: true });
  if (result.applicability.status === "inapplicable") {
    const aggregate = inapplicableByBrief.get(unit.brief) || {
      title: unit.title,
      units: [],
    };
    aggregate.units.push({
      label: unit.label,
      concern: unit.concern,
      reason: result.applicability.reason,
    });
    inapplicableByBrief.set(unit.brief, aggregate);
    return;
  }
  if (!reports.has(unit.brief))
    reports.set(unit.brief, {
      brief: unit.brief,
      title: unit.title,
      status: "run",
      reason: "applicable concern reviewed",
    });
  // A finding is made under its brief, whose path it carries to the
  // verifier, and under the brief's directory as it stands where the brief
  // reads its whole part.
  propose(result.findings, "finding", {
    outsideConcern: false,
    brief: unit.brief,
    ...(unit.extent === "full" ? { wholeScope: wholeScopeOf(unit) } : {}),
  });
});

for (const [brief, inapplicable] of inapplicableByBrief) {
  const report = reports.get(brief);
  if (report) {
    reports.set(brief, {
      ...report,
      inapplicableUnits: inapplicable.units,
    });
    continue;
  }
  reports.set(brief, {
    brief,
    title: inapplicable.title,
    status: "skipped",
    skipKind: "inapplicable",
    reason: inapplicable.units.length === 1
      ? inapplicable.units[0].reason
      : `all ${inapplicable.units.length} partition units were inapplicable`,
    inapplicableUnits: inapplicable.units,
  });
}

function envelope(fields) {
  return {
    reviewed: { target, head, occasion: input.occasion || null },
    stage: "present",
    ...(incomplete.length > 0 ? { incomplete } : {}),
    requiredModelEvidence: legs,
    proposedFindings: [],
    uncheckedFindings: [],
    outOfScopeObservations,
    briefs: [...reports.values()],
    misconfigurations,
    dispatches: dispatched.map(({ brief, title, label, extent, scope, files }) => ({ brief, title, label, extent, scope, files })),
    reviewers: reviewerStates,
    ...fields,
  };
}

phase("Merge");
const groups = await mergeProposed(proposed, "brief-merge", "Merge");
if (!groups) return envelope({});

phase("Verify");
const { verified, unchecked } = await verifySubjects(groups.map(subjectOf), "verify-brief", "brief-merge", "Verify");

return envelope({
  proposedFindings: verified.map(proposedFindingOf),
  uncheckedFindings: unchecked.map(uncheckedFindingOf),
});
