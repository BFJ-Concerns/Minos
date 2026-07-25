import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { adjudicate } from "./run-record-adjudicator.mjs";

const legs = [
  { label: "exploration", role: "exploration", expectedFamily: "gpt", pinnedModel: "gpt-5.6-terra" },
  { label: "specialist-1", role: "specialist", expectedFamily: "gpt", pinnedModel: "gpt-5.6-sol" },
  { label: "verify-1", role: "verifier", expectedFamily: "claude", pinnedModel: "claude-opus-5" },
];

const envelope = {
  reviewed: { target: "target-sha", head: "head-sha", occasion: null },
  stage: "present",
  requiredModelEvidence: legs,
  proposedFindings: [{
    id: "specialist-1:1",
    source: "Correctness",
    title: "Distinct failure",
    severity: "High",
    confidence: 86,
    path: "internal/review.go",
    line: 42,
    explanation: "The changed branch accepts an invalid state.",
    proposingLabel: "specialist-1",
    verifyLabel: "verify-1",
    rawVerifier: { verdict: "upheld", confidence: 94, reason: "The invalid state is reachable." },
  }],
  briefs: [{ brief: ".review/errors.md", title: "Error handling", status: "run", reason: "applicable concern reviewed" }],
  dispatches: [{ brief: ".review/errors.md", label: "specialist-1" }],
  reviewers: [],
};

function fixtureArchive(t, {
  manifestStatus = "complete",
  records = {
    exploration: { status: "complete", resolved_model: "gpt-5.6-terra-served" },
    "specialist-1": { status: "complete", resolved_model: "gpt-5.6-sol-served" },
    "verify-1": { status: "complete", resolved_model: "claude-opus-5" },
  },
  manifests = 1,
  agentsDirectory = true,
} = {}) {
  const recordDir = mkdtempSync(join(tmpdir(), "minos-adjudicator-fixture-"));
  t.after(() => rmSync(recordDir, { recursive: true, force: true }));
  for (let run = 0; run < manifests; run += 1) {
    const archive = join(recordDir, "runs", "cwd", `namespace-${run}`, `run-${run}`);
    mkdirSync(agentsDirectory ? join(archive, "agents") : archive, { recursive: true });
    writeFileSync(join(archive, "manifest.json"), JSON.stringify({ kind: "run_manifest", status: manifestStatus }));
    Object.entries(records).forEach(([label, record], index) => {
      const directory = join(archive, "agents", String(index + 1).padStart(6, "0"));
      mkdirSync(directory, { recursive: true });
      writeFileSync(join(directory, "agent.json"), JSON.stringify({ label, ...record }));
    });
  }
  return recordDir;
}

test("fixture archive with every distinctive served family produces a complete adapter verdict", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t);
  const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });

  assert.equal(adapterVerdict.status, "complete");
  assert.equal(adapterVerdict.complete, true);
  assert.deepEqual(adapterVerdict.confirmedFindings.map((finding) => finding.id), ["specialist-1:1"]);
  assert.equal(adapterVerdict.reviewBody.comments.length, 1);
  assert.equal(adapterVerdict.reviewBody.body, "Repository review brief findings.");
  assert.equal(adapterVerdict.fixRequired, true);
  assert.deepEqual(
    adapterVerdict.modelEvidence.map(({ label, resolvedModel, confirmed }) => ({ label, resolvedModel, confirmed })),
    [
      { label: "exploration", resolvedModel: "gpt-5.6-terra-served", confirmed: true },
      { label: "specialist-1", resolvedModel: "gpt-5.6-sol-served", confirmed: true },
      { label: "verify-1", resolvedModel: "claude-opus-5", confirmed: true },
    ],
  );
});

