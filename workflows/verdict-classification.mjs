// The verdict classification's one deterministic home. The lead's judgement
// decides what verdict a completed review earned; this module owns
// everything about that decision that is genuinely black and white: the
// mechanical digest of the adjudicated verdict the judgement is informed by,
// and the fail-closed validation of the recorded decision. Publication
// structure lives with the guarded forge commands, which consume a validated
// decision and never re-derive a classification.

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

export const DIGEST_KIND = "minos-verdict-digest-v1";
export const DECISION_KIND = "minos-verdict-decision-v1";
export const SEVERITY = { Low: 1, Medium: 2, High: 3, Critical: 4 };
export const DEFAULT_THRESHOLD = "High";

// The two classes the commission lets judge an at-or-above-threshold
// confirmed finding out of gating. There is no third: any other basis for
// declassification is the mechanical binding the lead's judgement exists to
// prevent.
export const NEVER_GATING_CLASSES = ["declared-out-of-scope", "speculative-hardening"];

export function atOrAboveThreshold(finding, threshold) {
  return SEVERITY[finding.severity] >= SEVERITY[threshold];
}

export function normalisedTitle(finding) {
  return String(finding.title || "").trim().toLowerCase().replace(/\s+/g, " ");
}

// The adjudicated verdict's own finding ids are the identity vocabulary:
// unique by construction, so two specialists confirming the same defect at
// the same site stay two disposable findings. The content key is only the
// fallback for a verdict without ids.
export function findingKey(finding) {
  if (typeof finding.id === "string" && finding.id !== "") return finding.id;
  return JSON.stringify([finding.path, finding.line, normalisedTitle(finding)]);
}

function invalidDigestInputReason(input) {
  if (!input || typeof input !== "object") return "verdict digest needs an input object";
  if (!input.review || typeof input.review !== "object" || !Array.isArray(input.review.confirmedFindings))
    return "verdict digest needs a review with confirmedFindings";
  if (input.review.status !== "complete")
    return "verdict digest needs a complete review";
  const threshold = input.threshold === undefined ? DEFAULT_THRESHOLD : input.threshold;
  if (!Object.hasOwn(SEVERITY, threshold))
    return `unknown review threshold ${String(threshold)}`;
  return null;
}

// The mechanical facts of one completed review, computed once and consumed
// twice: the lead reads the digest to judge the verdict, and the validation
// below recomputes it to check the recorded decision against the same facts.
// `thresholdIndication` is what the configured threshold alone would say —
// advisory by design: the commission makes classification the lead's
// judgement, informed by the threshold rather than mechanically bound to it.
export function verdictDigest(input) {
  const inputFailure = invalidDigestInputReason(input);
  if (inputFailure) return { kind: DIGEST_KIND, status: "invalid", reason: inputFailure };

  const threshold = input.threshold === undefined ? DEFAULT_THRESHOLD : input.threshold;
  const findings = input.review.confirmedFindings.map((finding) => ({
    key: findingKey(finding),
    severity: finding.severity,
    confidence: finding.confidence,
    verifierConfidence: finding.verifierConfidence,
    path: finding.path,
    line: finding.line,
    title: finding.title,
    atOrAboveThreshold: atOrAboveThreshold(finding, threshold),
  }));
  const duplicateKeys = new Set();
  const seen = new Set();
  for (const finding of findings) {
    if (seen.has(finding.key)) duplicateKeys.add(finding.key);
    seen.add(finding.key);
  }
  if (duplicateKeys.size > 0)
    return {
      kind: DIGEST_KIND,
      status: "invalid",
      reason: `confirmed findings carry duplicate identities: ${[...duplicateKeys].join(", ")}`,
    };

  return {
    kind: DIGEST_KIND,
    status: "complete",
    threshold,
    findings,
    atOrAboveThresholdKeys: findings.filter((f) => f.atOrAboveThreshold).map((f) => f.key),
    belowThresholdKeys: findings.filter((f) => !f.atOrAboveThreshold).map((f) => f.key),
    thresholdIndication: findings.some((f) => f.atOrAboveThreshold) ? "request-changes" : "clean",
  };
}

