export const meta = {
  name: "minos-review",
  description:
    "Independent pull-request review with diff-scaled breadth, .review/ briefs, and cross-family verification",
  phases: [
    { title: "Scout", detail: "diff shape, .review/ briefs, full-extent scopes" },
    { title: "Review", detail: "independent reviewers over the target→head diff" },
    { title: "Verify", detail: "other-family verification of each proposed finding" },
  ],
};

// Model families ride through the proxy as literal ids. A pinned id that the
// proxy cannot serve silently falls back to the session model (observed live
// 2026-07-17), so every verify leg records what family it expected; the lead
// confirms actual models against the workflow run record, which names them.
//
// The lead's session model is GPT (gpt-5.6-sol), so an unpinned agent inherits
// GPT — inheritance cannot produce the Claude family. Every leg is therefore
// pinned to its family's id explicitly: relying on the session model would put
// both families on GPT and collapse cross-family verification.
const GPT_REVIEW_MODEL = "gpt-5.6-sol";
const GPT_VERIFY_MODEL = "gpt-5.6-sol";
const CLAUDE_MODEL = "claude-opus-4-8";
const SCOUT_MODEL = "gpt-5.6-terra";

// Every review or verify leg pins the model for its family, never inheriting.
function modelForFamily(family) {
  return family === "gpt" ? GPT_REVIEW_MODEL : CLAUDE_MODEL;
}

function familyOf(modelId) {
  if (typeof modelId !== "string" || modelId === "") return "unknown";
  const id = modelId.toLowerCase();
  if (id.startsWith("gpt-")) return "gpt";
  if (id.startsWith("claude") || ["haiku", "sonnet", "opus", "fable"].includes(id))
    return "claude";
  return "unknown";
}

// --- .review/ brief handling (frontmatter contract per the operator's
// review-brief conventions: title, extent, sweep, occasion) ---

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
  return stem.split("-").map((w) => w.charAt(0).toUpperCase() + w.slice(1)).join(" ");
}

