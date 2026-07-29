import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { adjudicate } from "./run-record-adjudicator.mjs";

const legs = [
  { label: "exploration", role: "exploration", pinnedModel: "gpt-5.6-terra" },
  { label: "specialist-1", role: "specialist", pinnedModel: "gpt-5.6-sol" },
  { label: "verify-1", role: "verifier", pinnedModel: "claude-opus-5" },
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
  misconfigurations: [],
  dispatches: [{ brief: ".review/errors.md", label: "specialist-1" }],
  reviewers: [],
};

const zeroLegEnvelope = {
  reviewed: { target: "target-sha", head: "head-sha", occasion: null },
  stage: "absent",
  requiredModelEvidence: [],
  proposedFindings: [],
  briefs: [],
  misconfigurations: [],
  dispatches: [],
  reviewers: [],
};

function fixtureArchive(t, {
  manifestStatus = "complete",
  records = {
    exploration: { status: "complete", resolved_model: "gpt-5.6-terra-served" },
    "specialist-1": { status: "complete", resolved_model: "gpt-5.6-sol-served" },
    "verify-1": { status: "complete", resolved_model: "claude-opus-5" },
  },
  duplicateRecords = [],
  rawAgentRecords = [],
  manifests = 1,
  agentsDirectory = true,
} = {}) {
  const recordDir = mkdtempSync(join(tmpdir(), "minos-adjudicator-fixture-"));
  t.after(() => rmSync(recordDir, { recursive: true, force: true }));
  for (let run = 0; run < manifests; run += 1) {
    const archive = join(recordDir, "runs", "cwd", `namespace-${run}`, `run-${run}`);
    mkdirSync(agentsDirectory ? join(archive, "agents") : archive, { recursive: true });
    writeFileSync(join(archive, "manifest.json"), JSON.stringify({ kind: "run_manifest", status: manifestStatus }));
    const agentRecords = [
      ...[...Object.entries(records), ...duplicateRecords]
        .map(([label, record]) => JSON.stringify({ label, ...record })),
      ...rawAgentRecords,
    ];
    agentRecords.forEach((record, index) => {
      const directory = join(archive, "agents", String(index + 1).padStart(6, "0"));
      mkdirSync(directory, { recursive: true });
      writeFileSync(join(directory, "agent.json"), record);
    });
  }
  return recordDir;
}

function assertWithheld(adapterVerdict, condition) {
  assert.notEqual(adapterVerdict.status, "complete");
  assert.ok(adapterVerdict.incomplete.length > 0);
  if (typeof condition === "string")
    assert.ok(adapterVerdict.incomplete.includes(condition), adapterVerdict.incomplete.join("\n"));
  else
    assert.ok(
      adapterVerdict.incomplete.some((entry) => condition.test(entry)),
      adapterVerdict.incomplete.join("\n"),
    );
  assert.deepEqual(adapterVerdict.confirmedFindings, []);
  assert.equal(adapterVerdict.reviewBody, null);
  assert.equal(adapterVerdict.fixRequired, false);
}

function missingRecordDirectory(t) {
  const recordDir = mkdtempSync(join(tmpdir(), "minos-missing-adjudicator-fixture-"));
  rmSync(recordDir, { recursive: true });
  t.after(() => rmSync(recordDir, { recursive: true, force: true }));
  return recordDir;
}

test("complete legs and a complete verifier result produce a complete adapter verdict", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t);
  const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });

  assert.equal(adapterVerdict.status, "complete");
  assert.equal(adapterVerdict.complete, true);
  assert.deepEqual(adapterVerdict.confirmedFindings.map((finding) => finding.id), ["specialist-1:1"]);
  assert.equal(adapterVerdict.reviewBody.comments.length, 1);
  assert.deepEqual(
    adapterVerdict.reviewBody.comments.map(({ path, line }) => ({ path, line })),
    [{ path: "internal/review.go", line: 42 }],
  );
  assert.equal("new_position" in adapterVerdict.reviewBody.comments[0], false);
  assert.equal(adapterVerdict.reviewBody.body, "Repository review brief findings.");
  assert.equal(adapterVerdict.fixRequired, true);
  assert.deepEqual(adapterVerdict.ran, [{ brief: ".review/errors.md", title: "Error handling" }]);
  assert.deepEqual(adapterVerdict.skipped, []);
  assert.deepEqual(adapterVerdict.misconfigurations, []);
  assert.deepEqual(
    adapterVerdict.modelEvidence.map(({ label, resolvedModel, status }) => ({ label, resolvedModel, status })),
    [
      { label: "exploration", resolvedModel: "gpt-5.6-terra-served", status: "complete" },
      { label: "specialist-1", resolvedModel: "gpt-5.6-sol-served", status: "complete" },
      { label: "verify-1", resolvedModel: "claude-opus-5", status: "complete" },
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

test("an unexpected resolved_model family does not withhold a complete verdict", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t, {
    records: {
      exploration: { status: "complete", resolved_model: "gpt-5.6-terra-served" },
      "specialist-1": { status: "complete", resolved_model: "claude-opus-5" },
      "verify-1": { status: "complete", resolved_model: "claude-opus-5" },
    },
  });
  const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });

  assert.equal(adapterVerdict.status, "complete");
  assert.equal(adapterVerdict.complete, true);
  assert.deepEqual(adapterVerdict.confirmedFindings.map((finding) => finding.id), ["specialist-1:1"]);
  assert.equal(adapterVerdict.modelEvidence.find((entry) => entry.label === "specialist-1").resolvedModel, "claude-opus-5");
});

