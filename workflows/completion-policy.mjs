// The review completion policy's one deterministic home. The lead's judgement
// decides whether a sweep is working or terminal; this module owns everything
// about that decision that is genuinely black and white: the canonical finding
// identity, the mechanical digest of a sweep the judgement is informed by, and
// the fail-closed validation of the recorded decision. Plan construction and
// publication structure live in fix-wave-plan.mjs, which consumes a validated
// decision and never re-derives a classification.

import { fileURLToPath } from "node:url";

export const DIGEST_KIND = "minos-sweep-digest-v1";
export const DECISION_KIND = "minos-sweep-decision-v1";
export const SEVERITY = { Low: 1, Medium: 2, High: 3, Critical: 4 };
export const DEFAULT_THRESHOLD = "High";

// A configured command's exit status is a mechanical gate, not a lead
// judgement. Empty commands are valid skips; a non-zero result is terminal
// and cannot feed another review round.
export function configuredCommandResult(command, exitStatus) {
  if (command === undefined || command === null ||
      (typeof command === "string" && command.trim() === ""))
    return { status: "skipped", terminal: false, reviewAllowed: true };
  if (exitStatus === 0) return { status: "passed", terminal: false, reviewAllowed: true };
  return {
    status: "failed",
    exitStatus,
    terminal: true,
    reviewAllowed: false,
    forgeStatus: "incomplete",
  };
}

function runConfiguredCommandCli(argv) {
  const usage = "usage: node workflows/completion-policy.mjs COMMAND EXIT_STATUS\n";
  const [command, exitStatusText, ...surplus] = argv;
  if (command === undefined || exitStatusText === undefined || surplus.length > 0 ||
      !/^\d+$/.test(exitStatusText)) {
    process.stderr.write(usage);
    process.exitCode = 2;
    return;
  }
  process.stdout.write(`${JSON.stringify(configuredCommandResult(command, Number(exitStatusText)))}\n`);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1])
  runConfiguredCommandCli(process.argv.slice(2));

function normaliseTitle(value) {
  return String(value || "").trim().toLowerCase().replace(/\s+/g, " ");
}

// One finding, one key, everywhere: the loop record's confirmed-unfixed
// entries, dispatch assignment, and the digest all recognise a finding by
// this identity, so a re-reported finding cannot re-enter the loop under a
// cosmetic retitle. Known limit: the key includes the line, so a repair
// elsewhere in the file can shift a confirmed-unfixed finding's line and
// give its rediscovery a fresh key — do not treat this identity as
// drift-proof when suppression matters.
export function findingKey(finding) {
  return JSON.stringify([finding.path, finding.line, normaliseTitle(finding.title)]);
}

export function preparedFinding(finding) {
  return { ...finding, key: findingKey(finding) };
}

export function atOrAboveThreshold(finding, threshold) {
  return SEVERITY[finding.severity] >= SEVERITY[threshold];
}

function invalidDigestInputReason(input) {
  if (!input || typeof input !== "object") return "sweep digest needs an input object";
  if (!input.review || typeof input.review !== "object" || !Array.isArray(input.review.confirmedFindings))
    return "sweep digest needs a review with confirmedFindings";
  if (input.review.status !== "complete")
    return "sweep digest needs a complete review";
  const threshold = input.threshold === undefined ? DEFAULT_THRESHOLD : input.threshold;
  if (!Object.hasOwn(SEVERITY, threshold))
    return `unknown review threshold ${String(threshold)}`;
  const maximumRounds = input.maximumRounds === undefined || input.maximumRounds === null
    ? null
    : input.maximumRounds;
  if (maximumRounds !== null && (!Number.isInteger(maximumRounds) || maximumRounds < 1))
    return "maximum rounds must be a positive integer when set";
  return null;
}

