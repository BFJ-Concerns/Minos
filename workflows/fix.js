export const meta = {
  name: "minos-fix-wave-dispatch",
  description: "Dispatch one prepared fix wave and retry ordinary failures once",
  phases: [{ title: "Fix", detail: "repair prepared findings and retry ordinary failures once" }],
};

const FIX_MODEL = "gpt-5.6-sol";
const PLAN_KIND = "minos-fix-wave-plan-v1";

const fixResultSchema = {
  type: "object",
  additionalProperties: false,
  required: ["commit", "fixes"],
  properties: {
    commit: { type: "string" },
    fixes: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["findingKey", "status", "writeUp"],
        properties: {
          findingKey: { type: "string" },
          status: { type: "string", enum: ["fixed", "failed"] },
          writeUp: { type: "string" },
        },
      },
    },
  },
};

function writeUpComment(finding, writeUp) {
  return { path: finding.path, body: writeUp, line: finding.line };
}

function verificationInstruction(kind, command) {
  if (typeof command !== "string" || command.trim() === "") return "";
  return `Run this configured ${kind} command before returning: ${JSON.stringify(command)}\n`;
}

function warmTargetInstruction(source) {
  if (typeof source !== "string" || source.trim() === "") return "";
  return (
    `Before editing or verification, seed this isolated worktree's Rust target: create ./target and copy the contents of ` +
    `${JSON.stringify(source)} into it without modifying the source. Do not share one CARGO_TARGET_DIR across worktrees.\n`
  );
}

function fixPrompt(plan, dispatch, attempt) {
  const brief = plan.input.fixerBrief;
  const verification = plan.input.verification || { build: "", tests: "" };
  return (
    `Read and follow the Markdown fix brief at ${brief.readPath}.\n\n` +
    `<fix-brief path="${brief.path}">\n${brief.content}\n</fix-brief>\n\n` +
    `Judge repairs against the reviewed project's guidance.\n\n` +
    `<project-guidance grounding="${plan.input.guidance.grounding}" path="${plan.input.guidance.path}">\n` +
    `${plan.input.guidance.content}\n</project-guidance>\n\n` +
    `Workspace: ${plan.input.workspace}\nAttempt: ${attempt} of ${plan.classification === "single-wave" ? 1 : 2}\n` +
    `Wave fingerprint: ${plan.fingerprint}\n` +
    `Confirmed findings: ${JSON.stringify(dispatch.findings)}\n` +
    warmTargetInstruction(plan.input.warmTargetSource) +
    verificationInstruction("build", verification.build) +
    verificationInstruction("test", verification.tests) +
    `Return one result for every findingKey. Commit completed repairs, return the commit SHA, and do not push.`
  );
}

function failedResult(plan, reason) {
  return {
    status: "incomplete",
    reason,
    classification: null,
    round: Number(plan && plan.round) || 0,
    fingerprint: plan && plan.fingerprint || null,
    dispatches: [],
    sweepReview: null,
    fixReview: null,
    requestChangesReview: null,
    integration: { commits: [], pushCount: 0 },
    confirmedUnfixed: [],
    overflow: [],
    requestChanges: [],
    rerunReview: false,
  };
}

function validPlan(plan) {
  return Boolean(
    plan &&
    plan.kind === PLAN_KIND &&
    plan.status === "complete" &&
    (plan.classification === "working" || plan.classification === "single-wave") &&
    typeof plan.fingerprint === "string" &&
    plan.fingerprint !== "" &&
    plan.input &&
    typeof plan.input.workspace === "string" &&
    plan.input.guidance &&
    plan.input.fixerBrief &&
    Array.isArray(plan.findings) &&
    Array.isArray(plan.dispatches) &&
    Array.isArray(plan.priorConfirmedFixed) &&
    Array.isArray(plan.priorConfirmedUnfixed)
  );
}

const plan = args && typeof args === "object" ? args : null;
if (!validPlan(plan))
  return failedResult(
    plan,
    plan && plan.status === "incomplete" && typeof plan.reason === "string" && plan.reason !== ""
      ? plan.reason
      : "fix dispatch needs a complete working or single-wave preparation",
  );

phase("Fix");

const findingsByKey = new Map(plan.findings.map((finding) => [finding.key, finding]));
const preparedDispatches = plan.dispatches.map((dispatch) => ({
  ...dispatch,
  findings: Array.isArray(dispatch.findingKeys)
    ? dispatch.findingKeys.map((key) => findingsByKey.get(key))
    : [],
}));
if (preparedDispatches.some((dispatch) =>
  dispatch.findings.length !== dispatch.findingKeys?.length || dispatch.findings.some((finding) => !finding)))
  return failedResult(plan, "fix dispatch names a finding absent from the prepared plan");