test("missing resolved_model does not withhold a complete verdict", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t, {
    records: {
      exploration: { status: "complete" },
      "specialist-1": { status: "complete" },
      "verify-1": { status: "complete" },
    },
  });
  const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });
  assert.equal(adapterVerdict.status, "complete");
  assert.equal(adapterVerdict.complete, true);
  assert.deepEqual(adapterVerdict.confirmedFindings.map((finding) => finding.id), ["specialist-1:1"]);
  assert.ok(adapterVerdict.modelEvidence.every((entry) => entry.resolvedModel === null));
});

for (const manifestStatus of ["failed", "timed-out", "interrupted"]) {
  test(`manifest ${manifestStatus} is an infrastructure failure`, async (t) => {
    const fixtureArchiveDir = fixtureArchive(t, { manifestStatus });
    const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });
    assert.equal(adapterVerdict.status, "infrastructure-failure");
    assert.equal(adapterVerdict.infrastructureFailure.kind, manifestStatus);
    assertWithheld(adapterVerdict, `ensemble run ended ${manifestStatus}`);
  });
}

for (const manifests of [0, 2]) {
  test(`${manifests} manifests fail closed`, async (t) => {
    const fixtureArchiveDir = fixtureArchive(t, { manifests });
    const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });
    assertWithheld(
      adapterVerdict,
      `run-record directory contains ${manifests} manifests; expected exactly one`,
    );
  });
}

for (const {
  description,
  archive = {},
  caseEnvelope = envelope,
  condition,
} of [
  {
    description: "unreadable agent JSON",
    archive: { rawAgentRecords: ["not-json"] },
    condition: /^agent records are unreadable: .*not-json.*$/,
  },
  {
    description: "a zero-leg archive with unreadable agent JSON",
    archive: { records: {}, rawAgentRecords: ["not-json"] },
    caseEnvelope: zeroLegEnvelope,
    condition: /^agent records are unreadable: .*not-json.*$/,
  },
  {
    description: "a missing agents directory with required legs",
    archive: { records: {}, agentsDirectory: false },
    condition: /^agent records are unreadable: ENOENT: no such file or directory, scandir '.+\/agents'$/,
  },
  {
    description: "an agent record without a readable label",
    archive: { duplicateRecords: [["ignored", { label: "", status: "complete" }]] },
    condition: "agent record 000004 has no readable label",
  },
  {
    description: "a malformed required model-evidence leg",
    caseEnvelope: {
      ...envelope,
      requiredModelEvidence: [{ label: "exploration" }, ...legs.slice(1)],
    },
    condition: "required model-evidence leg 1 is malformed",
  },
  {
    description: "an array envelope",
    caseEnvelope: [],
    condition: "launcher returned an unreadable envelope",
  },
  {
    description: "an absent reviewed identity",
    caseEnvelope: { ...envelope, reviewed: undefined },
    condition: "envelope reviewed identity is absent",
  },
  {
    description: "an absent reviewed target",
    caseEnvelope: { ...envelope, reviewed: { ...envelope.reviewed, target: "" } },
    condition: "envelope reviewed target is absent or unreadable",
  },
  {
    description: "an absent reviewed head",
    caseEnvelope: { ...envelope, reviewed: { ...envelope.reviewed, head: "" } },
    condition: "envelope reviewed head is absent or unreadable",
  },
  {
    description: "an unreadable reviewed occasion",
    caseEnvelope: { ...envelope, reviewed: { ...envelope.reviewed, occasion: 42 } },
    condition: "envelope reviewed occasion is unreadable",
  },
  {
    description: "an unknown stage",
    caseEnvelope: { ...envelope, stage: "unknown" },
    condition: "envelope stage is absent or unknown",
  },
  {
    description: "a running manifest",
    archive: { manifestStatus: "running" },
    condition: "run manifest status is running; expected complete",
  },
  {
    description: "a manifest without a readable status",
    archive: { manifestStatus: null },
    condition: "run manifest status is absent or unreadable",
  },
  ...[
    "requiredModelEvidence",
    "proposedFindings",
    "briefs",
    "dispatches",
    "reviewers",
  ].map((field) => ({
    description: `an unreadable ${field} field`,
    caseEnvelope: { ...envelope, [field]: null },
    condition: `envelope ${field} is absent or unreadable`,
  })),
  {
    description: "a not-run brief disposition",
    caseEnvelope: {
      ...envelope,
      briefs: [{
        brief: ".review/errors.md",
        title: "Error handling",
        status: "not-run",
        reason: "specialist returned no result",
      }],
    },
    condition: 'brief "Error handling" was not run (specialist returned no result)',
  },
]) {
  test(`${description} cannot become publishable`, { timeout: 1000 }, async (t) => {
    const fixtureArchiveDir = fixtureArchive(t, archive);
    const adapterVerdict = await adjudicate({ envelope: caseEnvelope, recordDir: fixtureArchiveDir });
    assertWithheld(adapterVerdict, condition);
  });
}