// The mechanical facts of one completed sweep, computed once and consumed
// twice: the lead reads the digest to judge the sweep, and prepareFixWave
// recomputes it to validate the recorded decision against the same facts.
// `thresholdIndication` is what the configured threshold alone would say —
// advisory by design: the commission makes classification the lead's
// judgement, informed by the threshold rather than mechanically bound to it.
export function sweepDigest(input) {
  const inputFailure = invalidDigestInputReason(input);
  if (inputFailure) return { kind: DIGEST_KIND, status: "invalid", reason: inputFailure };

  const threshold = input.threshold === undefined ? DEFAULT_THRESHOLD : input.threshold;
  const maximumRounds = input.maximumRounds === undefined || input.maximumRounds === null
    ? null
    : input.maximumRounds;
  const prior = input.runRecord && typeof input.runRecord === "object" ? input.runRecord : {};
  const priorConfirmedUnfixed = Array.isArray(prior.confirmedUnfixed) ? prior.confirmedUnfixed : [];
  const round = (Number.isInteger(prior.round) && prior.round >= 0 ? prior.round : 0) + 1;
  const unfixedKeys = new Set(priorConfirmedUnfixed.map((entry) => entry.key));
  const findings = input.review.confirmedFindings.map(preparedFinding);
  const candidates = findings.filter((finding) => !unfixedKeys.has(finding.key));
  const aboveThreshold = candidates.filter((finding) => atOrAboveThreshold(finding, threshold));
  const belowThreshold = candidates.filter((finding) => !atOrAboveThreshold(finding, threshold));
  const maximumRoundsReached = maximumRounds !== null && round > maximumRounds;

  return {
    kind: DIGEST_KIND,
    status: "complete",
    round,
    threshold,
    maximumRounds,
    maximumRoundsReached,
    priorConfirmedUnfixed: priorConfirmedUnfixed.map((entry) => ({
      key: entry.key,
      attempts: entry.attempts,
    })),
    candidates: candidates.map((finding) => ({
      key: finding.key,
      severity: finding.severity,
      confidence: finding.confidence,
      path: finding.path,
      line: finding.line,
      title: finding.title,
    })),
    aboveThresholdKeys: aboveThreshold.map((finding) => finding.key),
    belowThresholdKeys: belowThreshold.map((finding) => finding.key),
    thresholdIndication: maximumRoundsReached || aboveThreshold.length === 0 ? "terminal" : "working",
  };
}

// Fail-closed validation of the lead's recorded decision. The judgement is
// the lead's; what is validated here is only coherence that is black and
// white: the decision names this contract, classifies as working or
// terminal, states its basis, does not start a wave with nothing to
// dispatch, and does not overrule the configured maximum-rounds ceiling —
// the loop's one non-judgement stop. A terminal decision is always
// coherent: judgement may stop the loop with new findings still on the
// table, and those findings become the request-changes material.
export function validateSweepDecision(decision, digest) {
  if (!digest || digest.kind !== DIGEST_KIND || digest.status !== "complete")
    return { ok: false, reason: "sweep decision validation needs a complete sweep digest" };
  if (!decision || typeof decision !== "object" || Array.isArray(decision))
    return { ok: false, reason: "sweep decision must be an object" };
  if (decision.kind !== DECISION_KIND)
    return { ok: false, reason: `sweep decision must have kind ${DECISION_KIND}` };
  if (decision.classification !== "working" && decision.classification !== "terminal")
    return { ok: false, reason: "sweep decision classification must be working or terminal" };
  if (typeof decision.basis !== "string" || decision.basis.trim() === "")
    return { ok: false, reason: "sweep decision must state a non-empty basis" };
  if (decision.classification === "working" && digest.candidates.length === 0)
    return { ok: false, reason: "a working decision needs at least one dispatchable finding" };
  if (decision.classification === "working" && digest.maximumRoundsReached)
    return { ok: false, reason: "the configured maximum rounds has been reached; the sweep is terminal" };
  return { ok: true };
}