test("a refuted verifier completes the run without publishing a finding", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t);
  const refutedEnvelope = {
    ...envelope,
    proposedFindings: [{
      ...envelope.proposedFindings[0],
      rawVerifier: { verdict: "refuted", confidence: 94, reason: "The invalid state is not reachable." },
    }],
  };
  const adapterVerdict = await adjudicate({ envelope: refutedEnvelope, recordDir: fixtureArchiveDir });

  assert.equal(adapterVerdict.status, "complete");
  assert.equal(adapterVerdict.complete, true);
  assert.deepEqual(adapterVerdict.confirmedFindings, []);
  assert.equal(adapterVerdict.reviewBody, null);
  assert.equal(adapterVerdict.fixRequired, false);
});

test("operator attention follows the combined-confidence threshold", async (t) => {
  const lowFixtureArchiveDir = fixtureArchive(t);
  const lowConfidenceEnvelope = {
    ...envelope,
    proposedFindings: [{
      ...envelope.proposedFindings[0],
      confidence: 40,
      rawVerifier: { verdict: "upheld", confidence: 50, reason: "The invalid state is reachable." },
    }],
  };
  const lowVerdict = await adjudicate({ envelope: lowConfidenceEnvelope, recordDir: lowFixtureArchiveDir });

  assert.equal(lowVerdict.status, "complete");
  assert.deepEqual(lowVerdict.operatorAttention, [{
    finding: "specialist-1:1",
    title: "Distinct failure",
    combinedConfidence: 45,
    threshold: 70,
  }]);

  const highFixtureArchiveDir = fixtureArchive(t);
  const highVerdict = await adjudicate({ envelope, recordDir: highFixtureArchiveDir });
  assert.equal(highVerdict.status, "complete");
  assert.deepEqual(highVerdict.operatorAttention, []);
});

test("fixture archive with a wrong served family fails closed despite the correct requested model", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t, {
    records: {
      exploration: { status: "complete", model: "gpt-5.6-terra", resolved_model: "gpt-5.6-terra-served" },
      "specialist-1": { status: "complete", model: "gpt-5.6-sol", resolved_model: "claude-opus-4-8-fallback" },
      "verify-1": { status: "complete", model: "claude-opus-5", resolved_model: "claude-opus-5" },
    },
  });
  const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });

  assert.equal(adapterVerdict.status, "incomplete");
  assert.equal(adapterVerdict.modelEvidence.find((entry) => entry.label === "specialist-1").confirmed, false);
  assert.deepEqual(adapterVerdict.confirmedFindings, []);
  assert.equal(adapterVerdict.reviewBody, null);
  assert.equal(adapterVerdict.fixRequired, false);
});

test("null resolved_model fails closed", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t, {
    records: {
      exploration: { status: "complete", resolved_model: "gpt-5.6-terra" },
      "specialist-1": { status: "complete", resolved_model: null },
      "verify-1": { status: "complete", resolved_model: "claude-opus-5" },
    },
  });
  const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });
  assert.equal(adapterVerdict.status, "incomplete");
  assert.match(adapterVerdict.incomplete.join("\n"), /specialist-1 has no resolved_model/);
});

for (const manifestStatus of ["failed", "timed-out"]) {
  test(`manifest ${manifestStatus} is an infrastructure failure`, async (t) => {
    const fixtureArchiveDir = fixtureArchive(t, { manifestStatus });
    const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });
    assert.equal(adapterVerdict.status, "infrastructure-failure");
    assert.equal(adapterVerdict.infrastructureFailure.kind, manifestStatus);
    assert.deepEqual(adapterVerdict.confirmedFindings, []);
  });
}

for (const manifests of [0, 2]) {
  test(`${manifests} manifests fail closed`, async (t) => {
    const fixtureArchiveDir = fixtureArchive(t, { manifests });
    const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });
    assert.equal(adapterVerdict.status, "incomplete");
    assert.match(adapterVerdict.incomplete[0], new RegExp(`contains ${manifests} manifests`));
  });
}