function briefScope(path) {
  const inner = path.replace(/^\.review\//, "");
  return inner.includes("/") ? inner.replace(/\/[^/]*$/, "") : null;
}

// Decide, without dispatching an agent, whether a brief runs on this diff.
// Returns null to run it, or {status:"skipped", reason} — a skipped concern is
// reported as skipped, never as passed.
function briefDisposition(brief, front, occasion, changedPaths) {
  if (front.occasion.length > 0) {
    if (!occasion)
      return { status: "skipped", reason: `brief applies on occasion ${front.occasion.join(", ")}; this run names none` };
    if (!front.occasion.includes(occasion))
      return { status: "skipped", reason: `brief applies on occasion ${front.occasion.join(", ")}, not ${occasion}` };
  }
  const scope = briefScope(brief.path);
  if (front.extent === "diff" && scope && brief.scopeExists !== false) {
    const inScope = changedPaths.some((p) => p === scope || p.startsWith(scope + "/"));
    if (!inScope)
      return { status: "skipped", reason: `nothing changed under its scope ${scope}/` };
  }
  return null;
}

// --- diff-scaled reviewer fan-out ---

const ASPECTS = [
  ["gpt", "Focus on correctness, edge cases, and error handling."],
  ["claude", "Focus on security and unsafe trust assumptions."],
  ["gpt", "Focus on tests and behaviour that the change fails to cover."],
  ["claude", "Focus on unnecessary complexity and simpler equivalent designs."],
];

function planAspectReviewers(files) {
  const changedLines = files.reduce((n, f) => n + (f.added || 0) + (f.deleted || 0), 0);
  const aspects = changedLines <= 40 ? ASPECTS.slice(0, 2) : ASPECTS.slice();
  const plan = aspects.map(([family, focus], i) => ({
    kind: "aspect",
    label: `review-${i + 1}-${family}`,
    family,
    focus,
  }));
  // A large diff gets extra correctness passes, each over a partition of the
  // changed files, so no reviewer skims.
  const extra = Math.min(4, Math.max(0, Math.ceil((changedLines - 600) / 800)));
  if (extra > 0) {
    const paths = files.map((f) => f.path);
    const per = Math.ceil(paths.length / extra);
    for (let i = 0; i < extra; i++) {
      const family = i % 2 === 0 ? "gpt" : "claude";
      plan.push({
        kind: "aspect",
        label: `review-${plan.length + 1}-${family}`,
        family,
        focus:
          "Focus on correctness, edge cases, and error handling, concentrating on these changed files: " +
          paths.slice(i * per, (i + 1) * per).join(", "),
      });
    }
  }
  return plan;
}

const PER_FILE_CHUNK = 40;

// A whole-tree brief needs one reviewer holding its entire scope at once (its
// verdict is about the relationship between files), so it is never split. Past
// what one reviewer can genuinely hold it would skim rather than judge, so the
// brief is reported not run — with the measured scope — rather than run badly.
//
// Reviewer reading budget (deliberately conservative and legible, no framework):
// one measure, not two thresholds. Neither raw file count nor raw byte volume
// alone is defensible — a hard file cap refuses 61 trivially small files, and
// bytes alone ignore that each extra file costs navigation. So we weight the
// two into a single reading volume: the scope's non-binary bytes plus a modest
// per-file overhead standing for the file's name, structure and its place in
// the cross-file relationships a whole-tree judgement rests on. Refuse only when
// that combined volume exceeds one budget.
//
// PER_FILE_OVERHEAD_BYTES ≈ the span of a short file's shape (path, imports,
// signatures) a reviewer must hold just to navigate it. READING_BUDGET_BYTES is
// a conservative working ceiling on the total the one reviewer holds at once.
// So 61 tiny files (≈0.12 MB weighted) run, while a few huge files or a
// genuinely large tree (both well past the budget) are reported not run.
const PER_FILE_OVERHEAD_BYTES = 2_000;
const WHOLE_TREE_READING_BUDGET_BYTES = 400_000;

function weightedReadingVolume(fileCount, nonBinaryBytes) {
  return nonBinaryBytes + fileCount * PER_FILE_OVERHEAD_BYTES;
}

// `scope` is the resolved review scope: the brief's subfolder when it matches a
// real repository directory, or null (repo-wide) when it does not — never the
// nonexistent path. `warning` carries the ghost-scope note into the report.
function planBriefReviewers(brief, front, scope, warning, scopeFiles) {
  const title = briefTitle(brief.path, front);
  const base = {
    kind: "brief",
    brief: brief.path,
    title,
    family: "claude",
    extent: front.extent,
    scope,
    ...(warning ? { warning } : {}),
  };
  if (front.extent === "full" && front.sweep === "per-file" && scopeFiles.length > PER_FILE_CHUNK) {
    const chunks = Math.ceil(scopeFiles.length / PER_FILE_CHUNK);
    return Array.from({ length: chunks }, (_, i) => ({
      ...base,
      label: `brief-${title.toLowerCase().replace(/[^a-z0-9]+/g, "-")}-${i + 1}`,
      files: scopeFiles.slice(i * PER_FILE_CHUNK, (i + 1) * PER_FILE_CHUNK),
    }));
  }
  return [{ ...base, label: `brief-${title.toLowerCase().replace(/[^a-z0-9]+/g, "-")}` }];
}

// --- schemas ---

const findingShape = {
  type: "object",
  additionalProperties: false,
  required: ["title", "priority", "path", "line", "explanation"],
  properties: {
    title: { type: "string" },
    priority: { type: "string", enum: ["critical", "high", "medium", "low"] },
    path: { type: "string" },
    line: { type: "integer", minimum: 1 },
    explanation: { type: "string" },
  },
};

// Reviewers self-report their model too: cross-family independence binds the
// proposer's actual family as much as the verifier's, so a review leg that
// silently fell back to the session model must be catchable. Self-report is
// corroboration only — the lead confirms actual models against the run record.
const aspectReviewSchema = {
  type: "object",
  additionalProperties: false,
  required: ["findings", "selfReportedModel"],
  properties: {
    findings: { type: "array", items: findingShape },
    selfReportedModel: { type: "string" },
  },
};

const briefReviewSchema = {
  type: "object",
  additionalProperties: false,
  required: ["applicable", "reason", "findings", "selfReportedModel"],
  properties: {
    applicable: { type: "boolean" },
    reason: { type: "string" },
    findings: { type: "array", items: findingShape },
    selfReportedModel: { type: "string" },
  },
};

const scoutSchema = {
  type: "object",
  additionalProperties: false,
  required: ["files", "briefs"],
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

const verdictSchema = {
  type: "object",
  additionalProperties: false,
  required: ["confirmed", "reason", "selfReportedModel"],
  properties: {
    confirmed: { type: "boolean" },
    reason: { type: "string" },
    selfReportedModel: { type: "string" },
  },
};

// --- verdict assembly: fail closed ---
// The cross-family join binds BOTH actual families: a finding is only a
// publishable verdict when its proposer and its verifier really ran on
// different families. Self-report is corroboration only — a fallen-back agent
// inherits the GPT session model and may sincerely self-report the wrong
// family — so a self-report that contradicts the pinned family downgrades the
// verdict to no-verdict here, and positive confirmation stays the lead's job,
// against the actual models the Workflow run record names per label. A reviewer
// (proposer) fallback cannot yield a publishable verdict any more than a
// verifier fallback can.
function verdictFor(reviewFamily, reviewerReportedModel, check, expectedFamily) {
  const proposer = familyOf(reviewerReportedModel);
  if (proposer !== reviewFamily)
    return {
      verdict: "no-verdict",
      verification: `reviewer self-reported ${reviewerReportedModel || "no model"}, not its pinned ${reviewFamily} family — treated as fallen back; a reviewer fallback cannot yield a publishable verdict`,
    };
  if (!check) return { verdict: "no-verdict", verification: "verifier returned no result" };
  const verifier = familyOf(check.selfReportedModel);
  if (verifier !== expectedFamily)
    return {
      verdict: "no-verdict",
      verification: `verifier self-reported ${check.selfReportedModel || "no model"}, not the pinned ${expectedFamily} family — treated as fallen back`,
    };
  if (proposer === verifier)
    return {
      verdict: "no-verdict",
      verification: `proposer and verifier both self-reported the ${proposer} family — no cross-family check`,
    };
  return {
    verdict: check.confirmed === true ? "confirmed" : "not-confirmed",
    verification: check.reason,
  };
}

function assembleResult({ target, head, occasion, reviewerStates, briefReports, findings, faults }) {
  // verdictsComplete answers only what this script can determine from inside the
  // run: every reviewer returned a result and every proposed finding got a
  // verdict whose verifier self-reported a consistent family. It is NOT the
  // final run-complete judgement — self-report is corroboration, not proof of
  // the actual served model. The lead confirms actual models against the
  // Workflow run/progress record (see modelCheck) and only then decides the run
  // complete. A concern that could not be evaluated (a not-run brief) also
  // withholds verdict-completeness, since its verdicts never existed to check.
  const incomplete = [...(faults || [])];
  for (const r of reviewerStates)
    if (r.status === "no-result") incomplete.push(`reviewer ${r.label} returned no result`);
  for (const b of briefReports)
    if (b.status === "not-run") incomplete.push(`brief "${b.title}" was not run (${b.reason})`);
  for (const f of findings)
    if (f.verdict === "no-verdict")
      incomplete.push(`finding "${f.title}" has no verdict (${f.verification})`);
  return {
    reviewed: { target, head, occasion: occasion || null },
    verdictsComplete: incomplete.length === 0,
    incomplete,
    modelCheck:
      "verdictsComplete rests on self-reports, which are corroboration only. Before any clean status or approval, confirm in the Workflow run/progress record the actual model of BOTH legs behind each finding: the review leg (finding.review.{label,configuredFamily,pinnedModel}) and its verify leg (finding.verify.{label,expectedFamily,pinnedModel}). A finding is a publishable verdict only when the two legs' actual families are both confirmed and differ; a review leg that fell back to the GPT session model, or a verify leg on the wrong or unnamed family, makes that verdict no-verdict and the run incomplete regardless of verdictsComplete.",
    reviewers: reviewerStates,
    briefs: briefReports,
    findings,
  };
}

// --- run ---

if (!args || typeof args.target !== "string" || typeof args.head !== "string") {
  return assembleResult({
    target: args && args.target,
    head: args && args.head,
    occasion: args && args.occasion,
    reviewerStates: [],
    briefReports: [],
    findings: [],
    faults: ["review workflow needs args {target, head}; it received neither"],
  });
}

const target = args.target;
const head = args.head;
const occasion = typeof args.occasion === "string" && args.occasion !== "" ? args.occasion : null;

phase("Scout");
const scout = await agent(
  `In the current repository, report the shape of the diff ${target}...${head} and the .review/ briefs. ` +
    `Run \`git diff --numstat ${target}...${head}\` and return each changed file with its added and deleted line counts (0 for binary files). ` +
    `List every .md file under .review/ at the repository root (empty list if the directory does not exist) and return each file's path and full content unmodified. ` +
    `For each brief in a subfolder (e.g. .review/src/api/x.md), set scopeExists to whether the subfolder path (src/api) exists as a directory in the repository; for briefs directly in .review/, set scopeExists to true. ` +
    `Report only what git and the filesystem actually say.`,
  { schema: scoutSchema, model: SCOUT_MODEL, effort: "low", label: "scout", phase: "Scout" }
);

if (!scout) {
  return assembleResult({
    target, head, occasion,
    reviewerStates: [{ label: "scout", family: "gpt", status: "no-result" }],
    briefReports: [],
    findings: [],
    faults: ["the scout returned no result, so no review ran"],
  });
}

const changedPaths = scout.files.map((f) => f.path);
const briefReports = [];
const briefUnits = [];
const fullExtentBriefs = [];

for (const brief of scout.briefs) {
  const front = parseFrontmatter(brief.content);
  const skip = briefDisposition(brief, front, occasion, changedPaths);
  if (skip) {
    briefReports.push({ brief: brief.path, title: briefTitle(brief.path, front), ...skip });
    continue;
  }
  // A subfolder scope that matches no real repository directory is a ghost: the
  // brief still runs, but repo-wide, and the effective scope becomes null so the
  // nonexistent path never reaches the reviewer prompt. The warning carries the
  // fact into the report.
  const rawScope = briefScope(brief.path);
  const scopeMissing = Boolean(rawScope) && brief.scopeExists === false;
  const scope = scopeMissing ? null : rawScope;
  const warning = scopeMissing
    ? `brief scope ${rawScope}/ matches no repository directory; ran repo-wide instead`
    : null;
  if (front.extent === "full") fullExtentBriefs.push({ brief, front, scope, warning });
  else briefUnits.push(...planBriefReviewers(brief, front, scope, warning, []));
}

if (fullExtentBriefs.length > 0) {
  const wanted = [...new Set(fullExtentBriefs.map(({ scope }) => scope || "."))];
  const scopes = await agent(
    `In the current repository, inventory the tracked files for each of these scopes with \`git ls-files\`: ${wanted.join(", ")}. ` +
      `A scope of "." means the whole repository. For each scope return its file list exactly as git reports it, and \`bytes\`: the summed size of those files that are not binary (skip binary files, count their text bytes). ` +
      `Report only what git and the filesystem actually say.`,
    { schema: scopeFilesSchema, model: SCOUT_MODEL, effort: "low", label: "scope-files", phase: "Scout" }
  );
  for (const { brief, front, scope, warning } of fullExtentBriefs) {
    const invScope = scope || ".";
    const where = invScope === "." ? "the whole repository" : `${invScope}/`;
    const entry = scopes && scopes.scopes.find((s) => s.scope === invScope);
    // No inventory means the scope could not be measured. EVERY full-extent brief
    // plans off this inventory — whole-tree to size the single reviewer, per-file
    // to shard — so an unmeasured scope is reported not run and no reviewer is
    // dispatched, rather than run blind over an unknown scope (a per-file brief
    // would otherwise silently collapse to one unsharded pass).
    if (!entry) {
      briefReports.push({
        brief: brief.path,
        title: briefTitle(brief.path, front),
        status: "not-run",
        reason: `measured scope unavailable: the file inventory for ${where} returned no result, so the scope could not be sized; reported not run rather than reviewed blind`,
        ...(warning ? { warning } : {}),
      });
      continue;
    }
    const scopeFiles = entry.files;
    const scopeBytes = typeof entry.bytes === "number" ? entry.bytes : 0;
    const readingVolume = weightedReadingVolume(scopeFiles.length, scopeBytes);
    if (front.sweep === "whole-tree" && readingVolume > WHOLE_TREE_READING_BUDGET_BYTES) {
      briefReports.push({
        brief: brief.path,
        title: briefTitle(brief.path, front),
        status: "not-run",
        scopeSize: scopeFiles.length,
        scopeBytes,
        readingVolume,
        reason: `whole-tree brief needs one reviewer to hold its entire scope at once, but ${where} measures ${scopeFiles.length} files / ${scopeBytes} non-binary bytes — a weighted reading volume of ${readingVolume} bytes (${scopeBytes} + ${scopeFiles.length} × ${PER_FILE_OVERHEAD_BYTES} per-file overhead), past the ${WHOLE_TREE_READING_BUDGET_BYTES}-byte budget; reported not run rather than skimmed`,
        ...(warning ? { warning } : {}),
      });
      continue;
    }
    briefUnits.push(...planBriefReviewers(brief, front, scope, warning, scopeFiles));
  }
}

const units = [...planAspectReviewers(scout.files), ...briefUnits];
const briefContent = new Map(scout.briefs.map((b) => [b.path, b.content]));

function reviewerPrompt(unit) {
  const common = `Review the git diff ${target}...${head} in the current repository. Read relevant files in full. Report only specific defects worth fixing, with file path and line. `;
  if (unit.kind === "aspect")
    return (
      common +
      `Report only defects introduced by this change. ${unit.focus} Return {findings:[{title,priority,path,line,explanation}],selfReportedModel:<the exact model id you are running as>}.`
    );
  const body = briefContent.get(unit.brief) || "";
  const where =
    unit.extent === "full"
      ? `Audit the whole scope ${unit.scope ? unit.scope + "/" : "(the whole repository)"} against this brief, regardless of what the diff changed.` +
        (unit.files ? ` Confine this pass to these files: ${unit.files.join(", ")}.` : "")
      : `Judge only what the diff ${target}...${head} changed${unit.scope ? ` under ${unit.scope}/` : ""} against this brief. ` +
        `First decide whether this diff gives the brief's concern anything to do — a change that cannot bear on the concern (for example a version-only bump against a code-quality concern) does not. ` +
        `If it does not, return applicable:false with the reason and no findings.`;
  return (
    `You are one reviewer applying a standing review brief from the repository's .review/ directory. ${where}\n\n` +
    `The brief:\n\n${body}\n\n` +
    common +
    `Return {applicable:boolean,reason:string,findings:[{title,priority,path,line,explanation}],selfReportedModel:<the exact model id you are running as>}.`
  );
}

const reviewerStates = [];
const findings = [];

const unitResults = await pipeline(
  units,
  (unit) =>
    agent(reviewerPrompt(unit), {
      schema: unit.kind === "aspect" ? aspectReviewSchema : briefReviewSchema,
      model: modelForFamily(unit.family),
      label: unit.label,
      phase: "Review",
    }).then((review) => ({ unit, review })),
  ({ unit, review }) => {
    if (!review || (unit.kind === "brief" && review.applicable === false)) return { unit, review, checks: [] };
    const expectedFamily = unit.family === "gpt" ? "claude" : "gpt";
    return parallel(
      review.findings.map((finding, i) => () =>
        agent(
          `Independently try to disprove this proposed finding against the repository and the diff ${target}...${head}. ` +
            `Confirm it only if the cited code demonstrates a real, worth-fixing defect. ` +
            `Return {confirmed:boolean,reason:string,selfReportedModel:<the exact model id you are running as>}. ` +
            `Finding: ${JSON.stringify(finding)}`,
          {
            schema: verdictSchema,
            model: expectedFamily === "gpt" ? GPT_VERIFY_MODEL : CLAUDE_MODEL,
            label: `${unit.label}-verify-${i + 1}-${expectedFamily}`,
            phase: "Verify",
          }
        )
      )
    ).then((checks) => ({ unit, review, checks }));
  }
);

for (let i = 0; i < units.length; i++) {
  const unit = units[i];
  const outcome = unitResults[i];
  const review = outcome && outcome.review;
  const warn = unit.kind === "brief" && unit.warning ? { warning: unit.warning } : {};
  reviewerStates.push({
    label: unit.label,
    family: unit.family,
    pinnedModel: modelForFamily(unit.family),
    selfReportedModel: review ? review.selfReportedModel ?? null : null,
    status: review ? "done" : "no-result",
  });
  if (!review) {
    if (unit.kind === "brief")
      briefReports.push({ brief: unit.brief, title: unit.title, status: "skipped", reason: "reviewer returned no result", ...warn });
    continue;
  }
  if (unit.kind === "brief") {
    if (review.applicable === false) {
      briefReports.push({ brief: unit.brief, title: unit.title, status: "skipped", reason: review.reason, ...warn });
      continue;
    }
    briefReports.push({ brief: unit.brief, title: unit.title, status: "run", reason: review.reason, ...warn });
  }
  const expectedFamily = unit.family === "gpt" ? "claude" : "gpt";
  review.findings.forEach((finding, j) => {
    const check = outcome.checks[j];
    findings.push({
      ...finding,
      source: unit.kind === "brief" ? unit.title : unit.label,
      ...verdictFor(unit.family, review.selfReportedModel, check, expectedFamily),
      review: {
        label: unit.label,
        configuredFamily: unit.family,
        pinnedModel: modelForFamily(unit.family),
        selfReportedModel: review.selfReportedModel ?? null,
      },
      verify: {
        label: `${unit.label}-verify-${j + 1}-${expectedFamily}`,
        expectedFamily,
        pinnedModel: expectedFamily === "gpt" ? GPT_VERIFY_MODEL : CLAUDE_MODEL,
        selfReportedModel: check ? check.selfReportedModel : null,
      },
    });
  });
}

return assembleResult({ target, head, occasion, reviewerStates, briefReports, findings });
