import { createHash } from "node:crypto";

const SEVERITY = { Low: 1, Medium: 2, High: 3, Critical: 4 };
const DEFAULT_THRESHOLD = "High";
const DEFAULT_CLUSTER_CAP = 5;
const PLAN_KIND = "minos-fix-wave-plan-v1";

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

function invalidInputReason(input) {
  if (!input || typeof input !== "object") return "fix preparation needs an input object";
  if (!input.review || typeof input.review !== "object") return "fix preparation needs a review result";
  if (input.review.status !== "complete") {
    const detail = typeof input.review.reason === "string" && input.review.reason.trim() !== ""
      ? `: ${input.review.reason}`
      : "";
    return `fix preparation needs a complete review${detail}`;
  }
  if (!input.review.reviewed || typeof input.review.reviewed !== "object")
    return "fix preparation review is missing reviewed head and target";
  if (typeof input.review.reviewed.head !== "string" || input.review.reviewed.head.trim() === "")
    return "fix preparation review needs reviewed.head as a non-empty string";
  if (typeof input.review.reviewed.target !== "string" || input.review.reviewed.target.trim() === "")
    return "fix preparation review needs reviewed.target as a non-empty string";
  if (!Array.isArray(input.review.confirmedFindings))
    return "fix preparation review needs confirmedFindings as an array";
  if (!input.fixerBrief || typeof input.fixerBrief.readPath !== "string" ||
      typeof input.fixerBrief.content !== "string")
    return "fix preparation needs a fixer brief";
  if (!input.guidance || typeof input.guidance.path !== "string" ||
      typeof input.guidance.content !== "string" || input.guidance.content.trim() === "")
    return "fix preparation needs non-empty project guidance";
  if (typeof input.workspace !== "string" || input.workspace.trim() === "")
    return "fix preparation needs a workspace";
  return null;
}

