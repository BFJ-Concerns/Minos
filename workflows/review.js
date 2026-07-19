export const meta = {
  name: "minos-review",
  description:
    "Planned pull-request review with bounded specialists, repository briefs, and cross-family verification",
  phases: [
    { title: "Explore", detail: "produce a scoped, schema-validated review plan" },
    { title: "Specialise", detail: "run the planned concern specialists" },
    { title: "Verify", detail: "opposite-family verification of each proposed finding" },
  ],
};

const GPT_SPECIALIST_MODEL = "anthropic-gpt-5.6-sol";
const GPT_EXPLORER_MODEL = "anthropic-gpt-5.6-terra";
const CLAUDE_MODEL = "claude-opus-4-8";

const ROLE_BRIEFS = {
  exploration: "workflows/review-briefs/exploration.md",
  correctness: "workflows/review-briefs/correctness.md",
  security: "workflows/review-briefs/security.md",
  testing: "workflows/review-briefs/testing.md",
  design: "workflows/review-briefs/design.md",
  repository: "workflows/review-briefs/repository.md",
  verifier: "workflows/review-briefs/verifier.md",
};

const SPECIALIST_FAMILY = {
  correctness: "gpt",
  security: "claude",
  testing: "gpt",
  design: "claude",
};

// These caps are the workflow's stage budgets. With two findings permitted per
// specialist, no valid run can dispatch more than 32 agents in total.
const STAGE_BUDGETS = {
  exploration: 2,
  specialists: 10,
  verification: 20,
  total: 32,
};
const MAX_PLANNED_SPECIALISTS = 6;
const MAX_REPOSITORY_SPECIALISTS = STAGE_BUDGETS.specialists - MAX_PLANNED_SPECIALISTS;
const MAX_FINDINGS_PER_SPECIALIST = 2;
const LOW_COMBINED_CONFIDENCE = 70;
const PER_FILE_CHUNK = 40;
const PER_FILE_OVERHEAD_BYTES = 2_000;
const WHOLE_TREE_READING_BUDGET_BYTES = 400_000;

function familyOf(modelId) {
  if (typeof modelId !== "string" || modelId === "") return "unknown";
  const id = modelId.toLowerCase();
  if (id.includes("gpt-")) return "gpt";
  if (id.includes("claude") || ["haiku", "sonnet", "opus", "fable"].includes(id))
    return "claude";
  return "unknown";
}

function modelForFamily(family) {
  return family === "gpt" ? GPT_SPECIALIST_MODEL : CLAUDE_MODEL;
}

function slug(value) {
  return String(value).toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "review";
}

