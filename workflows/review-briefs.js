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

const GPT_MODEL = "gpt-5.6-sol";
const GPT_PLANNER_MODEL = "gpt-5.6-terra";
const CLAUDE_MODEL = "claude-opus-4-8";

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
      return { status: "skipped", skipKind: "occasion", reason: `brief applies on occasion ${front.occasion.join(", ")}; this run names none` };
    if (!front.occasion.includes(occasion))
      return { status: "skipped", skipKind: "occasion", reason: `brief applies on occasion ${front.occasion.join(", ")}, not ${occasion}` };
    // A matched occasion is itself a satisfied trigger; a path-scope or relevance
    // it also carries only refines width, it does not further gate a brief the
    // occasion has already opted in.
    return { triggered: true };
  }
  // No occasion declared: run on a satisfied positive trigger — a touched
  // path-scope, or a relevance condition judged in the relevance phase.
  const scopeSatisfied =
    hasPathScope && changedPaths.some((path) => path === brief.scope || path.startsWith(brief.scope + "/"));
  if (scopeSatisfied) return { triggered: true };
  if (hasRelevance) return { triggered: false };
  return { status: "skipped", skipKind: "empty", reason: `nothing changed under scope ${brief.scope}/` };
}

function instructionBriefsFromInput(input) {
  return new Map((Array.isArray(input && input.instructionBriefs) ? input.instructionBriefs : [])
    .filter((entry) => entry && typeof entry.path === "string" && typeof entry.readPath === "string" && typeof entry.content === "string")
    .map((entry) => [entry.path, entry]));
}

function guidanceFromInput(input) {
  const guidance = input && input.guidance;
  if (!guidance || typeof guidance.path !== "string" || typeof guidance.content !== "string" || guidance.content.trim() === "") return null;
  return { grounding: guidance.grounding || "repository", path: guidance.path, content: guidance.content };
}

function groundedPrompt(instruction, guidance, assignment) {
  return `Read and follow the Markdown role brief at ${instruction.readPath}.\n\n` +
    `<role-brief path="${instruction.path}">\n${instruction.content}\n</role-brief>\n\n` +
    `<project-guidance grounding="${guidance.grounding}" path="${guidance.path}">\n${guidance.content}\n</project-guidance>\n\n` +
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
    briefs: [],
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
const guidance = guidanceFromInput(input);
if (!repositoryInstruction || !verifierInstruction)
  throw new Error("deterministic input omitted shipped repository or verifier brief");
if (!guidance) throw new Error("deterministic input omitted reviewed-project guidance");

const legs = [];
const addLeg = (label, role, expectedFamily, pinnedModel) => {
  const leg = { label, role, expectedFamily, pinnedModel };
  legs.push(leg);
  return leg;
};
const reports = new Map();
const candidates = [];
for (const brief of input.briefs) {
  const front = parseFrontmatter(brief.content);
  const base = { brief: brief.path, title: briefTitle(brief.path, front) };
  const disposition = deterministicDisposition(brief, front, input.occasion || null, input.changedPaths);
  if (disposition.status === "skipped") reports.set(brief.path, { ...base, ...disposition });
  else candidates.push({ brief, front, base, triggered: disposition.triggered });
}

// Only briefs not already settled by a deterministic trigger need the judged
// relevance evaluation; a triggered brief runs regardless of relevance.
const relevanceCandidates = candidates.filter((candidate) => candidate.front.relevance && !candidate.triggered);
const relevanceDecisions = new Map();
if (relevanceCandidates.length > 0) {
  phase("Relevance");
  addLeg("brief-relevance", "relevance", "gpt", GPT_PLANNER_MODEL);
  const relevanceResult = await agent(
    `<project-guidance grounding="${guidance.grounding}" path="${guidance.path}">\n${guidance.content}\n</project-guidance>\n\n` +
      `Judge which repository concerns ${input.target}...${input.head} gives work to. Inspect the actual diff when paths alone do not settle it. ` +
      `Mark a concern inapplicable only when the diff clearly gives it nothing to judge. Return one decision for every entry and preserve each brief path.\n` +
      `Changed paths: ${JSON.stringify(input.changedPaths)}\n` +
      `Concerns: ${JSON.stringify(relevanceCandidates.map(({ brief, front }) => ({ brief: brief.path, relevance: front.relevance })))}`,
    {
      engine: "codex",
      schema: relevanceSchema,
      model: GPT_PLANNER_MODEL,
      effort: "high",
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

function makeUnit(candidate, files, suffix, warning, concern) {
  const { brief, front, base } = candidate;
  return {
    brief: brief.path,
    briefContent: brief.content,
    title: base.title,
    extent: front.extent,
    scope: warning ? null : brief.scope,
    files,
    concern,
    warning,
    label: `repository-${slug(brief.path)}${suffix ? `-${suffix}` : ""}-claude`,
  };
}

const partitionRequests = runnable.map((candidate, index) => {
  const { brief, front } = candidate;
  const warning = brief.scope && !brief.scopeExists
    ? `brief scope ${brief.scope}/ matches no repository directory; ran repo-wide instead`
    : null;
  const inventory = front.extent === "full" ? scopeInventory(warning ? null : brief.scope) : [];
  if (front.extent !== "full" || front.sweep !== "per-file" || inventory.length === 0) return null;
  return {
    candidate,
    inventory,
    warning,
    label: `brief-partition-${index + 1}-${slug(brief.path)}-gpt`,
  };
}).filter(Boolean);

const partitions = new Map();
if (partitionRequests.length > 0) {
  phase("Partition");
  for (const request of partitionRequests)
    addLeg(request.label, "partition", "gpt", GPT_PLANNER_MODEL);
  const partitionResults = await parallel(partitionRequests.map((request) => () => agent(
      `<project-guidance grounding="${guidance.grounding}" path="${guidance.path}">\n${guidance.content}\n</project-guidance>\n\n` +
      `<repository-brief path="${request.candidate.brief.path}">\n${request.candidate.brief.content}\n</repository-brief>\n\n` +
      `Create a lossless partition of the assigned file inventory into coherent review units for this brief. Group paths by module, directory, or concern, whichever reflects the repository's actual structure. ` +
      `Let that structure determine the unit count. Assign every listed path to exactly one unit; include no unlisted paths. Return at least one unit. Use no skills for this planning judgement.\n` +
      `Assigned file inventory: ${JSON.stringify(request.inventory.map((entry) => entry.path))}`,
    {
      engine: "codex",
      schema: partitionSchema,
      model: GPT_PLANNER_MODEL,
      effort: "high",
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
        ...(request.warning ? { warning: request.warning } : {}),
      });
      return;
    }
    partitions.set(request.candidate.brief.path, normalisePartition(result, request.inventory));
  });
}

const dispatched = [];
for (const candidate of runnable) {
  const { brief, front } = candidate;
  const warning = brief.scope && !brief.scopeExists
    ? `brief scope ${brief.scope}/ matches no repository directory; ran repo-wide instead`
    : null;
  if (front.extent !== "full") {
    dispatched.push(makeUnit(candidate, [], null, warning, null));
    continue;
  }
  const inventory = scopeInventory(warning ? null : brief.scope);
  if (front.sweep === "per-file" && inventory.length > 0) {
    const partition = partitions.get(brief.path);
    if (!partition) continue;
    partition.forEach((unit, index) =>
      dispatched.push(makeUnit(candidate, unit.files, index + 1, warning, unit.concern)));
  } else {
    dispatched.push(makeUnit(candidate, inventory.map((entry) => entry.path), null, warning, null));
  }
}

phase("Review");
for (const unit of dispatched) addLeg(unit.label, "specialist", "claude", CLAUDE_MODEL);
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
    engine: "claude",
    schema: specialistSchema,
    model: CLAUDE_MODEL,
    effort: "high",
    label: unit.label,
    phase: "Review",
  });
}));

