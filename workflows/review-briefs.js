export const meta = {
  name: "minos-repository-review-briefs",
  description: "Run applicable repository review briefs after the main review loop",
  phases: [
    { title: "Relevance", detail: "select the briefs this change gives work to" },
    { title: "Review", detail: "run bounded repository-concern specialists" },
    { title: "Verify", detail: "opposite-family verification of each proposed finding" },
  ],
};

const GPT_MODEL = "anthropic-gpt-5.6-sol";
const GPT_PLANNER_MODEL = "anthropic-gpt-5.6-terra";
const CLAUDE_MODEL = "claude-opus-4-8";
const MAX_SPECIALISTS = 24;
const MAX_FINDINGS_PER_SPECIALIST = 2;
const PER_FILE_CHUNK = 40;
const PER_FILE_OVERHEAD_BYTES = 2_000;
const WHOLE_TREE_READING_BUDGET_BYTES = 650_000;
const LOW_COMBINED_CONFIDENCE = 70;

function familyOf(modelId) {
  if (typeof modelId !== "string" || modelId === "") return "unknown";
  const id = modelId.toLowerCase();
  if (id.includes("gpt-")) return "gpt";
  if (id.includes("claude") || ["haiku", "sonnet", "opus", "fable"].includes(id)) return "claude";
  return "unknown";
}

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

function progressRecord(runRecord, label) {
  const records = runRecord && Array.isArray(runRecord.workflowProgress) ? runRecord.workflowProgress : [];
  return records.find((record) => record && record.label === label) || null;
}

function actualModelEvidence(runRecord, leg) {
  const record = progressRecord(runRecord, leg.label);
  const done = Boolean(record && record.state === "done");
  const actualModel = done ? (record.fallbackModel || record.model || null) : null;
  const actualFamily = familyOf(actualModel);
  return {
    ...leg,
    state: record ? record.state || "unknown" : "absent",
    actualModel,
    actualFamily,
    confirmed: done && actualFamily === leg.expectedFamily,
  };
}

function infrastructureFailure(runRecord) {
  const terminal = runRecord && runRecord.terminal;
  if (!terminal) return null;
  const text = typeof terminal === "string" ? terminal : JSON.stringify(terminal);
  if (/out[- ]of[- ]memory|\boom\b/i.test(text)) return { kind: "oom", detail: terminal };
  if (/cancel/i.test(text)) return { kind: "cancelled", detail: terminal };
  return { kind: "terminal", detail: terminal };
}

function attemptAccounting(runRecord) {
  const attempts = runRecord && Array.isArray(runRecord.attempts) ? runRecord.attempts : [];
  const progress = runRecord && Array.isArray(runRecord.workflowProgress) ? runRecord.workflowProgress : [];
  return {
    attempts,
    legs: progress.map((record) => ({
      label: record.label || null,
      state: record.state || "unknown",
      attempt: Number.isInteger(record.attempt) ? record.attempt : 1,
      lastAttemptReason: record.lastAttemptReason || null,
    })),
  };
}