if (plan.classification === "single-wave") {
  const results = await parallel(preparedDispatches.map((dispatch) => () =>
    agent(fixPrompt(plan, dispatch, 1), {
      engine: "codex",
      schema: fixResultSchema,
      model: FIX_MODEL,
      effort: "high",
      isolation: "worktree",
      label: dispatch.label,
      phase: "Fix",
    })));
  const commits = [];
  const fixed = [];
  const confirmedUnfixed = [];
  preparedDispatches.forEach((dispatch, index) => {
    const result = results[index];
    const returned = new Map(Array.isArray(result && result.fixes)
      ? result.fixes.map((entry) => [entry.findingKey, entry])
      : []);
    let usedCommit = false;
    for (const finding of dispatch.findings) {
      const entry = returned.get(finding.key);
      if (result && result.commit && entry && entry.status === "fixed" && entry.writeUp) {
        usedCommit = true;
        fixed.push({ finding, writeUp: entry.writeUp });
      } else {
        confirmedUnfixed.push({
          key: finding.key,
          finding,
          attempts: 1,
          reason: entry && entry.writeUp ? entry.writeUp : "brief fix failed",
        });
      }
    }
    if (usedCommit) commits.push(result.commit);
  });
  const uniqueCommits = [...new Set(commits)];
  return {
    status: "complete",
    classification: "single-wave",
    threshold: "Low",
    round: plan.round,
    fingerprint: plan.fingerprint,
    dispatches: plan.dispatches,
    sweepReview: null,
    fixReview: fixed.length > 0 ? {
      verdict: "comment",
      body: "Implemented repairs for confirmed findings.",
      comments: fixed.map(({ finding, writeUp }) => writeUpComment(finding, writeUp)),
    } : null,
    requestChangesReview: null,
    integration: {
      commits: uniqueCommits,
      pushCount: uniqueCommits.length > 0 ? 1 : 0,
      author: { name: "Minos", email: "minos@example.invalid" },
    },
    confirmedUnfixed,
    overflow: [],
    requestChanges: confirmedUnfixed,
    repairsComplete: confirmedUnfixed.length === 0,
    buildAndTestsRequired: uniqueCommits.length > 0,
    rerunReview: false,
    runRecord: { round: plan.round, confirmedFixed: [], confirmedUnfixed },
  };
}

const dispatches = [...plan.dispatches];
const firstResults = await parallel(preparedDispatches.map((dispatch) => () =>
  agent(fixPrompt(plan, dispatch, 1), {
    engine: "codex",
    schema: fixResultSchema,
    model: FIX_MODEL,
    effort: "high",
    isolation: "worktree",
    label: dispatch.label,
    phase: "Fix",
  })));

const fixed = new Map();
const commits = [];
const retryDispatches = [];
preparedDispatches.forEach((dispatch, index) => {
  const result = firstResults[index];
  const returned = new Map(Array.isArray(result && result.fixes)
    ? result.fixes.map((entry) => [entry.findingKey, entry])
    : []);
  const failed = [];
  for (const finding of dispatch.findings) {
    const entry = returned.get(finding.key);
    if (result && result.commit && entry && entry.status === "fixed" && entry.writeUp) {
      fixed.set(finding.key, { finding, writeUp: entry.writeUp, commit: result.commit });
    } else failed.push(finding);
  }
  if (result && result.commit && dispatch.findings.some((finding) => fixed.has(finding.key)))
    commits.push(result.commit);
  if (failed.length > 0)
    retryDispatches.push({
      id: `${dispatch.id}-retry`,
      label: `${dispatch.label}-retry`,
      attempt: 2,
      findingKeys: failed.map((finding) => finding.key),
      findings: failed,
    });
});

for (const dispatch of retryDispatches)
  dispatches.push({
    id: dispatch.id,
    label: dispatch.label,
    attempt: dispatch.attempt,
    findingKeys: dispatch.findingKeys,
  });
const retryResults = await parallel(retryDispatches.map((dispatch) => () =>
  agent(fixPrompt(plan, dispatch, 2), {
    engine: "codex",
    schema: fixResultSchema,
    model: FIX_MODEL,
    effort: "high",
    isolation: "worktree",
    label: dispatch.label,
    phase: "Fix",
  })));

const newlyUnfixed = [];
retryDispatches.forEach((dispatch, index) => {
  const result = retryResults[index];
  const returned = new Map(Array.isArray(result && result.fixes)
    ? result.fixes.map((entry) => [entry.findingKey, entry])
    : []);
  let usedCommit = false;
  for (const finding of dispatch.findings) {
    const entry = returned.get(finding.key);
    if (result && result.commit && entry && entry.status === "fixed" && entry.writeUp) {
      fixed.set(finding.key, { finding, writeUp: entry.writeUp, commit: result.commit });
      usedCommit = true;
    } else {
      newlyUnfixed.push({
        key: finding.key,
        finding,
        attempts: 2,
        reason: entry && entry.writeUp ? entry.writeUp : "fix failed twice",
      });
    }
  }
  if (usedCommit) commits.push(result.commit);
});

const confirmedUnfixed = [...plan.priorConfirmedUnfixed, ...newlyUnfixed];
const confirmedFixed = [
  ...plan.priorConfirmedFixed,
  ...[...fixed.entries()].map(([key, { finding, writeUp }]) => ({ key, finding, writeUp })),
];
const uniqueCommits = [...new Set(commits)];
const writeUps = [...fixed.values()];
return {
  status: "complete",
  classification: "working",
  threshold: plan.threshold,
  round: plan.round,
  fingerprint: plan.fingerprint,
  dispatches,
  sweepReview: plan.sweepReview,
  fixReview: writeUps.length > 0 ? {
    verdict: "comment",
    body: "Implemented repairs for confirmed findings.",
    comments: writeUps.map(({ finding, writeUp }) => writeUpComment(finding, writeUp)),
  } : null,
  requestChangesReview: null,
  integration: {
    commits: uniqueCommits,
    pushCount: uniqueCommits.length > 0 ? 1 : 0,
    author: { name: "Minos", email: "minos@example.invalid" },
  },
  confirmedUnfixed,
  overflow: [],
  requestChanges: [],
  rerunReview: true,
  runRecord: { round: plan.round, confirmedFixed, confirmedUnfixed },
};