for (const [description, recordDir] of [
  ["a non-string record directory", null],
  ["an empty record directory", ""],
]) {
  test(`${description} cannot become publishable`, { timeout: 1000 }, async () => {
    const adapterVerdict = await adjudicate({ envelope, recordDir });
    assertWithheld(adapterVerdict, "run-record directory is absent");
  });
}

test("an unreadable record directory cannot become publishable", { timeout: 1000 }, async (t) => {
  const recordDir = missingRecordDirectory(t);
  const adapterVerdict = await adjudicate({ envelope, recordDir });
  assertWithheld(
    adapterVerdict,
    /^run-record directory could not be read: ENOENT: no such file or directory, scandir '.+'$/,
  );
});

test("a non-complete leg makes the run incomplete", async (t) => {
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
  assert.match(adapterVerdict.incomplete.join("\n"), /specialist-1 has status failed; expected complete/);
  assert.match(adapterVerdict.incomplete.join("\n"), /no complete verdict/);
});

test("an absent required agent record makes the run incomplete", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t, {
    records: {
      exploration: { status: "complete", resolved_model: "gpt-5.6-terra-served" },
      "verify-1": { status: "complete", resolved_model: "claude-opus-5" },
    },
  });
  const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });
  assert.equal(adapterVerdict.status, "incomplete");
  assert.match(adapterVerdict.incomplete.join("\n"), /agent record for specialist-1 is absent or ambiguous/);
});

test("ambiguous agent records make the run incomplete", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t, {
    duplicateRecords: [["specialist-1", { status: "complete", resolved_model: "gpt-5.6-sol-served" }]],
  });
  const adapterVerdict = await adjudicate({ envelope, recordDir: fixtureArchiveDir });
  assert.equal(adapterVerdict.status, "incomplete");
  assert.match(adapterVerdict.incomplete.join("\n"), /agent label specialist-1 occurs more than once/);
  assert.match(adapterVerdict.incomplete.join("\n"), /agent record for specialist-1 is absent or ambiguous/);
});

test("duplicate required labels make the run incomplete", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t);
  const duplicateLegEnvelope = {
    ...envelope,
    requiredModelEvidence: [...envelope.requiredModelEvidence, envelope.requiredModelEvidence[1]],
  };
  const adapterVerdict = await adjudicate({ envelope: duplicateLegEnvelope, recordDir: fixtureArchiveDir });
  assert.equal(adapterVerdict.status, "incomplete");
  assert.match(adapterVerdict.incomplete.join("\n"), /required model-evidence label specialist-1 occurs more than once/);
});

test("a missing verifier result makes the run incomplete", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t);
  const missingVerifierEnvelope = {
    ...envelope,
    proposedFindings: [{ ...envelope.proposedFindings[0], rawVerifier: null }],
  };
  const adapterVerdict = await adjudicate({ envelope: missingVerifierEnvelope, recordDir: fixtureArchiveDir });
  assert.equal(adapterVerdict.status, "incomplete");
  assert.match(adapterVerdict.incomplete.join("\n"), /verifier returned no valid result/);
});

test("an invalid verifier result makes the run incomplete", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t);
  const invalidVerifierEnvelope = {
    ...envelope,
    proposedFindings: [{
      ...envelope.proposedFindings[0],
      rawVerifier: { verdict: "maybe", confidence: 94, reason: "No verdict." },
    }],
  };
  const adapterVerdict = await adjudicate({ envelope: invalidVerifierEnvelope, recordDir: fixtureArchiveDir });
  assert.equal(adapterVerdict.status, "incomplete");
  assert.match(adapterVerdict.incomplete.join("\n"), /verifier returned no valid result/);
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