// Fail-closed validation of the lead's recorded classification. The
// judgement is the lead's; what is validated here is only coherence that is
// black and white: the decision names this contract, states its basis,
// disposes of every confirmed finding exactly once, declassifies an
// at-or-above-threshold finding only under one of the two never-gating
// classes (named per finding), gates a below-threshold finding only with an
// undergrade basis naming it, labels restatements of one defect with one
// non-empty `defect` label whose members gate alike, and earns
// request-changes exactly when a gating finding exists.
export function validateVerdictDecision(decision, digest) {
  if (!digest || digest.kind !== DIGEST_KIND || digest.status !== "complete")
    return { ok: false, reason: "verdict decision validation needs a complete verdict digest" };
  if (!decision || typeof decision !== "object" || Array.isArray(decision))
    return { ok: false, reason: "verdict decision must be an object" };
  if (decision.kind !== DECISION_KIND)
    return { ok: false, reason: `verdict decision must have kind ${DECISION_KIND}` };
  if (decision.verdict !== "clean" && decision.verdict !== "request-changes")
    return { ok: false, reason: "verdict decision verdict must be clean or request-changes" };
  if (typeof decision.basis !== "string" || decision.basis.trim() === "")
    return { ok: false, reason: "verdict decision must state a non-empty basis" };
  if (!Array.isArray(decision.findings))
    return { ok: false, reason: "verdict decision must dispose of the confirmed findings as an array" };

  const digestByKey = new Map(digest.findings.map((finding) => [finding.key, finding]));
  const disposed = new Set();
  for (const disposition of decision.findings) {
    if (!disposition || typeof disposition !== "object" || typeof disposition.key !== "string")
      return { ok: false, reason: "each finding disposition needs the digest's finding key" };
    const finding = digestByKey.get(disposition.key);
    if (!finding)
      return { ok: false, reason: `finding disposition names an unknown finding: ${disposition.key}` };
    if (disposed.has(disposition.key))
      return { ok: false, reason: `finding disposed of twice: ${disposition.key}` };
    disposed.add(disposition.key);
    if (disposition.gating !== true && disposition.gating !== false)
      return { ok: false, reason: `finding disposition must set gating true or false: ${disposition.key}` };
    if (disposition.class !== undefined && !NEVER_GATING_CLASSES.includes(disposition.class))
      return { ok: false, reason: `finding disposition carries an unknown class: ${disposition.key}` };
    if (disposition.undergrade !== undefined && (typeof disposition.undergrade !== "string" || disposition.undergrade.trim() === ""))
      return { ok: false, reason: `finding disposition undergrade must be a non-empty string: ${disposition.key}` };
    if (disposition.defect !== undefined && (typeof disposition.defect !== "string" || disposition.defect.trim() === ""))
      return { ok: false, reason: `finding disposition defect must be a non-empty label: ${disposition.key}` };
    if (finding.atOrAboveThreshold && disposition.gating === false) {
      if (!NEVER_GATING_CLASSES.includes(disposition.class))
        return {
          ok: false,
          reason: `an at-or-above-threshold finding may be judged non-gating only as ${NEVER_GATING_CLASSES.join(" or ")}: ${disposition.key}`,
        };
    }
    if (!finding.atOrAboveThreshold && disposition.gating === true) {
      if (typeof disposition.undergrade !== "string" || disposition.undergrade.trim() === "")
        return {
          ok: false,
          reason: `a below-threshold finding gates only with an undergrade basis naming it: ${disposition.key}`,
        };
    }
  }
  for (const key of digestByKey.keys()) {
    if (!disposed.has(key))
      return { ok: false, reason: `confirmed finding has no disposition: ${key}` };
  }
  // Restatements of one defect are one gating call: a label whose members
  // disagree is a decision that has not decided.
  const gatingByDefect = new Map();
  for (const disposition of decision.findings) {
    if (typeof disposition.defect !== "string") continue;
    const seen = gatingByDefect.get(disposition.defect);
    if (seen !== undefined && seen !== disposition.gating)
      return { ok: false, reason: `findings labelled one defect gate differently: ${disposition.defect}` };
    gatingByDefect.set(disposition.defect, disposition.gating);
  }
  const anyGating = decision.findings.some((disposition) => disposition.gating === true);
  if (anyGating && decision.verdict !== "request-changes")
    return { ok: false, reason: "a gating finding makes the verdict request-changes" };
  if (!anyGating && decision.verdict !== "clean")
    return { ok: false, reason: "with no gating finding the verdict is clean" };
  return { ok: true };
}

function readJson(path, label) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch {
    process.stderr.write(`${label} is missing or not valid JSON: ${path}\n`);
    process.exitCode = 2;
    return undefined;
  }
}

// CLI, in the deterministic input-builder idiom: one JSON line on stdout,
// usage-exit 2 on argument errors. --digest hands the lead the mechanical
// facts; --validate checks a recorded decision against a fresh digest of the
// same verdict, so publication can require a validated decision as data.
function runVerdictClassificationCli(argv) {
  const usage =
    "usage: node workflows/verdict-classification.mjs --digest VERDICT_FILE [THRESHOLD]\n" +
    "       node workflows/verdict-classification.mjs --validate VERDICT_FILE DECISION_FILE [THRESHOLD]\n";
  if (argv[0] === "--digest" && (argv.length === 2 || argv.length === 3)) {
    const review = readJson(argv[1], "verdict file");
    if (review === undefined) return;
    const input = argv.length === 3 ? { review, threshold: argv[2] } : { review };
    process.stdout.write(`${JSON.stringify(verdictDigest(input))}\n`);
    return;
  }
  if (argv[0] === "--validate" && (argv.length === 3 || argv.length === 4)) {
    const review = readJson(argv[1], "verdict file");
    if (review === undefined) return;
    const decision = readJson(argv[2], "decision file");
    if (decision === undefined) return;
    const input = argv.length === 4 ? { review, threshold: argv[3] } : { review };
    const digest = verdictDigest(input);
    if (digest.status !== "complete") {
      process.stdout.write(`${JSON.stringify({ ok: false, reason: digest.reason })}\n`);
      return;
    }
    process.stdout.write(`${JSON.stringify(validateVerdictDecision(decision, digest))}\n`);
    return;
  }
  process.stderr.write(usage);
  process.exitCode = 2;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1])
  runVerdictClassificationCli(process.argv.slice(2));