function emptyResult(input, fault) {
  return {
    reviewed: { target: input && input.target || null, head: input && input.head || null, occasion: input && input.occasion || null },
    stage: input && input.hasReviewDirectory === false ? "absent" : "present",
    status: fault ? "incomplete" : "complete",
    complete: !fault,
    verdictsComplete: !fault,
    incomplete: fault ? [fault] : [],
    infrastructureFailure: null,
    briefs: [],
    dispatches: [],
    reviewers: [],
    findings: [],
    confirmedFindings: [],
    operatorAttention: [],
    briefReview: null,
    fixRequired: false,
    requiredModelEvidence: [],
    modelEvidence: [],
    accounting: { specialists: 0, verification: 0, total: 0, attempts: { attempts: [], legs: [] } },
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
  required: ["findings"],
  properties: { findings: { type: "array", maxItems: MAX_FINDINGS_PER_SPECIALIST, items: findingShape } },
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

const input = args && typeof args === "object" ? args : null;
if (!input || typeof input.target !== "string" || typeof input.head !== "string")
  return emptyResult(input, "brief workflow needs args {target, head, workspace, briefs, changedPaths, trackedFiles, guidance, instructionBriefs}");
if (input.hasReviewDirectory === false) return emptyResult(input, null);
if (!Array.isArray(input.briefs) || !Array.isArray(input.changedPaths) || !Array.isArray(input.trackedFiles))
  return emptyResult(input, "deterministic brief enumeration is incomplete");

const instructions = instructionBriefsFromInput(input);
const repositoryInstruction = instructions.get("workflows/review-briefs/repository.md");
const verifierInstruction = instructions.get("workflows/review-briefs/verifier.md");
const guidance = guidanceFromInput(input);
if (!repositoryInstruction || !verifierInstruction)
  return emptyResult(input, "deterministic input omitted shipped repository or verifier brief");
if (!guidance) return emptyResult(input, "deterministic input omitted reviewed-project guidance");

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
      `The default is to run: mark a concern inapplicable only when the diff clearly gives it nothing to do; when in doubt, run it. ` +
      `Return one decision for each entry and use its brief path unchanged.\nChanged paths: ${JSON.stringify(input.changedPaths)}\n` +
      `Concerns: ${JSON.stringify(relevanceCandidates.map(({ brief, front }) => ({ brief: brief.path, relevance: front.relevance })))}`,
    { schema: relevanceSchema, model: GPT_PLANNER_MODEL, effort: "high", label: "brief-relevance", phase: "Relevance" },
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

const units = [];
for (const candidate of runnable) {
  const { brief, front, base } = candidate;
  const warning = brief.scope && !brief.scopeExists
    ? `brief scope ${brief.scope}/ matches no repository directory; ran repo-wide instead`
    : null;
  if (front.extent !== "full") {
    units.push(makeUnit(candidate, [], null, warning));
    continue;
  }
  const inventory = scopeInventory(warning ? null : brief.scope);
  const bytes = inventory.reduce((total, entry) => total + entry.bytes, 0);
  const readingVolume = bytes + inventory.length * PER_FILE_OVERHEAD_BYTES;
  if (front.sweep === "whole-tree" && readingVolume > WHOLE_TREE_READING_BUDGET_BYTES) {
    reports.set(brief.path, {
      ...base,
      status: "not-run",
      scopeSize: inventory.length,
      scopeBytes: bytes,
      readingVolume,
      reason: `whole-tree brief needs one specialist, but its scope has a weighted reading volume of ${readingVolume} bytes (${bytes} non-binary bytes plus ${inventory.length} file entries), past the ${WHOLE_TREE_READING_BUDGET_BYTES}-byte budget`,
      ...(warning ? { warning } : {}),
    });
    continue;
  }
  if (front.sweep === "per-file" && inventory.length > PER_FILE_CHUNK) {
    for (let index = 0; index < inventory.length; index += PER_FILE_CHUNK)
      units.push(makeUnit(candidate, inventory.slice(index, index + PER_FILE_CHUNK).map((entry) => entry.path), index / PER_FILE_CHUNK + 1, warning));
  } else units.push(makeUnit(candidate, inventory.map((entry) => entry.path), null, warning));
}

for (const unit of units.slice(MAX_SPECIALISTS))
  reports.set(unit.brief, { brief: unit.brief, title: unit.title, status: "not-run", reason: `repository specialists exceeded the stage budget of ${MAX_SPECIALISTS}` });
const dispatched = units.slice(0, MAX_SPECIALISTS);

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
  ), { schema: specialistSchema, model: CLAUDE_MODEL, effort: "high", label: unit.label, phase: "Review" });
}));

