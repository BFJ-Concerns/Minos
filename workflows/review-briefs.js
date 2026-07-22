export const meta = {
  name: "minos-repository-review-briefs",
  description: "Run applicable repository review briefs after the main review loop",
  phases: [
    { title: "Relevance", detail: "select the briefs this change gives work to" },
    { title: "Review", detail: "run repository-concern specialists" },
    { title: "Verify", detail: "opposite-family verification of each proposed finding" },
  ],
};

const GPT_MODEL = "gpt-5.6-sol";
const GPT_PLANNER_MODEL = "gpt-5.6-terra";
const CLAUDE_MODEL = "claude-opus-4-8";
const PER_FILE_CHUNK = 40;

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
  if (front.occasion.length > 0) {
    if (!occasion)
      return { status: "skipped", skipKind: "occasion", reason: `brief applies on occasion ${front.occasion.join(", ")}; this run names none` };
    if (!front.occasion.includes(occasion))
      return { status: "skipped", skipKind: "occasion", reason: `brief applies on occasion ${front.occasion.join(", ")}, not ${occasion}` };
  }
  if (front.extent === "diff" && brief.scope && brief.scopeExists) {
    const inScope = changedPaths.some((path) => path === brief.scope || path.startsWith(brief.scope + "/"));
    if (!inScope)
      return { status: "skipped", skipKind: "empty", reason: `nothing changed under its scope ${brief.scope}/` };
  }
  if (
    front.extent === "diff" &&
    front.occasion.length === 0 &&
    !front.relevance &&
    !brief.scope
  ) {
    return {
      status: "skipped",
      skipKind: "no-condition",
      reason: "brief has no relevance, occasion, path scope, or full-extent condition",
    };
  }
  return null;
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
  const skip = deterministicDisposition(brief, front, input.occasion || null, input.changedPaths);
  if (skip) reports.set(brief.path, { ...base, ...skip });
  else candidates.push({ brief, front, base });
}

const relevanceCandidates = candidates.filter(({ front }) => front.extent === "diff" && front.relevance);
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
  const { brief, front, base } = candidate;
  if (front.extent === "diff" && front.relevance) {
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

function makeUnit(candidate, files, suffix, warning) {
  const { brief, front, base } = candidate;
  return {
    brief: brief.path,
    briefContent: brief.content,
    title: base.title,
    extent: front.extent,
    scope: warning ? null : brief.scope,
    files,
    warning,
    label: `repository-${slug(brief.path)}${suffix ? `-${suffix}` : ""}-claude`,
  };
}

const dispatched = [];
for (const candidate of runnable) {
  const { brief, front } = candidate;
  const warning = brief.scope && !brief.scopeExists
    ? `brief scope ${brief.scope}/ matches no repository directory; ran repo-wide instead`
    : null;
  if (front.extent !== "full") {
    dispatched.push(makeUnit(candidate, [], null, warning));
    continue;
  }
  const inventory = scopeInventory(warning ? null : brief.scope);
  if (front.sweep === "per-file" && inventory.length > PER_FILE_CHUNK) {
    for (let index = 0; index < inventory.length; index += PER_FILE_CHUNK)
      dispatched.push(makeUnit(candidate, inventory.slice(index, index + PER_FILE_CHUNK).map((entry) => entry.path), index / PER_FILE_CHUNK + 1, warning));
  } else dispatched.push(makeUnit(candidate, inventory.map((entry) => entry.path), null, warning));
}

phase("Review");
for (const unit of dispatched) addLeg(unit.label, "specialist", "claude", CLAUDE_MODEL);
const specialistResults = await parallel(dispatched.map((unit) => () => {
  const scope = unit.scope ? `${unit.scope}/` : "the whole repository";
  const files = unit.files.length > 0 ? `\nAssigned files: ${unit.files.join(", ")}` : "";
  return agent(groundedPrompt(
    repositoryInstruction,
    guidance,
    `<repository-brief path="${unit.brief}">\n${unit.briefContent}\n</repository-brief>\n\n` +
      `Assigned scope: ${scope}.${files}\n` +
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
