import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import {
  DECISION_KIND,
  DIGEST_KIND,
  validateVerdictDecision,
  verdictDigest,
} from "./verdict-classification.mjs";

const cliPath = fileURLToPath(new URL("./verdict-classification.mjs", import.meta.url));

function finding(overrides = {}) {
  return {
    id: "specialist-1:1",
    title: "Off-by-one in retry bound",
    severity: "High",
    confidence: 80,
    verifierConfidence: 90,
    path: "internal/retry.go",
    line: 42,
    explanation: "The loop retries one time fewer than the configured bound.",
    ...overrides,
  };
}

function verdict(findings) {
  return { status: "complete", confirmedFindings: findings };
}

function completeDigest(findings, threshold = "High") {
  const digest = verdictDigest({ review: verdict(findings), threshold });
  assert.equal(digest.status, "complete");
  return digest;
}

function decision(digest, overrides = {}) {
  return {
    kind: DECISION_KIND,
    verdict: digest.findings.some((entry) => entry.atOrAboveThreshold) ? "request-changes" : "clean",
    basis: "the gating finding is a confirmed defect in the change's own logic",
    findings: digest.findings.map((entry) => ({ key: entry.key, gating: entry.atOrAboveThreshold })),
    ...overrides,
  };
}

test("digest reports threshold position per finding and the advisory indication", () => {
  const digest = completeDigest([
    finding(),
    finding({ id: "specialist-2:1", title: "Sloppy log wording", severity: "Low", line: 7 }),
  ]);
  assert.equal(digest.kind, DIGEST_KIND);
  assert.equal(digest.threshold, "High");
  assert.equal(digest.findings.length, 2);
  assert.equal(digest.atOrAboveThresholdKeys.length, 1);
  assert.equal(digest.belowThresholdKeys.length, 1);
  assert.equal(digest.thresholdIndication, "request-changes");
  const below = digest.findings.find((entry) => !entry.atOrAboveThreshold);
  assert.equal(below.severity, "Low");
});

test("digest with no at-or-above-threshold finding indicates clean", () => {
  const digest = completeDigest([finding({ severity: "Low" })]);
  assert.equal(digest.thresholdIndication, "clean");
});

test("an omitted threshold defaults High findings to the gating side", () => {
  const digest = verdictDigest({ review: verdict([finding({ severity: "High" })]) });
  assert.equal(digest.status, "complete");
  assert.equal(digest.threshold, "High");
  assert.deepEqual(digest.atOrAboveThresholdKeys, ["specialist-1:1"]);
  assert.equal(digest.thresholdIndication, "request-changes");
});

test("digest fails closed on an incomplete review, an unknown threshold, and duplicate identities", () => {
  assert.equal(verdictDigest({ review: { status: "incomplete", confirmedFindings: [] } }).status, "invalid");
  assert.equal(verdictDigest({ review: verdict([]), threshold: "P1" }).status, "invalid");
  const duplicated = verdictDigest({ review: verdict([finding(), finding()]) });
  assert.equal(duplicated.status, "invalid");
  assert.match(duplicated.reason, /duplicate identities/);
});

test("digest keys by the verdict's finding id, so two findings at one site stay distinct", () => {
  const digest = completeDigest([
    finding({ id: "specialist-1:1" }),
    finding({ id: "specialist-2:1" }),
  ]);
  assert.equal(digest.status, "complete");
  assert.deepEqual(digest.findings.map((entry) => entry.key), ["specialist-1:1", "specialist-2:1"]);
});

test("an unknown class or malformed undergrade is refused wherever it appears", () => {
  const digest = completeDigest([finding()]);
  const unknownClass = decision(digest, {
    findings: [{ key: digest.findings[0].key, gating: true, class: "just-because" }],
  });
  const refused = validateVerdictDecision(unknownClass, digest);
  assert.equal(refused.ok, false);
  assert.match(refused.reason, /unknown class/);

  const badUndergrade = decision(digest, {
    findings: [{ key: digest.findings[0].key, gating: true, undergrade: "  " }],
  });
  const refusedUndergrade = validateVerdictDecision(badUndergrade, digest);
  assert.equal(refusedUndergrade.ok, false);
  assert.match(refusedUndergrade.reason, /undergrade must be a non-empty string/);
});

test("a coherent decision validates", () => {
  const digest = completeDigest([finding()]);
  assert.deepEqual(validateVerdictDecision(decision(digest), digest), { ok: true });
});

test("validation refuses a missing basis", () => {
  const digest = completeDigest([finding()]);
  const result = validateVerdictDecision(decision(digest, { basis: "  " }), digest);
  assert.equal(result.ok, false);
  assert.match(result.reason, /non-empty basis/);
});

