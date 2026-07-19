export const meta = {
  name: "minos-fix-wave",
  description: "Classify a completed review sweep and dispatch one bounded fix wave",
  phases: [{ title: "Fix", detail: "cluster confirmed findings, repair, and retry once" }],
};

const SEVERITY = { Low: 1, Medium: 2, High: 3, Critical: 4 };
const DEFAULT_THRESHOLD = "High";
const DEFAULT_CLUSTER_CAP = 5;

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

function normaliseTitle(value) {
  return String(value || "").trim().toLowerCase().replace(/\s+/g, " ");
}

function findingKey(finding) {
  return JSON.stringify([finding.path, finding.line, normaliseTitle(finding.title)]);
}

function preparedFinding(finding) {
  return { ...finding, key: findingKey(finding) };
}

function findingComment(finding) {
  return {
    path: finding.path,
    body: `**${finding.title}**\n\n${finding.explanation}\n\nSeverity: ${finding.severity}. Confidence: ${finding.confidence}.`,
    new_position: finding.line,
  };
}

function writeUpComment(finding, writeUp) {
  return { path: finding.path, body: writeUp, new_position: finding.line };
}

function clustersFor(findings, cap) {
  const byPath = new Map();
  for (const finding of findings) {
    if (!byPath.has(finding.path)) byPath.set(finding.path, []);
    byPath.get(finding.path).push(finding);
  }
  const components = [...byPath.entries()].map(([path, sameFile]) => ({ files: [path], findings: sameFile }));
  const packed = [];
  let current = null;
  for (const component of components) {
    if (!current || current.findings.length + component.findings.length > cap) {
      if (current) packed.push(current);
      current = { files: [...component.files], findings: [...component.findings] };
    } else {
      current.files.push(...component.files);
      current.findings.push(...component.findings);
    }
  }
  if (current) packed.push(current);
  return packed.map((cluster, index) => ({ id: `fix-cluster-${index + 1}`, ...cluster }));
}

function fixPrompt(input, cluster, attempt) {
  const brief = input.fixerBrief;
  return (
    `Read and follow the Markdown fix brief at ${brief.readPath}.\n\n` +
    `<fix-brief path="${brief.path}">\n${brief.content}\n</fix-brief>\n\n` +
    `Judge repairs against the reviewed project's guidance.\n\n` +
    `<project-guidance grounding="${input.guidance.grounding}" path="${input.guidance.path}">\n` +
    `${input.guidance.content}\n</project-guidance>\n\n` +
    `Workspace: ${input.workspace}\nAttempt: ${attempt} of ${input.singleWave ? 1 : 2}\n` +
    `Assigned files: ${cluster.files.join(", ")}\n` +
    `Confirmed findings: ${JSON.stringify(cluster.findings)}\n` +
    `Return one result for every findingKey. Commit completed repairs, return the commit SHA, and do not push.`
  );
}

function validInput(input) {
  return Boolean(
    input &&
    input.review &&
    input.review.status === "complete" &&
    Array.isArray(input.review.confirmedFindings) &&
    input.fixerBrief &&
    typeof input.fixerBrief.readPath === "string" &&
    typeof input.fixerBrief.content === "string" &&
    input.guidance &&
    typeof input.guidance.path === "string" &&
    typeof input.guidance.content === "string" &&
    input.guidance.content.trim() !== "" &&
    typeof input.workspace === "string" &&
    input.workspace !== ""
  );
}