function parseFrontmatter(content) {
  const meta = { title: null, extent: "diff", sweep: "per-file", occasion: [] };
  if (typeof content !== "string") return meta;
  const match = content.match(/^---\n([\s\S]*?)\n---\n?/);
  if (!match) return meta;
  for (const line of match[1].split("\n")) {
    const kv = line.match(/^(\w+):\s*(.*)$/);
    if (!kv) continue;
    const key = kv[1].toLowerCase();
    let value = kv[2];
    if (key !== "title") value = value.replace(/#.*$/, "");
    value = value.trim();
    if (key === "title") meta.title = value;
    if (key === "extent" && value === "full") meta.extent = "full";
    if (key === "sweep" && value === "whole-tree") meta.sweep = "whole-tree";
    if (key === "occasion")
      meta.occasion = value.split(",").map((token) => token.trim()).filter(Boolean);
  }
  return meta;
}

function briefTitle(path, front) {
  if (front.title) return front.title;
  const stem = path.replace(/^.*\//, "").replace(/\.md$/, "");
  return stem.split("-").map((word) => word.charAt(0).toUpperCase() + word.slice(1)).join(" ");
}

function briefScope(path) {
  const inner = path.replace(/^\.review\//, "");
  return inner.includes("/") ? inner.replace(/\/[^/]*$/, "") : null;
}

function briefDisposition(brief, front, occasion, changedPaths) {
  if (front.occasion.length > 0) {
    if (!occasion)
      return { status: "skipped", reason: `brief applies on occasion ${front.occasion.join(", ")}; this run names none` };
    if (!front.occasion.includes(occasion))
      return { status: "skipped", reason: `brief applies on occasion ${front.occasion.join(", ")}, not ${occasion}` };
  }
  const scope = briefScope(brief.path);
  if (front.extent === "diff" && scope && brief.scopeExists !== false) {
    const inScope = changedPaths.some((path) => path === scope || path.startsWith(scope + "/"));
    if (!inScope)
      return { status: "skipped", reason: `nothing changed under its scope ${scope}/` };
  }
  return null;
}

function weightedReadingVolume(fileCount, nonBinaryBytes) {
  return nonBinaryBytes + fileCount * PER_FILE_OVERHEAD_BYTES;
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
    `The deterministic input enumerator supplied the same content below so the Workflow script can bind the dispatched prompt to the shipped brief without reading files itself.\n\n` +
    `<role-brief path="${path}">\n${brief.content}\n</role-brief>\n\n` +
    `Judge the change against the reviewed project's checked-in commission or guidance below. ` +
    `This project intent governs whether behaviour is correct.\n\n` +
    `<project-guidance grounding="${guidance.grounding}" path="${guidance.path}">\n` +
    `${guidance.content}\n</project-guidance>\n\n` + assignment
  );
}

function clampPlan(plan, files) {
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
  if (correctness) {
    ordered = [correctness, ...requested.filter((unit) => unit !== correctness)];
  } else {
    clamps.push("added the mandatory correctness specialist");
    ordered = [fallback, ...requested];
  }
  if (ordered.length > MAX_PLANNED_SPECIALISTS)
    clamps.push(`capped planned specialists at ${MAX_PLANNED_SPECIALISTS}`);
  const dispatched = ordered.slice(0, MAX_PLANNED_SPECIALISTS).map((unit, index) => {
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

function repositoryUnit(brief, front, scope, warning, files, suffix) {
  const title = briefTitle(brief.path, front);
  return {
    kind: "repository",
    concern: title,
    title,
    brief: brief.path,
    extent: front.extent,
    scope,
    files,
    family: "claude",
    roleBrief: ROLE_BRIEFS.repository,
    label: `repository-${slug(title)}${suffix ? `-${suffix}` : ""}-claude`,
    ...(warning ? { warning } : {}),
  };
}

function planRepositoryUnits(brief, front, scope, warning, scopeFiles) {
  if (front.extent === "full" && front.sweep === "per-file" && scopeFiles.length > PER_FILE_CHUNK) {
    const chunks = Math.ceil(scopeFiles.length / PER_FILE_CHUNK);
    return Array.from({ length: chunks }, (_, index) =>
      repositoryUnit(
        brief,
        front,
        scope,
        warning,
        scopeFiles.slice(index * PER_FILE_CHUNK, (index + 1) * PER_FILE_CHUNK),
        index + 1
      )
    );
  }
  return [repositoryUnit(brief, front, scope, warning, [], null)];
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
  properties: {
    findings: { type: "array", maxItems: MAX_FINDINGS_PER_SPECIALIST, items: findingShape },
  },
};

const repositorySpecialistSchema = {
  type: "object",
  additionalProperties: false,
  required: ["applicable", "reason", "findings"],
  properties: {
    applicable: { type: "boolean" },
    reason: { type: "string" },
    findings: { type: "array", maxItems: MAX_FINDINGS_PER_SPECIALIST, items: findingShape },
  },
};

const explorationSchema = {
  type: "object",
  additionalProperties: false,
  required: ["files", "plan", "briefs"],
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
    briefs: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["path", "content", "scopeExists"],
        properties: {
          path: { type: "string" },
          content: { type: "string" },
          scopeExists: { type: "boolean" },
        },
      },
    },
  },
};

const scopeFilesSchema = {
  type: "object",
  additionalProperties: false,
  required: ["scopes"],
  properties: {
    scopes: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["scope", "files", "bytes"],
        properties: {
          scope: { type: "string" },
          files: { type: "array", items: { type: "string" } },
          bytes: { type: "integer", minimum: 0 },
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

function progressRecord(runRecord, label) {
  const records = Array.isArray(runRecord && runRecord.workflowProgress)
    ? runRecord.workflowProgress
    : [];
  return records.find((record) => record && record.type === "workflow_agent" && record.label === label) || null;
}

function actualModelEvidence(runRecord, leg) {
  const record = progressRecord(runRecord, leg.label);
  const actualModel = record && record.state === "done"
    ? (record.fallbackModel || record.model || null)
    : null;
  const actualFamily = familyOf(actualModel);
  return {
    label: leg.label,
    role: leg.role,
    expectedFamily: leg.expectedFamily,
    pinnedModel: leg.pinnedModel,
    actualModel,
    actualFamily,
    state: record ? record.state : "absent",
    confirmed: Boolean(record && record.state === "done" && actualFamily === leg.expectedFamily),
  };
}

function infrastructureFailure(runRecord) {
  const raw = runRecord && runRecord.terminal;
  const terminal = typeof raw === "string"
    ? raw
    : raw && typeof raw === "object"
      ? String(raw.status || raw.state || raw.reason || "")
      : "";
  const normalised = terminal.toLowerCase();
  if (normalised.includes("oom") || normalised.includes("out-of-memory") || normalised.includes("out of memory"))
    return { kind: "oom", detail: terminal };
  if (normalised.includes("cancel") || normalised.includes("killed"))
    return { kind: "cancelled", detail: terminal };
  return null;
}

function attemptAccounting(runRecord) {
  const explicit = Array.isArray(runRecord && runRecord.attempts) ? runRecord.attempts : [];
  const progress = Array.isArray(runRecord && runRecord.workflowProgress)
    ? runRecord.workflowProgress.filter((record) => record && record.type === "workflow_agent")
    : [];
  return {
    attempts: explicit,
    legs: progress.map((record) => ({
      label: record.label || null,
      state: record.state || "unknown",
      attempt: Number.isInteger(record.attempt) ? record.attempt : 1,
      lastAttemptReason: record.lastAttemptReason || null,
    })),
  };
}

function emptyResult(target, head, occasion, fault) {
  return {
    reviewed: { target: target || null, head: head || null, occasion: occasion || null },
    status: "incomplete",
    complete: false,
    verdictsComplete: false,
    incomplete: [fault],
    infrastructureFailure: null,
    plan: { requested: [], dispatched: [], clamps: [] },
    reviewers: [],
    briefs: [],
    findings: [],
    confirmedFindings: [],
    operatorAttention: [],
    requiredModelEvidence: [],
    modelEvidence: [],
    accounting: {
      budgets: {
        exploration: { maximum: STAGE_BUDGETS.exploration, used: 0 },
        specialists: { maximum: STAGE_BUDGETS.specialists, used: 0 },
        verification: { maximum: STAGE_BUDGETS.verification, used: 0 },
        total: { maximum: STAGE_BUDGETS.total, used: 0 },
      },
      attempts: { attempts: [], legs: [] },
    },
  };
}

const input = args && typeof args === "object" ? args : null;
const target = input && typeof input.target === "string" ? input.target : null;
const head = input && typeof input.head === "string" ? input.head : null;
const occasion = input && typeof input.occasion === "string" && input.occasion !== "" ? input.occasion : null;
if (!target || !head)
  return emptyResult(target, head, occasion, "review workflow needs args {target, head, guidance, instructionBriefs}");

const roleBriefs = roleBriefsFromInput(input);
const missingBriefs = missingRoleBriefs(roleBriefs);
if (missingBriefs.length > 0)
  return emptyResult(target, head, occasion, `deterministic input omitted shipped role briefs: ${missingBriefs.join(", ")}`);
const projectGuidance = projectGuidanceFromInput(input);
if (!projectGuidance)
  return emptyResult(target, head, occasion, "deterministic input omitted reviewed-project guidance");

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
    `Review ${target}...${head}. Return the change inventory, a partitioned review plan, and the repository's .review/ Markdown briefs in the requested schema.`
  ),
  { schema: explorationSchema, model: GPT_EXPLORER_MODEL, effort: "high", label: "exploration", phase: "Explore" }
);

if (!exploration) {
  const result = emptyResult(target, head, occasion, "the exploration stage returned no result, so no specialists ran");
  result.reviewers = [{ label: "exploration", role: "exploration", status: "no-result" }];
  result.requiredModelEvidence = legs;
  result.modelEvidence = legs.map((leg) => actualModelEvidence(input.runRecord, leg));
  result.accounting.budgets.exploration.used = 1;
  result.accounting.budgets.total.used = 1;
  result.accounting.attempts = attemptAccounting(input.runRecord);
  return result;
}

const planned = clampPlan(exploration.plan, exploration.files);
const changedPaths = exploration.files.map((file) => file.path);
const briefReports = [];
const repositoryUnits = [];
const fullExtentBriefs = [];

for (const brief of exploration.briefs) {
  const front = parseFrontmatter(brief.content);
  const skip = briefDisposition(brief, front, occasion, changedPaths);
  if (skip) {
    briefReports.push({ brief: brief.path, title: briefTitle(brief.path, front), ...skip });
    continue;
  }
  const rawScope = briefScope(brief.path);
  const scopeMissing = Boolean(rawScope) && brief.scopeExists === false;
  const scope = scopeMissing ? null : rawScope;
  const warning = scopeMissing
    ? `brief scope ${rawScope}/ matches no repository directory; ran repo-wide instead`
    : null;
  if (front.extent === "full") fullExtentBriefs.push({ brief, front, scope, warning });
  else repositoryUnits.push(...planRepositoryUnits(brief, front, scope, warning, []));
}

if (fullExtentBriefs.length > 0) {
  const wanted = [...new Set(fullExtentBriefs.map(({ scope }) => scope || "."))];
  addLeg("scope-files", "exploration", "gpt", GPT_EXPLORER_MODEL);
  const scopes = await agent(
    rolePrompt(
      roleBriefs,
      projectGuidance,
      ROLE_BRIEFS.exploration,
      `Inventory tracked files and summed non-binary bytes for these scopes: ${wanted.join(", ")}. A scope of "." means the whole repository.`
    ),
    { schema: scopeFilesSchema, model: GPT_EXPLORER_MODEL, effort: "low", label: "scope-files", phase: "Explore" }
  );
  for (const { brief, front, scope, warning } of fullExtentBriefs) {
    const inventoryScope = scope || ".";
    const where = inventoryScope === "." ? "the whole repository" : `${inventoryScope}/`;
    const entry = scopes && scopes.scopes.find((candidate) => candidate.scope === inventoryScope);
    if (!entry) {
      briefReports.push({
        brief: brief.path,
        title: briefTitle(brief.path, front),
        status: "not-run",
        reason: `measured scope unavailable: the file inventory for ${where} returned no result`,
        ...(warning ? { warning } : {}),
      });
      continue;
    }
    const readingVolume = weightedReadingVolume(entry.files.length, entry.bytes);
    if (front.sweep === "whole-tree" && readingVolume > WHOLE_TREE_READING_BUDGET_BYTES) {
      briefReports.push({
        brief: brief.path,
        title: briefTitle(brief.path, front),
        status: "not-run",
        scopeSize: entry.files.length,
        scopeBytes: entry.bytes,
        readingVolume,
        reason: `whole-tree brief needs one specialist, but ${where} has a weighted reading volume of ${readingVolume} bytes (${entry.bytes} non-binary bytes plus ${entry.files.length} file entries), past the ${WHOLE_TREE_READING_BUDGET_BYTES}-byte budget`,
        ...(warning ? { warning } : {}),
      });
      continue;
    }
    repositoryUnits.push(...planRepositoryUnits(brief, front, scope, warning, entry.files));
  }
}

if (repositoryUnits.length > MAX_REPOSITORY_SPECIALISTS) {
  const omitted = repositoryUnits.slice(MAX_REPOSITORY_SPECIALISTS);
  for (const unit of omitted)
    briefReports.push({
      brief: unit.brief,
      title: unit.title,
      status: "not-run",
      reason: `repository specialists exceeded the stage budget of ${MAX_REPOSITORY_SPECIALISTS}`,
      ...(unit.warning ? { warning: unit.warning } : {}),
    });
}

const specialistUnits = [...planned.dispatched, ...repositoryUnits.slice(0, MAX_REPOSITORY_SPECIALISTS)];
phase("Specialise");
for (const unit of specialistUnits)
  addLeg(unit.label, "specialist", unit.family, modelForFamily(unit.family));

function specialistPrompt(unit) {
  if (unit.kind === "planned") {
    return rolePrompt(
      roleBriefs,
      projectGuidance,
      unit.roleBrief,
      `Assigned concern: ${unit.concern}\nAssigned specialist type: ${unit.specialistType}\nAssigned scope: ${unit.scope.join(", ")}\nReview only that concern and scope against ${target}...${head}.`
    );
  }
  const files = unit.files && unit.files.length > 0 ? `\nAssigned files: ${unit.files.join(", ")}` : "";
  const scope = unit.scope ? `${unit.scope}/` : "the whole repository";
  return rolePrompt(
    roleBriefs,
    projectGuidance,
    unit.roleBrief,
    `Read the reviewed repository's concern brief at ${unit.brief}. Assigned scope: ${scope}.${files}\n` +
      (unit.extent === "full"
        ? "Audit the assigned scope regardless of what the diff changed."
        : `Judge only what ${target}...${head} changed in the assigned scope.`)
  );
}

const specialistResults = await parallel(
  specialistUnits.map((unit) => () =>
    agent(specialistPrompt(unit), {
      schema: unit.kind === "repository" ? repositorySpecialistSchema : specialistSchema,
      model: modelForFamily(unit.family),
      effort: "high",
      label: unit.label,
      phase: "Specialise",
    })
  )
);

const reviewerStates = [];
const proposed = [];
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
  if (!result) {
    if (unit.kind === "repository")
      briefReports.push({
        brief: unit.brief,
        title: unit.title,
        status: "not-run",
        reason: "specialist returned no result",
        ...(unit.warning ? { warning: unit.warning } : {}),
      });
    return;
  }
  if (unit.kind === "repository") {
    if (!result.applicable) {
      briefReports.push({
        brief: unit.brief,
        title: unit.title,
        status: "skipped",
        reason: result.reason,
        ...(unit.warning ? { warning: unit.warning } : {}),
      });
      return;
    }
    briefReports.push({
      brief: unit.brief,
      title: unit.title,
      status: "run",
      reason: result.reason,
      ...(unit.warning ? { warning: unit.warning } : {}),
    });
  }
  result.findings.slice(0, MAX_FINDINGS_PER_SPECIALIST).forEach((finding, findingIndex) => {
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
        schema: verifierSchema,
        model: modelForFamily(item.expectedFamily),
        effort: "high",
        label: item.verifyLabel,
        phase: "Verify",
      }
    )
  )
);

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
  const combinedConfidence = check
    ? Math.round((item.finding.confidence + check.confidence) / 2)
    : null;
  const operatorAttention = verdict === "confirmed" && combinedConfidence < LOW_COMBINED_CONFIDENCE;
  return {
    id: `${item.unit.label}:${item.findingIndex + 1}`,
    source: item.unit.kind === "repository" ? item.unit.title : item.unit.concern,
    ...item.finding,
    verifierConfidence: check ? check.confidence : null,
    combinedConfidence,
    operatorAttention,
    verdict,
    verification,
    review: reviewEvidence,
    verify: verifyEvidence,
  };
});

