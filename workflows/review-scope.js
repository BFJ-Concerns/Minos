export const meta = {
  name: "minos-review-scope",
  description: "Lightweight engagement gate ahead of the full pull-request review",
  phases: [
    { title: "Scope", detail: "judge whether the change engages any review work" },
  ],
};

const GATE_MODEL = "gpt-6-sol";
const SCOPE_BRIEF_PATH = "workflows/review-briefs/scope.md";

const scopeSchema = {
  type: "object",
  additionalProperties: false,
  required: ["decision", "reason"],
  properties: {
    decision: { type: "string", enum: ["nothing-engages", "review-required"] },
    reason: { type: "string" },
  },
};

const input = args && typeof args === "object" && !Array.isArray(args) ? args : null;
const target = input && typeof input.target === "string" ? input.target : null;
const head = input && typeof input.head === "string" ? input.head : null;
const occasion = input && typeof input.occasion === "string" && input.occasion !== "" ? input.occasion : null;
if (!target || !head)
  throw new Error("review-scope workflow needs args {target, head, guidance, instructionBriefs, changedFiles, briefsEngage, briefDispositions}");

const scopeBrief = (Array.isArray(input.instructionBriefs) ? input.instructionBriefs : []).find(
  (entry) =>
    entry &&
    entry.path === SCOPE_BRIEF_PATH &&
    typeof entry.readPath === "string" &&
    typeof entry.content === "string"
);
if (!scopeBrief) throw new Error("deterministic input omitted the shipped scope brief");

const guidance = input.guidance;
if (
  !guidance ||
  typeof guidance.path !== "string" ||
  guidance.path === "" ||
  typeof guidance.content !== "string" ||
  guidance.content.trim() === "" ||
  typeof guidance.grounding !== "string"
) throw new Error("deterministic input omitted reviewed-project guidance");

if (typeof input.briefsEngage !== "boolean")
  throw new Error("deterministic input omitted the brief engagement pre-branch");
if (!Array.isArray(input.briefDispositions) || !Array.isArray(input.briefMisconfigurations))
  throw new Error("deterministic brief enumeration is incomplete");
const changedFiles = Array.isArray(input.changedFiles) ? input.changedFiles : null;
if (!changedFiles || changedFiles.some((entry) =>
  !entry ||
  typeof entry.path !== "string" ||
  !Number.isInteger(entry.added) ||
  !Number.isInteger(entry.deleted)
)) throw new Error("deterministic input omitted the changed-file inventory");

const settledSkips = input.briefDispositions.filter((entry) => entry && entry.status === "skipped");

function envelope(legs, reviewers, scopeDecision) {
  return {
    reviewed: { target, head, occasion },
    stage: "present",
    requiredModelEvidence: legs,
    proposedFindings: [],
    outOfScopeObservations: [],
    briefs: settledSkips,
    misconfigurations: input.briefMisconfigurations,
    dispatches: [],
    reviewers,
    scopeDecision,
  };
}

// The lead only launches this workflow when no repository brief engages; an
// engaged brief already commits the run to the full pipeline, so answer
// review-required without spending a leg.
if (input.briefsEngage) {
  return envelope([], [], {
    status: "review-required",
    reason: "a repository review brief engages this change",
  });
}

const legs = [{ label: "review-scope", role: "scope", pinnedModel: GATE_MODEL }];

phase("Scope");
const result = await agent(
  `Read and follow the Markdown role brief at ${scopeBrief.readPath}. ` +
    `The deterministic input enumerator supplied the same content below so this workflow can bind the dispatched prompt to the shipped brief without reading files itself.\n\n` +
    `<role-brief path="${SCOPE_BRIEF_PATH}">\n${scopeBrief.content}\n</role-brief>\n\n` +
    `Judge the change against the reviewed project's checked-in commission or guidance below. ` +
    `This project intent governs whether behaviour is correct.\n\n` +
    `<project-guidance grounding="${guidance.grounding}" path="${guidance.path}">\n` +
    `${guidance.content}\n</project-guidance>\n\n` +
    `Judge ${target}...${head}. Changed files: ${JSON.stringify(changedFiles)}\n` +
    `Every repository review brief has already settled deterministically as not applying to this change. ` +
    `Decide only whether the change itself gives any reviewable concern real work.`,
  {
    engine: "codex",
    schema: scopeSchema,
    model: GATE_MODEL,
    effort: "medium",
    label: "review-scope",
    phase: "Scope",
  }
);

if (!result) {
  return envelope(
    legs,
    [{ label: "review-scope", role: "scope", status: "no-result" }],
    { status: "review-required", reason: "scope leg returned no result" }
  );
}

return envelope(
  legs,
  [{ label: "review-scope", role: "scope", status: "done" }],
  { status: result.decision, reason: result.reason }
);
