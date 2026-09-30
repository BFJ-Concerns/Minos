export const meta = {
  name: "minos-repository-review-briefs",
  description: "Run applicable repository review briefs after the main review loop",
  phases: [
    { title: "Relevance", detail: "select the briefs this change gives work to" },
    { title: "Partition", detail: "divide broad brief scopes into coherent review units" },
    { title: "Review", detail: "run repository-concern specialists" },
    { title: "Verify", detail: "opposite-family verification of each proposed finding" },
  ],
};

const MAX_FINDINGS_PER_VERIFIER = 6;

function slug(value) {
  return String(value).toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "review";
}

function parseFrontmatter(content) {
  const front = { title: null, extent: "diff", sweep: "per-file", occasion: [], relevance: null };
  if (typeof content !== "string") return front;
  const match = content.match(/^---\n([\s\S]*?)\n---\n?/);
  if (!match) return front;
  for (const line of match[1].split("\n")) {
    const pair = line.match(/^(\w+):\s*(.*)$/);
    if (!pair) continue;
    const key = pair[1].toLowerCase();
    let value = pair[2];
    if (key !== "title" && key !== "relevance") value = value.replace(/#.*$/, "");
    value = value.trim();
    if (key === "title" && value) front.title = value;
    if (key === "extent" && value === "full") front.extent = "full";
    if (key === "sweep" && value === "whole-tree") front.sweep = "whole-tree";
    if (key === "occasion") front.occasion = value.split(",").map((token) => token.trim()).filter(Boolean);
    if (key === "relevance" && value) front.relevance = value;
  }
  return front;
}

function briefTitle(path, front) {
  if (front.title) return front.title;
  const stem = path.replace(/^.*\//, "").replace(/\.md$/, "");
  return stem.split("-").map((word) => word.charAt(0).toUpperCase() + word.slice(1)).join(" ");
}

function deterministicDisposition(brief, front, occasion, changedPaths) {
  const hasOccasion = front.occasion.length > 0;
  const hasRelevance = Boolean(front.relevance);
  const hasPathScope = typeof brief.scope === "string" && brief.scope !== "";
  const misconfiguration = hasPathScope && brief.scopeExists === false
    ? {
        skipKind: "misconfigured-scope",
        reason: `brief scope ${brief.scope}/ matches no repository directory`,
      }
    : null;
  if (!hasOccasion && !hasRelevance && !hasPathScope) {
    return {
      status: "skipped",
      skipKind: "no-trigger",
      reason: "brief declares no relevance, occasion, or path-scope trigger",
    };
  }
  // Occasion is a veto: a brief that names occasions runs only on a run that
  // names a matching one, whatever else it declares — it selects which runs the
  // brief applies to. A brief naming no occasion is unrestricted by occasion.
  if (hasOccasion) {
    if (!occasion)
      return {
        status: "skipped",
        skipKind: "occasion",
        reason: `brief applies on occasion ${front.occasion.join(", ")}; this run names none`,
        misconfiguration,
      };
    if (!front.occasion.includes(occasion))
      return {
        status: "skipped",
        skipKind: "occasion",
        reason: `brief applies on occasion ${front.occasion.join(", ")}, not ${occasion}`,
        misconfiguration,
      };
    // A matched occasion is itself a satisfied trigger; a path-scope or relevance
    // it also carries only refines width, it does not further gate a brief the
    // occasion has already opted in.
    return { triggered: true, misconfiguration };
  }
  // No occasion declared: run on a satisfied positive trigger — a touched
  // path-scope, or a relevance condition judged in the relevance phase.
  const scopeSatisfied =
    hasPathScope && changedPaths.some((path) => path === brief.scope || path.startsWith(brief.scope + "/"));
  if (scopeSatisfied) return { triggered: true, misconfiguration };
  if (hasRelevance) return { triggered: false, misconfiguration };
  if (misconfiguration) return { status: "skipped", ...misconfiguration, misconfiguration };
  return { status: "skipped", skipKind: "empty", reason: `nothing changed under scope ${brief.scope}/` };
}

function instructionBriefsFromInput(input) {
  return new Map((Array.isArray(input && input.instructionBriefs) ? input.instructionBriefs : [])
    .filter((entry) => entry && typeof entry.path === "string" && typeof entry.readPath === "string" && typeof entry.content === "string")
    .map((entry) => [entry.path, entry]));
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
    `${entry.content}\n</project-guidance>\n`
  ).join("\n") + "\n";
}

function groundedPrompt(instruction, guidance, assignment) {
  return `Read and follow the Markdown role brief at ${instruction.readPath}.\n\n` +
    `<role-brief path="${instruction.path}">\n${instruction.content}\n</role-brief>\n\n` +
    projectGuidanceSection(guidance) +
    assignment;
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
    outOfScopeObservations: [],
    briefs: [],
    misconfigurations: [],
    dispatches: [],
    reviewers: [],
  };
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

const specialistSchema = {
  type: "object",
  additionalProperties: false,
  required: ["applicability", "findings"],
  properties: {
    applicability: {
      type: "object",
      additionalProperties: false,
      required: ["status", "reason"],
      properties: {
        status: { type: "string", enum: ["applicable", "inapplicable"] },
        reason: { type: "string" },
      },
    },
    findings: { type: "array", items: findingShape },
    outOfScopeObservations: { type: "array", items: outOfScopeObservationShape },
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
    outOfScopeObservations: { type: "array", items: outOfScopeObservationShape },
  },
};

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
  throw new Error("brief workflow needs args {target, head, workspace, briefs, changedPaths, trackedFiles, guidance, instructionBriefs}");
if (input.hasReviewDirectory === false) return emptyEnvelope(input);
if (!Array.isArray(input.briefs) || !Array.isArray(input.changedPaths) || !Array.isArray(input.trackedFiles))
  throw new Error("deterministic brief enumeration is incomplete");

const instructions = instructionBriefsFromInput(input);
const repositoryInstruction = instructions.get("workflows/review-briefs/repository.md");
const verifierInstruction = instructions.get("workflows/review-briefs/verifier.md");
const guidance = projectGuidanceFromInput(input);
if (!repositoryInstruction || !verifierInstruction)
  throw new Error("deterministic input omitted shipped repository or verifier brief");
if (!guidance) throw new Error("deterministic input omitted reviewed-project guidance");
const routing = routingFromInput(input);
if (!routing) throw new Error("deterministic input omitted the role routing");

const legs = [];
const addLeg = (label, role, pinnedModel, findingIds = null) => {
  const leg = { label, role, pinnedModel };
  if (findingIds) leg.findingIds = findingIds;
  legs.push(leg);
  return leg;
};
const reports = new Map();
const misconfigurations = [];
const candidates = [];
for (const brief of input.briefs) {
  const front = parseFrontmatter(brief.content);
  const base = { brief: brief.path, title: briefTitle(brief.path, front) };
  const disposition = deterministicDisposition(brief, front, input.occasion || null, input.changedPaths);
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
    projectGuidanceSection(guidance) +
      `Judge which repository concerns ${input.target}...${input.head} gives work to. Inspect the actual diff when paths alone do not settle it. ` +
      `Mark a concern inapplicable only when the diff clearly gives it nothing to judge. Return one decision for every entry and preserve each brief path.\n` +
      `Changed paths: ${JSON.stringify(input.changedPaths)}\n` +
      `Concerns: ${JSON.stringify(relevanceCandidates.map(({ brief, front }) => ({ brief: brief.path, relevance: front.relevance })))}`,
    {
      engine: routing["brief-planner"].engine,
      schema: relevanceSchema,
      model: routing["brief-planner"].model,
      effort: routing["brief-planner"].effort,
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

function findingId(item) {
  return `repository-brief-unit-${item.unitIndex + 1}:finding:${item.findingIndex + 1}`;
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
      projectGuidanceSection(guidance) +
      `<repository-brief path="${request.candidate.brief.path}">\n${request.candidate.brief.content}\n</repository-brief>\n\n` +
      `Create a lossless partition of the assigned file inventory into coherent review units for this brief. Group paths by module, directory, or concern, whichever reflects the repository's actual structure. ` +
      `Each dispatched unit has a real cost, so the partition is the minimal one whose units each need a genuinely distinct reading for this brief's concern — merge groups one reading covers, and never split a unit to mirror directory structure for its own sake. ` +
      `Assign every listed path to exactly one unit; include no unlisted paths. Use no skills for this planning judgement.\n` +
      `Assigned file inventory: ${JSON.stringify(request.inventory.map((entry) => entry.path))}`,
    {
      engine: routing["brief-planner"].engine,
      schema: partitionSchema,
      model: routing["brief-planner"].model,
      effort: routing["brief-planner"].effort,
      label: request.label,
      phase: "Partition",
    },
  )));
  partitionRequests.forEach((request, index) => {
    const result = partitionResults[index];
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

phase("Review");
for (const unit of dispatched) addLeg(unit.label, "specialist", routing.proposer.model);
const specialistResults = await parallel(dispatched.map((unit) => () => {
  const scope = unit.scope ? `${unit.scope}/` : "the whole repository";
  const files = unit.files.length > 0 ? `\nAssigned files: ${unit.files.join(", ")}` : "";
  const concern = unit.concern ? `\nAssigned review unit: ${unit.concern}` : "";
  return agent(groundedPrompt(
    repositoryInstruction,
    guidance,
    `<repository-brief path="${unit.brief}">\n${unit.briefContent}\n</repository-brief>\n\n` +
      `Assigned scope: ${scope}.${concern}${files}\n` +
      (unit.extent === "full" ? "Audit the assigned scope regardless of what the diff changed." : `Judge only what ${input.target}...${input.head} changed in the assigned scope.`),
  ), {
    engine: routing.proposer.engine,
    schema: specialistSchema,
    model: routing.proposer.model,
    effort: routing.proposer.effort,
    label: unit.label,
    phase: "Review",
  });
}));

const reviewerStates = [];
const proposed = [];
const outOfScopeObservations = [];
const inapplicableByBrief = new Map();
dispatched.forEach((unit, unitIndex) => {
  const result = specialistResults[unitIndex];
  reviewerStates.push({
    label: unit.label,
    role: "specialist",
    brief: unit.brief,
    family: "gpt",
    pinnedModel: routing.proposer.model,
    status: result ? "done" : "no-result",
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
  const observations = Array.isArray(result.outOfScopeObservations)
    ? result.outOfScopeObservations
    : [];
  observations.forEach((observation, observationIndex) => {
    outOfScopeObservations.push({
      id: `${unit.label}:observation:${observationIndex + 1}`,
      source: unit.title,
      ...observation,
      observingLabel: unit.label,
      verified: false,
    });
  });
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
  result.findings.forEach((finding, findingIndex) =>
    proposed.push({ unit, unitIndex, finding, findingIndex }));
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

phase("Verify");
const verifierGroups = [];
for (let offset = 0; offset < proposed.length; offset += MAX_FINDINGS_PER_VERIFIER) {
  const items = proposed.slice(offset, offset + MAX_FINDINGS_PER_VERIFIER);
  const groupIndex = Math.floor(offset / MAX_FINDINGS_PER_VERIFIER) + 1;
  const label = `verify-brief-${groupIndex}-${routing.verifier.engine}`;
  const findingIds = items.map(findingId);
  const group = { items, label, findingIds };
  verifierGroups.push(group);
  items.forEach((item) => { item.verifyLabel = label; });
  addLeg(label, "verifier", routing.verifier.model, findingIds);
}
const verifierResults = await parallel(verifierGroups.map((group) => () => agent(
  groundedPrompt(
    verifierInstruction,
    guidance,
    `Try to disprove each repository-brief finding against ${input.target}...${input.head} and the cited code.\n` +
      `Findings: ${JSON.stringify(group.items.map((item, index) => ({
        id: group.findingIds[index],
        concern: item.unit.concern,
        unit: item.unit.concern || item.unit.title,
        proposingSpecialist: item.unit.label,
        ...item.finding,
      })))}`,
  ),
  {
    engine: routing.verifier.engine,
    schema: verifierSchema,
    model: routing.verifier.model,
    effort: routing.verifier.effort,
    label: group.label,
    phase: "Verify",
  },
)));

const verifierByFinding = new Map(proposed.map((item) => [findingId(item), null]));
// The observation channel is independent of verdict validity: what a
// verifier established while checking survives even when its verdict set
// is discarded as malformed.
verifierGroups.forEach((group, groupIndex) => {
  const response = verifierResults[groupIndex];
  const observations = response && Array.isArray(response.outOfScopeObservations)
    ? response.outOfScopeObservations
    : [];
  observations.forEach((observation, observationIndex) => {
    outOfScopeObservations.push({
      id: `${group.label}:observation:${observationIndex + 1}`,
      source: "verification",
      ...observation,
      observingLabel: group.label,
      verified: false,
    });
  });
});
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
  for (const id of group.findingIds) {
    const matches = verdictsById.get(id) || [];
    if (matches.length !== 1) continue;
    const { findingId: omittedFindingId, ...rawVerifier } = matches[0];
    verifierByFinding.set(id, rawVerifier);
  }
});

const proposedFindings = proposed.map((item) => ({
  id: findingId(item),
  source: item.unit.title,
  ...item.finding,
  proposingLabel: item.unit.label,
  verifyLabel: item.verifyLabel,
  rawVerifier: verifierByFinding.get(findingId(item)),
}));

return {
  reviewed: { target: input.target, head: input.head, occasion: input.occasion || null },
  stage: "present",
  requiredModelEvidence: legs,
  proposedFindings,
  outOfScopeObservations,
  briefs: [...reports.values()],
  misconfigurations,
  dispatches: dispatched.map(({ brief, title, label, extent, scope, files }) => ({ brief, title, label, extent, scope, files })),
  reviewers: reviewerStates,
};
