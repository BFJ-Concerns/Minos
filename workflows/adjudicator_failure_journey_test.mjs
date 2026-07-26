import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { adjudicate } from "./run-record-adjudicator.mjs";

const workflowsDir = dirname(fileURLToPath(import.meta.url));

const envelope = {
  reviewed: { target: "target-sha", head: "head-sha", occasion: null },
  stage: "present",
  requiredModelEvidence: [
    { label: "specialist", role: "specialist", pinnedModel: "gpt-5.6-sol" },
    { label: "verifier", role: "verifier", pinnedModel: "claude-opus-5" },
  ],
  proposedFindings: [{
    id: "specialist:1",
    source: "Correctness",
    title: "Publishable sentinel",
    severity: "High",
    confidence: 90,
    path: "internal/review.go",
    line: 42,
    explanation: "This finding must disappear when adjudication is incomplete.",
    proposingLabel: "specialist",
    verifyLabel: "verifier",
    rawVerifier: {
      verdict: "upheld",
      confidence: 92,
      reason: "The defect is reachable.",
    },
  }],
  briefs: [],
  dispatches: [],
  reviewers: [],
};

function archive(t, manifest) {
  const recordDir = mkdtempSync(join(tmpdir(), "minos-adjudicator-failure-"));
  t.after(() => rmSync(recordDir, { recursive: true, force: true }));
  const runDir = join(recordDir, "runs", "cwd", "namespace", "run");
  mkdirSync(join(runDir, "agents", "000001"), { recursive: true });
  mkdirSync(join(runDir, "agents", "000002"), { recursive: true });
  writeFileSync(
    join(runDir, "manifest.json"),
    typeof manifest === "string" ? manifest : JSON.stringify(manifest),
  );
  writeFileSync(
    join(runDir, "agents", "000001", "agent.json"),
    JSON.stringify({ label: "specialist", status: "complete", resolved_model: "gpt-5.6-sol" }),
  );
  writeFileSync(
    join(runDir, "agents", "000002", "agent.json"),
    JSON.stringify({ label: "verifier", status: "complete", resolved_model: "claude-opus-5" }),
  );
  return recordDir;
}

function assertWithheld(verdict) {
  assert.equal(verdict.status, "incomplete");
  assert.equal(verdict.complete, false);
  assert.deepEqual(verdict.confirmedFindings, []);
  assert.equal(verdict.reviewBody, null);
  assert.equal(verdict.fixRequired, false);
}

test("the executable adjudicator fails closed on invalid invocation", () => {
  const result = spawnSync(
    process.execPath,
    [join(workflowsDir, "adjudicated-review")],
    {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    },
  );

  assert.equal(result.status, 0);
  assert.equal(result.signal, null);
  const verdict = JSON.parse(result.stdout);
  assertWithheld(verdict);
  assert.deepEqual(verdict.incomplete, [
    "usage: adjudicated-review <workflow-script-abs-path> --json-args @<argsfile>",
  ]);
  assert.match(result.stderr, /adjudicated-review: usage:/);
  assert.match(result.stderr, /\[workflow\] complete/);
});

test("a malformed reviewed identity suppresses an otherwise confirmed finding", async (t) => {
  const recordDir = archive(t, { kind: "run_manifest", status: "complete" });
  const verdict = await adjudicate({
    envelope: {
      ...envelope,
      reviewed: { ...envelope.reviewed, head: null },
    },
    recordDir,
  });

  assertWithheld(verdict);
  assert.deepEqual(verdict.incomplete, ["envelope reviewed head is absent or unreadable"]);
});

test("a corrupt run manifest suppresses an otherwise confirmed finding", async (t) => {
  const recordDir = archive(t, "{not-json");
  const verdict = await adjudicate({ envelope, recordDir });

  assertWithheld(verdict);
  assert.match(verdict.incomplete[0], /^run manifest is unreadable:/);
});