test("an upheld verifier on an unconfirmed specialist leg yields no verdict and publishes nothing", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t, {
    records: {
      exploration: { status: "complete", resolved_model: "gpt-5.6-terra" },
      "specialist-1": { status: "failed", resolved_model: "gpt-5.6-sol" },
      "verify-1": { status: "complete", resolved_model: "claude-opus-5" },
    },
  });
  const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });
  assert.equal(adapterVerdict.status, "incomplete");
  assert.deepEqual(adapterVerdict.confirmedFindings, []);
  assert.equal(adapterVerdict.reviewBody, null);
  assert.match(adapterVerdict.incomplete.join("\n"), /no complete verdict/);
});

test("one served leg cannot act as both proposer and verifier", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t);
  const sameLegEnvelope = {
    ...envelope,
    proposedFindings: [{ ...envelope.proposedFindings[0], verifyLabel: "specialist-1" }],
  };
  const adapterVerdict = await adjudicate({ envelope: sameLegEnvelope, recordDir: fixtureArchiveDir });

  assert.equal(adapterVerdict.status, "incomplete");
  assert.deepEqual(adapterVerdict.confirmedFindings, []);
  assert.equal(adapterVerdict.reviewBody, null);
  assert.match(adapterVerdict.incomplete.join("\n"), /same model family/);
});

test("distinct proposer and verifier legs served by the same family yield no verdict", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t, {
    records: {
      exploration: { status: "complete", resolved_model: "gpt-5.6-terra-served" },
      "specialist-1": { status: "complete", resolved_model: "gpt-5.6-sol-served" },
      "verify-1": { status: "complete", resolved_model: "gpt-5.6-sol-verifier" },
    },
  });
  const sameFamilyEnvelope = {
    ...envelope,
    requiredModelEvidence: envelope.requiredModelEvidence.map((leg) => leg.label === "verify-1"
      ? { ...leg, expectedFamily: "gpt", pinnedModel: "gpt-5.6-sol" }
      : leg),
  };
  const adapterVerdict = await adjudicate({ envelope: sameFamilyEnvelope, recordDir: fixtureArchiveDir });

  assert.equal(adapterVerdict.status, "incomplete");
  assert.deepEqual(adapterVerdict.confirmedFindings, []);
  assert.equal(adapterVerdict.reviewBody, null);
  assert.match(adapterVerdict.incomplete.join("\n"), /same model family/);
});

test("an unconfirmed unpaired exploration leg makes the whole verdict incomplete", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t, {
    records: {
      exploration: { status: "complete", resolved_model: "claude-opus-4-8" },
      "specialist-1": { status: "complete", resolved_model: "gpt-5.6-sol" },
      "verify-1": { status: "complete", resolved_model: "claude-opus-5" },
    },
  });
  const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });
  assert.equal(adapterVerdict.status, "incomplete");
  assert.deepEqual(adapterVerdict.confirmedFindings, []);
  assert.match(adapterVerdict.incomplete.join("\n"), /exploration resolved to claude, expected gpt/);
});

test("a malformed proposed finding cannot become publishable", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t);
  const malformedEnvelope = {
    ...envelope,
    proposedFindings: [{ ...envelope.proposedFindings[0], path: "" }],
  };
  const adapterVerdict = await adjudicate({ envelope: malformedEnvelope, recordDir: fixtureArchiveDir });
  assert.equal(adapterVerdict.status, "incomplete");
  assert.deepEqual(adapterVerdict.confirmedFindings, []);
  assert.equal(adapterVerdict.reviewBody, null);
  assert.match(adapterVerdict.incomplete.join("\n"), /proposed finding 1 is absent or malformed/);
});

test("a zero-leg archive needs no agents directory", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t, { records: {}, agentsDirectory: false });
  const absentEnvelope = {
    reviewed: { target: "target-sha", head: "head-sha", occasion: null },
    stage: "absent",
    requiredModelEvidence: [],
    proposedFindings: [],
    briefs: [],
    dispatches: [],
    reviewers: [],
  };
  const adapterVerdict = await adjudicate({ envelope: absentEnvelope, recordDir: fixtureArchiveDir });
  assert.equal(adapterVerdict.status, "complete");
  assert.deepEqual(adapterVerdict.modelEvidence, []);
});