function failedResult(input, reason) {
  return {
    status: "incomplete",
    reason,
    classification: null,
    round: Number(input && input.runRecord && input.runRecord.round) || 0,
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

const input = args && typeof args === "object" ? args : null;
if (!validInput(input))
  return failedResult(input, "fix workflow needs a complete review, fixer brief, project guidance, and workspace");

const threshold = input.threshold === undefined ? DEFAULT_THRESHOLD : input.threshold;
const clusterCap = input.clusterCap === undefined ? DEFAULT_CLUSTER_CAP : input.clusterCap;
const maximumRounds = input.maximumRounds === undefined || input.maximumRounds === null
  ? null
  : input.maximumRounds;
if (!Object.hasOwn(SEVERITY, threshold))
  return failedResult(input, `unknown review threshold ${String(threshold)}`);
if (!Number.isInteger(clusterCap) || clusterCap < 1)
  return failedResult(input, "fix cluster cap must be a positive integer");
if (maximumRounds !== null && (!Number.isInteger(maximumRounds) || maximumRounds < 1))
  return failedResult(input, "maximum rounds must be a positive integer when set");
const prior = input.runRecord && typeof input.runRecord === "object" ? input.runRecord : {};
const round = (Number.isInteger(prior.round) && prior.round >= 0 ? prior.round : 0) + 1;
const priorUnfixed = Array.isArray(prior.confirmedUnfixed) ? prior.confirmedUnfixed : [];
const unfixedByKey = new Map(priorUnfixed.map((entry) => [entry.key, entry]));
const findings = input.review.confirmedFindings.map(preparedFinding);

if (input.singleWave === true) {
  const clusters = clustersFor(findings, clusterCap);
  const dispatches = clusters.map((cluster) => ({
    label: `brief-${cluster.id}`,
    attempt: 1,
    files: cluster.files,
    findingKeys: cluster.findings.map((finding) => finding.key),
  }));
  phase("Fix");
  const results = await parallel(clusters.map((cluster, index) => () =>
    agent(fixPrompt(input, cluster, 1), {
      schema: fixResultSchema,
      model: "claude-opus-4-8",
      effort: "high",
      label: dispatches[index].label,
      phase: "Fix",
  })));
  const commits = [];
  const confirmedUnfixed = [];
  clusters.forEach((cluster, index) => {
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
    round: 1,
    dispatches,
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
    runRecord: { round: 1, confirmedUnfixed },
  };
}

const newAboveThreshold = findings.filter((finding) =>
  SEVERITY[finding.severity] >= SEVERITY[threshold] && !unfixedByKey.has(finding.key));
const maximumReached = maximumRounds !== null && round > maximumRounds;

if (newAboveThreshold.length === 0 || maximumReached) {
  const overflow = findings.filter((finding) =>
    SEVERITY[finding.severity] < SEVERITY[threshold] && !unfixedByKey.has(finding.key));
  const requestChanges = [...priorUnfixed];
  if (maximumReached) {
    for (const finding of findings.filter((candidate) => SEVERITY[candidate.severity] >= SEVERITY[threshold]))
      if (!unfixedByKey.has(finding.key)) requestChanges.push({ key: finding.key, finding, attempts: 0, reason: "maximum rounds reached" });
  }
  return {
    status: "complete",
    classification: "terminal",
    terminalReason: maximumReached ? "maximum-rounds" : "below-threshold",
    threshold,
    round,
    dispatches: [],
    sweepReview: null,
    fixReview: null,
    requestChangesReview: requestChanges.length > 0 ? {
      verdict: "request-changes",
      body: "Confirmed code findings remain unresolved.",
      comments: requestChanges.map((entry) => findingComment(entry.finding)),
    } : null,
    integration: { commits: [], pushCount: 0 },
    confirmedUnfixed: requestChanges,
    overflow,
    requestChanges,
    rerunReview: false,
    runRecord: { round, confirmedUnfixed: requestChanges },
  };
}

const candidates = findings.filter((finding) => !unfixedByKey.has(finding.key));
const clusters = clustersFor(candidates, clusterCap);
const dispatches = [];
phase("Fix");

for (const cluster of clusters)
  dispatches.push({ label: cluster.id, attempt: 1, files: cluster.files, findingKeys: cluster.findings.map((finding) => finding.key) });
const firstResults = await parallel(clusters.map((cluster) => () =>
  agent(fixPrompt(input, cluster, 1), {
    schema: fixResultSchema,
    model: "claude-opus-4-8",
    effort: "high",
    label: cluster.id,
    phase: "Fix",
  })));

const fixed = new Map();
const commits = [];
const retryClusters = [];
clusters.forEach((cluster, index) => {
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
  if (result && result.commit && cluster.findings.some((finding) => fixed.has(finding.key))) commits.push(result.commit);
  if (failed.length > 0) retryClusters.push({ ...cluster, id: `${cluster.id}-retry`, findings: failed });
});

for (const cluster of retryClusters)
  dispatches.push({ label: cluster.id, attempt: 2, files: cluster.files, findingKeys: cluster.findings.map((finding) => finding.key) });
const retryResults = await parallel(retryClusters.map((cluster) => () =>
  agent(fixPrompt(input, cluster, 2), {
    schema: fixResultSchema,
    model: "claude-opus-4-8",
    effort: "high",
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
      newlyUnfixed.push({ key: finding.key, finding, attempts: 2, reason: entry && entry.writeUp ? entry.writeUp : "fix failed twice" });
    }
  }
  if (usedCommit) commits.push(result.commit);
});

const confirmedUnfixed = [...priorUnfixed, ...newlyUnfixed];
const uniqueCommits = [...new Set(commits)];
const writeUps = [...fixed.values()];
return {
  status: "complete",
  classification: "working",
  threshold,
  round,
  dispatches,
  sweepReview: {
    verdict: "comment",
    body: "Confirmed findings in the reviewed code.",
    comments: findings.map(findingComment),
  },
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
  runRecord: { round, confirmedUnfixed },
};