const reviewerStates = [];
const proposed = [];
dispatched.forEach((unit, unitIndex) => {
  const result = specialistResults[unitIndex];
  reviewerStates.push({ label: unit.label, role: "specialist", brief: unit.brief, family: "claude", pinnedModel: CLAUDE_MODEL, status: result ? "done" : "no-result" });
  if (!result) {
    reports.set(unit.brief, { brief: unit.brief, title: unit.title, status: "not-run", reason: "specialist returned no result", ...(unit.warning ? { warning: unit.warning } : {}) });
    return;
  }
  if (!reports.has(unit.brief)) reports.set(unit.brief, { brief: unit.brief, title: unit.title, status: "run", reason: "applicable concern reviewed", ...(unit.warning ? { warning: unit.warning } : {}) });
  result.findings.slice(0, MAX_FINDINGS_PER_SPECIALIST).forEach((finding, findingIndex) => proposed.push({ unit, unitIndex, finding, findingIndex }));
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
  { schema: verifierSchema, model: GPT_MODEL, effort: "high", label: item.verifyLabel, phase: "Verify" },
)));

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
  const combinedConfidence = check ? Math.round((item.finding.confidence + check.confidence) / 2) : null;
  return {
    id: `${item.unit.label}:${item.findingIndex + 1}`,
    source: item.unit.title,
    ...item.finding,
    verifierConfidence: check ? check.confidence : null,
    combinedConfidence,
    operatorAttention: verdict === "confirmed" && combinedConfidence < LOW_COMBINED_CONFIDENCE,
    verdict,
    verification,
    review: reviewEvidence,
    verify: verifyEvidence,
  };
});

const incomplete = [];
for (const report of reports.values()) if (report.status === "not-run") incomplete.push(`brief "${report.title}" was not run (${report.reason})`);
for (const state of reviewerStates) if (state.status === "no-result") incomplete.push(`specialist ${state.label} returned no result`);
for (const evidence of modelEvidence) if (!evidence.confirmed) incomplete.push(`actual model for ${evidence.label} was not confirmed as ${evidence.expectedFamily}`);
for (const finding of findings) if (finding.verdict === "no-verdict") incomplete.push(`finding "${finding.title}" has no complete verdict (${finding.verification})`);
const infrastructure = infrastructureFailure(input.runRecord);
const complete = incomplete.length === 0 && !infrastructure;
const confirmedFindings = findings.filter((finding) => finding.verdict === "confirmed");
const comments = confirmedFindings.map((finding) => ({
  path: finding.path,
  body: `**${finding.source}: ${finding.title}**\n\n${finding.explanation}\n\nSeverity: ${finding.severity}. Reviewer confidence: ${finding.confidence}. Verifier confidence: ${finding.verifierConfidence}.`,
  new_position: finding.line,
}));

return {
  reviewed: { target: input.target, head: input.head, occasion: input.occasion || null },
  stage: "present",
  status: infrastructure ? "infrastructure-failure" : complete ? "complete" : "incomplete",
  complete,
  verdictsComplete: complete,
  incomplete,
  infrastructureFailure: infrastructure,
  briefs: [...reports.values()],
  dispatches: dispatched.map(({ brief, title, label, extent, scope, files }) => ({ brief, title, label, extent, scope, files })),
  reviewers: reviewerStates,
  findings,
  confirmedFindings,
  operatorAttention: confirmedFindings.filter((finding) => finding.operatorAttention).map((finding) => ({
    finding: finding.id,
    title: finding.title,
    combinedConfidence: finding.combinedConfidence,
    threshold: LOW_COMBINED_CONFIDENCE,
  })),
  briefReview: complete && comments.length > 0 ? { verdict: "comment", body: "Repository review brief findings.", comments } : null,
  fixRequired: complete && confirmedFindings.length > 0,
  requiredModelEvidence: legs,
  modelEvidence,
  accounting: {
    specialists: dispatched.length,
    verification: proposed.length,
    total: legs.length,
    attempts: attemptAccounting(input.runRecord),
  },
};