test("declassifying an at-or-above-threshold finding requires one of the two never-gating classes", () => {
  const digest = completeDigest([finding()]);
  const unnamed = decision(digest, {
    verdict: "clean",
    findings: [{ key: digest.findings[0].key, gating: false }],
  });
  const refused = validateVerdictDecision(unnamed, digest);
  assert.equal(refused.ok, false);
  assert.match(refused.reason, /declared-out-of-scope or speculative-hardening/);

  const named = decision(digest, {
    verdict: "clean",
    findings: [{ key: digest.findings[0].key, gating: false, class: "speculative-hardening" }],
  });
  assert.deepEqual(validateVerdictDecision(named, digest), { ok: true });
});

test("gating a below-threshold finding requires an undergrade basis naming it", () => {
  const digest = completeDigest([finding({ severity: "Low" })]);
  const unnamed = decision(digest, {
    verdict: "request-changes",
    findings: [{ key: digest.findings[0].key, gating: true }],
  });
  const refused = validateVerdictDecision(unnamed, digest);
  assert.equal(refused.ok, false);
  assert.match(refused.reason, /undergrade basis/);

  const named = decision(digest, {
    verdict: "request-changes",
    findings: [{ key: digest.findings[0].key, gating: true, undergrade: "a data-loss path graded Low" }],
  });
  assert.deepEqual(validateVerdictDecision(named, digest), { ok: true });
});

test("every confirmed finding needs exactly one disposition", () => {
  const digest = completeDigest([finding(), finding({ id: "specialist-3:1", title: "Second defect", line: 9 })]);
  const missing = decision(digest, { findings: [decision(digest).findings[0]] });
  const refusedMissing = validateVerdictDecision(missing, digest);
  assert.equal(refusedMissing.ok, false);
  assert.match(refusedMissing.reason, /no disposition/);

  const doubled = decision(digest, {
    findings: [decision(digest).findings[0], decision(digest).findings[0], decision(digest).findings[1]],
  });
  const refusedDoubled = validateVerdictDecision(doubled, digest);
  assert.equal(refusedDoubled.ok, false);
  assert.match(refusedDoubled.reason, /disposed of twice/);

  const unknown = decision(digest, {
    findings: [...decision(digest).findings, { key: "no-such-key", gating: false }],
  });
  const refusedUnknown = validateVerdictDecision(unknown, digest);
  assert.equal(refusedUnknown.ok, false);
  assert.match(refusedUnknown.reason, /unknown finding/);
});

test("the verdict must match the gating outcome in both directions", () => {
  const digest = completeDigest([finding()]);
  const understated = decision(digest, { verdict: "clean" });
  const refused = validateVerdictDecision(understated, digest);
  assert.equal(refused.ok, false);
  assert.match(refused.reason, /request-changes/);

  const cleanDigest = completeDigest([finding({ severity: "Low" })]);
  const overstated = decision(cleanDigest, {
    verdict: "request-changes",
    findings: [{ key: cleanDigest.findings[0].key, gating: false }],
  });
  const refusedClean = validateVerdictDecision(overstated, cleanDigest);
  assert.equal(refusedClean.ok, false);
  assert.match(refusedClean.reason, /verdict is clean/);
});

test("CLI --digest and --validate run the same policy; argument errors exit 2", () => {
  const scratch = mkdtempSync(join(tmpdir(), "verdict-classification-"));
  const verdictPath = join(scratch, "verdict.json");
  writeFileSync(verdictPath, JSON.stringify(verdict([finding()])));
  const digest = JSON.parse(execFileSync(
    process.execPath, [cliPath, "--digest", verdictPath], { encoding: "utf8" },
  ));
  assert.equal(digest.status, "complete");
  assert.equal(digest.threshold, "High");
  assert.equal(digest.thresholdIndication, "request-changes");

  const decisionPath = join(scratch, "decision.json");
  writeFileSync(decisionPath, JSON.stringify(decision(digest)));
  const validated = JSON.parse(execFileSync(
    process.execPath, [cliPath, "--validate", verdictPath, decisionPath], { encoding: "utf8" },
  ));
  assert.deepEqual(validated, { ok: true });

  const usage = (args) => {
    try {
      execFileSync(process.execPath, [cliPath, ...args], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
      assert.fail("expected a usage exit");
    } catch (error) {
      assert.equal(error.status, 2);
      assert.match(String(error.stderr), /usage:/);
    }
  };
  usage([]);
  usage(["--digest"]);
  usage(["--validate", verdictPath]);
});
