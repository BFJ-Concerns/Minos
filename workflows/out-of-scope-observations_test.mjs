import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { attachOutOfScopeObservations } from "./out-of-scope-observations.mjs";
import { adjudicate } from "./run-record-adjudicator.mjs";

test("an unverified observation survives adjudication without becoming a finding", async (t) => {
  const recordDir = mkdtempSync(join(tmpdir(), "minos-observation-adjudication-"));
  t.after(() => rmSync(recordDir, { recursive: true, force: true }));
  const archive = join(recordDir, "runs", "cwd", "namespace", "run");
  mkdirSync(archive, { recursive: true });
  writeFileSync(join(archive, "manifest.json"), JSON.stringify({
    kind: "run_manifest",
    status: "complete",
  }));

  const observation = {
    id: "specialist-1-correctness-gpt:observation:1",
    source: "Correctness",
    member: "primary",
    title: "pre-existing defect",
    path: "internal/legacy.go",
    line: 9,
    explanation: "Unverified observation: unchanged code admits an invalid state.",
    observingLabel: "specialist-1-correctness-gpt",
    verified: false,
  };
  const envelope = {
    reviewed: { target: "target-sha", head: "head-sha", occasion: null },
    stage: "present",
    requiredModelEvidence: [],
    proposedFindings: [],
    outOfScopeObservations: [
      observation,
      { ...observation, id: "invalid-observation", verified: true },
    ],
    briefs: [],
    misconfigurations: [],
    dispatches: [],
    reviewers: [],
  };

  const verdict = attachOutOfScopeObservations(
    await adjudicate({ envelope, recordDir }),
    envelope,
  );

  assert.equal(verdict.status, "complete");
  assert.deepEqual(verdict.confirmedFindings, []);
  assert.deepEqual(verdict.outOfScopeObservations, [observation]);
  assert.equal(verdict.outOfScopeObservations[0].verified, false);
  assert.ok(!("verdict" in verdict.outOfScopeObservations[0]));
  assert.ok(!("severity" in verdict.outOfScopeObservations[0]));
  assert.ok(!("confidence" in verdict.outOfScopeObservations[0]));
});

test("grouped observations retain known members and discard unattributed entries", () => {
  const base = {
    id: "specialist-1:observation:1",
    source: "Correctness",
    title: "pre-existing defect",
    path: "internal/legacy.go",
    line: 9,
    explanation: "Unverified observation: unchanged code admits an invalid state.",
    observingLabel: "specialist-1",
    verified: false,
  };
  const envelope = {
    members: [{ id: "primary" }, { id: "sibling" }],
    outOfScopeObservations: [
      { ...base, member: "sibling" },
      { ...base, id: "missing-member" },
      { ...base, id: "unknown-member", member: "unknown" },
    ],
  };

  assert.deepEqual(attachOutOfScopeObservations({}, envelope).outOfScopeObservations, [
    { ...base, member: "sibling" },
  ]);
});