const reviewerStates = [];
const proposed = [];
const inapplicable = [];
dispatched.forEach((unit, unitIndex) => {
  const result = specialistResults[unitIndex];
  reviewerStates.push({
    label: unit.label,
    role: "specialist",
    brief: unit.brief,
    family: "claude",
    pinnedModel: CLAUDE_MODEL,
    status: result ? "done" : "no-result",
  });
  if (!result) {
    reports.set(unit.brief, {
      brief: unit.brief,
      title: unit.title,
      status: "not-run",
      reason: "specialist returned no result",
      ...(unit.warning ? { warning: unit.warning } : {}),
    });
    return;
  }
  if (result.applicability.status === "inapplicable") {
    inapplicable.push({
      brief: unit.brief,
      title: unit.title,
      status: "skipped",
      skipKind: "inapplicable",
      reason: result.applicability.reason,
    });
    return;
  }
  if (!reports.has(unit.brief))
    reports.set(unit.brief, {
      brief: unit.brief,
      title: unit.title,
      status: "run",
      reason: "applicable concern reviewed",
      ...(unit.warning ? { warning: unit.warning } : {}),
    });
  result.findings.forEach((finding, findingIndex) =>
    proposed.push({ unit, unitIndex, finding, findingIndex }));
});

phase("Verify");
for (const item of proposed) {
  item.verifyLabel = `verify-brief-${item.unitIndex + 1}-${item.findingIndex + 1}-gpt`;
  addLeg(item.verifyLabel, "verifier", "gpt", GPT_MODEL);
}
const verifierResults = await parallel(proposed.map((item) => () => agent(
  groundedPrompt(
    verifierInstruction,
    guidance,
    `Try to disprove this repository-brief finding against ${input.target}...${input.head} and the cited code.\n` +
      `Concern: ${item.unit.title}\nProposing specialist: ${item.unit.label}\nFinding data: ${JSON.stringify(item.finding)}`,
  ),
  {
    engine: "codex",
    schema: verifierSchema,
    model: GPT_MODEL,
    effort: "high",
    label: item.verifyLabel,
    phase: "Verify",
  },
)));

const proposedFindings = proposed.map((item, index) => ({
  id: `${item.unit.label}:${item.findingIndex + 1}`,
  source: item.unit.title,
  ...item.finding,
  proposingLabel: item.unit.label,
  verifyLabel: item.verifyLabel,
  rawVerifier: verifierResults[index] || null,
}));

return {
  reviewed: { target: input.target, head: input.head, occasion: input.occasion || null },
  stage: "present",
  requiredModelEvidence: legs,
  proposedFindings,
  briefs: [...reports.values(), ...inapplicable],
  dispatches: dispatched.map(({ brief, title, label, extent, scope, files }) => ({ brief, title, label, extent, scope, files })),
  reviewers: reviewerStates,
};