for (const [description, findingField] of [
  ["an unknown severity", { severity: "Wibble" }],
  ["an out-of-range confidence", { confidence: 999 }],
  ["an invalid line number", { line: -5 }],
]) {
  test(`a proposed finding with ${description} cannot become publishable`, { timeout: 1000 }, async (t) => {
    const fixtureArchiveDir = fixtureArchive(t);
    const malformedEnvelope = {
      ...envelope,
      proposedFindings: [{ ...envelope.proposedFindings[0], ...findingField }],
    };
    const adapterVerdict = await adjudicate({ envelope: malformedEnvelope, recordDir: fixtureArchiveDir });
    assertWithheld(adapterVerdict, "proposed finding 1 is absent or malformed");
  });
}

for (const [description, verifierField] of [
  ["a non-integer confidence", { confidence: "lots" }],
  ["a non-string reason", { reason: null }],
]) {
  test(`a verifier result with ${description} cannot become publishable`, { timeout: 1000 }, async (t) => {
    const fixtureArchiveDir = fixtureArchive(t);
    const malformedEnvelope = {
      ...envelope,
      proposedFindings: [{
        ...envelope.proposedFindings[0],
        rawVerifier: { ...envelope.proposedFindings[0].rawVerifier, ...verifierField },
      }],
    };
    const adapterVerdict = await adjudicate({ envelope: malformedEnvelope, recordDir: fixtureArchiveDir });
    assertWithheld(
      adapterVerdict,
      'finding "Distinct failure" has no complete verdict (verifier returned no valid result)',
    );
  });
}

for (const [description, briefDisposition, condition] of [
  [
    "run disposition with no title",
    { brief: ".review/errors.md", status: "run" },
    /envelope contains a malformed run disposition/,
  ],
  [
    "skipped disposition with an empty reason",
    {
      brief: ".review/errors.md",
      title: "Error handling",
      status: "skipped",
      skipKind: "inapplicable",
      reason: "",
    },
    /envelope contains a malformed skipped disposition/,
  ],
]) {
  test(`a malformed ${description} cannot become publishable`, { timeout: 1000 }, async (t) => {
    const fixtureArchiveDir = fixtureArchive(t);
    const malformedEnvelope = {
      ...envelope,
      briefs: [briefDisposition],
    };
    const adapterVerdict = await adjudicate({ envelope: malformedEnvelope, recordDir: fixtureArchiveDir });
    assert.equal(adapterVerdict.status, "incomplete");
    assert.deepEqual(adapterVerdict.confirmedFindings, []);
    assert.equal(adapterVerdict.reviewBody, null);
    assert.equal(adapterVerdict.fixRequired, false);
    assert.match(adapterVerdict.incomplete.join("\n"), condition);
  });
}

test("a malformed misconfigurations field cannot become publishable", { timeout: 1000 }, async (t) => {
  const fixtureArchiveDir = fixtureArchive(t);
  const malformedEnvelope = {
    ...envelope,
    misconfigurations: null,
  };
  const adapterVerdict = await adjudicate({ envelope: malformedEnvelope, recordDir: fixtureArchiveDir });
  assert.equal(adapterVerdict.status, "incomplete");
  assert.deepEqual(adapterVerdict.confirmedFindings, []);
  assert.equal(adapterVerdict.reviewBody, null);
  assert.match(adapterVerdict.incomplete.join("\n"), /envelope misconfigurations is absent or unreadable/);
});

for (const [description, misconfiguration] of [
  ["missing kind", {
    brief: ".review/missing/scoped.md",
    title: "Scoped",
    reason: "brief scope missing/ matches no repository directory",
  }],
  ["empty reason", {
    brief: ".review/missing/scoped.md",
    title: "Scoped",
    kind: "misconfigured-scope",
    reason: "",
  }],
]) {
  test(`a misconfiguration entry with ${description} cannot become publishable`, { timeout: 1000 }, async (t) => {
    const fixtureArchiveDir = fixtureArchive(t);
    const malformedEnvelope = {
      ...envelope,
      misconfigurations: [misconfiguration],
    };
    const adapterVerdict = await adjudicate({ envelope: malformedEnvelope, recordDir: fixtureArchiveDir });
    assert.equal(adapterVerdict.status, "incomplete");
    assert.deepEqual(adapterVerdict.confirmedFindings, []);
    assert.equal(adapterVerdict.reviewBody, null);
    assert.equal(adapterVerdict.fixRequired, false);
    assert.match(adapterVerdict.incomplete.join("\n"), /envelope contains a malformed brief misconfiguration/);
  });
}

test("a zero-leg archive needs no agents directory", async (t) => {
  const fixtureArchiveDir = fixtureArchive(t, { records: {}, agentsDirectory: false });
  const adapterVerdict = await adjudicate({ envelope: zeroLegEnvelope, recordDir: fixtureArchiveDir });
  assert.equal(adapterVerdict.status, "complete");
  assert.deepEqual(adapterVerdict.modelEvidence, []);
});
