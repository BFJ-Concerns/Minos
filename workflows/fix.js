export const meta = {
  name: "minos-fix-wave-dispatch",
  description: "Dispatch one prepared fix wave and retry ordinary failures once",
  phases: [{ title: "Fix", detail: "repair prepared clusters and retry ordinary failures once" }],
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
  return { path: finding.path, body: writeUp, new_position: finding.line };
}

function fixPrompt(plan, cluster, attempt) {
  const brief = plan.input.fixerBrief;
  return (
    `Read and follow the Markdown fix brief at ${brief.readPath}.\n\n` +
    `<fix-brief path="${brief.path}">\n${brief.content}\n</fix-brief>\n\n` +
    `Judge repairs against the reviewed project's guidance.\n\n` +
    `<project-guidance grounding="${plan.input.guidance.grounding}" path="${plan.input.guidance.path}">\n` +
    `${plan.input.guidance.content}\n</project-guidance>\n\n` +
    `Workspace: ${plan.input.workspace}\nAttempt: ${attempt} of ${plan.classification === "single-wave" ? 1 : 2}\n` +
    `Wave fingerprint: ${plan.fingerprint}\n` +
    `Assigned files: ${cluster.files.join(", ")}\n` +
    `Confirmed findings: ${JSON.stringify(cluster.findings)}\n` +
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
    Array.isArray(plan.clusters) &&
    Array.isArray(plan.dispatches) &&
    Array.isArray(plan.priorConfirmedUnfixed)
  );
}

const plan = args && typeof args === "object" ? args : null;
if (!validPlan(plan))
  return failedResult(plan, "fix dispatch needs a complete working or single-wave preparation");

phase("Fix");

if (plan.classification === "single-wave") {
  const results = await parallel(plan.clusters.map((cluster, index) => () =>
    agent(fixPrompt(plan, cluster, 1), {
      engine: "codex",
      schema: fixResultSchema,
      model: FIX_MODEL,
      effort: "high",
      isolation: "worktree",
      label: plan.dispatches[index].label,
      phase: "Fix",
    })));
  const commits = [];
  const confirmedUnfixed = [];
  plan.clusters.forEach((cluster, index) => {
    const result = results[index];
    const returned = new Map(Array.isArray(result && result.fixes)
      ? result.fixes.map((entry) => [entry.findingKey, entry])
      : []);
    let usedCommit = false;
    for (const finding of cluster.findings) {
      const entry = returned.get(finding.key);
      if (result && result.commit && entry && entry.status === "fixed" && entry.writeUp) {
        usedCommit = true;
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
    fixReview: null,
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
    runRecord: { round: plan.round, confirmedUnfixed },
  };
}

const dispatches = [...plan.dispatches];
const firstResults = await parallel(plan.clusters.map((cluster) => () =>
  agent(fixPrompt(plan, cluster, 1), {
    engine: "codex",
    schema: fixResultSchema,
    model: FIX_MODEL,
    effort: "high",
    isolation: "worktree",
    label: cluster.id,
    phase: "Fix",
  })));

const fixed = new Map();
const commits = [];
const retryClusters = [];
plan.clusters.forEach((cluster, index) => {
  const result = firstResults[index];
  const returned = new Map(Array.isArray(result && result.fixes)
    ? result.fixes.map((entry) => [entry.findingKey, entry])
    : []);
  const failed = [];
  for (const finding of cluster.findings) {
    const entry = returned.get(finding.key);
    if (result && result.commit && entry && entry.status === "fixed" && entry.writeUp) {
      fixed.set(finding.key, { finding, writeUp: entry.writeUp, commit: result.commit });
    } else failed.push(finding);
  }
  if (result && result.commit && cluster.findings.some((finding) => fixed.has(finding.key)))
    commits.push(result.commit);
  if (failed.length > 0)
    retryClusters.push({ ...cluster, id: `${cluster.id}-retry`, findings: failed });
});

for (const cluster of retryClusters)
  dispatches.push({
    label: cluster.id,
    attempt: 2,
    files: cluster.files,
    findingKeys: cluster.findings.map((finding) => finding.key),
  });
const retryResults = await parallel(retryClusters.map((cluster) => () =>
  agent(fixPrompt(plan, cluster, 2), {
    engine: "codex",
    schema: fixResultSchema,
    model: FIX_MODEL,
    effort: "high",
    isolation: "worktree",
    label: cluster.id,
    phase: "Fix",
  })));

const newlyUnfixed = [];
retryClusters.forEach((cluster, index) => {
  const result = retryResults[index];
  const returned = new Map(Array.isArray(result && result.fixes)
    ? result.fixes.map((entry) => [entry.findingKey, entry])
    : []);
  let usedCommit = false;
  for (const finding of cluster.findings) {
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
  runRecord: { round: plan.round, confirmedUnfixed },
};