function failedPlan(input, reason) {
  return {
    kind: PLAN_KIND,
    status: "incomplete",
    reason,
    classification: null,
    round: Number(input && input.runRecord && input.runRecord.round) || 0,
    fingerprint: null,
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

function fingerprintFor(input, round, findings) {
  const value = {
    reviewed: {
      target: input.review.reviewed.target,
      head: input.review.reviewed.head,
    },
    round,
    threshold: input.threshold === undefined ? DEFAULT_THRESHOLD : input.threshold,
    clusterCap: input.clusterCap === undefined ? DEFAULT_CLUSTER_CAP : input.clusterCap,
    maximumRounds: input.maximumRounds ?? null,
    singleWave: input.singleWave === true,
    priorConfirmedUnfixed: Array.isArray(input.runRecord && input.runRecord.confirmedUnfixed)
      ? input.runRecord.confirmedUnfixed.map((entry) => ({ key: entry.key, attempts: entry.attempts }))
      : [],
    findings: findings.map((finding) => ({
      key: finding.key,
      severity: finding.severity,
      confidence: finding.confidence,
      path: finding.path,
      line: finding.line,
      explanation: finding.explanation,
    })),
  };
  return createHash("sha256").update(JSON.stringify(value)).digest("hex");
}

function deepFreeze(value) {
  if (!value || typeof value !== "object" || Object.isFrozen(value)) return value;
  Object.freeze(value);
  for (const child of Object.values(value)) deepFreeze(child);
  return value;
}

export function isFixWavePlan(value) {
  return Boolean(value && value.kind === PLAN_KIND);
}

export function prepareFixWave(input) {
  const inputFailure = invalidInputReason(input);
  if (inputFailure)
    return deepFreeze(failedPlan(input, inputFailure));

  const threshold = input.threshold === undefined ? DEFAULT_THRESHOLD : input.threshold;
  const clusterCap = input.clusterCap === undefined ? DEFAULT_CLUSTER_CAP : input.clusterCap;
  const maximumRounds = input.maximumRounds === undefined || input.maximumRounds === null
    ? null
    : input.maximumRounds;
  if (!Object.hasOwn(SEVERITY, threshold))
    return deepFreeze(failedPlan(input, `unknown review threshold ${String(threshold)}`));
  if (!Number.isInteger(clusterCap) || clusterCap < 1)
    return deepFreeze(failedPlan(input, "fix cluster cap must be a positive integer"));
  if (maximumRounds !== null && (!Number.isInteger(maximumRounds) || maximumRounds < 1))
    return deepFreeze(failedPlan(input, "maximum rounds must be a positive integer when set"));

  const prior = input.runRecord && typeof input.runRecord === "object" ? input.runRecord : {};
  const verification = {
    build: typeof input.verification?.build === "string" ? input.verification.build : "",
    tests: typeof input.verification?.tests === "string" ? input.verification.tests : "",
  };
  const priorUnfixed = Array.isArray(prior.confirmedUnfixed)
    ? prior.confirmedUnfixed.map((entry) => ({
      ...entry,
      finding: entry && entry.finding ? { ...entry.finding } : entry.finding,
    }))
    : [];
  const findings = input.review.confirmedFindings.map(preparedFinding);

  if (input.singleWave === true) {
    const round = 1;
    const clusters = clustersFor(findings, clusterCap);
    const fingerprint = fingerprintFor(input, round, findings);
    return deepFreeze({
      kind: PLAN_KIND,
      status: "complete",
      classification: "single-wave",
      threshold: "Low",
      round,
      fingerprint,
      input: {
        workspace: input.workspace,
        verification,
        guidance: { ...input.guidance },
        fixerBrief: { ...input.fixerBrief },
      },
      findings,
      clusters,
      priorConfirmedUnfixed: [],
      dispatches: clusters.map((cluster) => ({
        label: `brief-${cluster.id}`,
        attempt: 1,
        files: cluster.files,
        findingKeys: cluster.findings.map((finding) => finding.key),
      })),
      sweepReview: null,
      fixReview: null,
      requestChangesReview: null,
      integration: { commits: [], pushCount: 0 },
      confirmedUnfixed: [],
      overflow: [],
      requestChanges: [],
      rerunReview: false,
      runRecord: { round, confirmedUnfixed: [] },
    });
  }

  const round = (Number.isInteger(prior.round) && prior.round >= 0 ? prior.round : 0) + 1;
  const unfixedByKey = new Map(priorUnfixed.map((entry) => [entry.key, entry]));
  const newAboveThreshold = findings.filter((finding) =>
    SEVERITY[finding.severity] >= SEVERITY[threshold] && !unfixedByKey.has(finding.key));
  const maximumReached = maximumRounds !== null && round > maximumRounds;
  const fingerprint = fingerprintFor(input, round, findings);

  if (newAboveThreshold.length === 0 || maximumReached) {
    const overflow = findings.filter((finding) =>
      SEVERITY[finding.severity] < SEVERITY[threshold] && !unfixedByKey.has(finding.key));
    const requestChanges = [...priorUnfixed];
    if (maximumReached) {
      for (const finding of findings.filter((candidate) => SEVERITY[candidate.severity] >= SEVERITY[threshold]))
        if (!unfixedByKey.has(finding.key))
          requestChanges.push({ key: finding.key, finding, attempts: 0, reason: "maximum rounds reached" });
    }
    return deepFreeze({
      kind: PLAN_KIND,
      status: "complete",
      classification: "terminal",
      terminalReason: maximumReached ? "maximum-rounds" : "below-threshold",
      threshold,
      round,
      fingerprint,
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
    });
  }

  const candidates = findings.filter((finding) => !unfixedByKey.has(finding.key));
  const clusters = clustersFor(candidates, clusterCap);
  return deepFreeze({
    kind: PLAN_KIND,
    status: "complete",
    classification: "working",
    threshold,
    round,
    fingerprint,
    input: {
      workspace: input.workspace,
      verification,
      guidance: { ...input.guidance },
      fixerBrief: { ...input.fixerBrief },
    },
    findings,
    clusters,
    priorConfirmedUnfixed: priorUnfixed,
    dispatches: clusters.map((cluster) => ({
      label: cluster.id,
      attempt: 1,
      files: cluster.files,
      findingKeys: cluster.findings.map((finding) => finding.key),
    })),
    sweepReview: {
      verdict: "comment",
      body: "Confirmed findings in the reviewed code.",
      comments: findings.map(findingComment),
    },
    fixReview: null,
    requestChangesReview: null,
    integration: { commits: [], pushCount: 0 },
    confirmedUnfixed: priorUnfixed,
    overflow: [],
    requestChanges: [],
    rerunReview: true,
    runRecord: { round, confirmedUnfixed: priorUnfixed },
  });
}