const incomplete = [];
for (const state of reviewerStates)
  if (state.status === "no-result") incomplete.push(`specialist ${state.label} returned no result`);
for (const report of briefReports)
  if (report.status === "not-run") incomplete.push(`brief "${report.title}" was not run (${report.reason})`);
for (const evidence of modelEvidence)
  if (!evidence.confirmed)
    incomplete.push(`actual model for ${evidence.label} was not confirmed as ${evidence.expectedFamily}`);
for (const finding of findings)
  if (finding.verdict === "no-verdict")
    incomplete.push(`finding "${finding.title}" has no complete verdict (${finding.verification})`);

const infrastructure = infrastructureFailure(input.runRecord);
const complete = incomplete.length === 0 && !infrastructure;
const status = infrastructure ? "infrastructure-failure" : complete ? "complete" : "incomplete";
const confirmedFindings = findings.filter((finding) => finding.verdict === "confirmed");
const operatorAttention = confirmedFindings
  .filter((finding) => finding.operatorAttention)
  .map((finding) => ({
    finding: finding.id,
    title: finding.title,
    combinedConfidence: finding.combinedConfidence,
    threshold: LOW_COMBINED_CONFIDENCE,
  }));
const used = legs.length;

return {
  reviewed: { target, head, occasion },
  status,
  complete,
  verdictsComplete: complete,
  incomplete,
  infrastructureFailure: infrastructure,
  plan: {
    requested: planned.requested,
    dispatched: planned.dispatched.map((unit) => ({
      id: unit.id,
      label: unit.label,
      concern: unit.concern,
      scope: unit.scope,
      specialistType: unit.specialistType,
      family: unit.family,
      roleBrief: unit.roleBrief,
    })),
    clamps: planned.clamps,
  },
  reviewers: reviewerStates,
  briefs: briefReports,
  findings,
  confirmedFindings,
  operatorAttention,
  requiredModelEvidence: legs,
  modelEvidence,
  accounting: {
    budgets: {
      exploration: { maximum: STAGE_BUDGETS.exploration, used: fullExtentBriefs.length > 0 ? 2 : 1 },
      specialists: { maximum: STAGE_BUDGETS.specialists, used: specialistUnits.length },
      verification: { maximum: STAGE_BUDGETS.verification, used: proposed.length },
      total: { maximum: STAGE_BUDGETS.total, used },
    },
    attempts: attemptAccounting(input.runRecord),
  },
};
