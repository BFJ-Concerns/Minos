import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import {
  DECISION_KIND,
  DIGEST_KIND,
  configuredCommandResult,
  findingKey,
  preparedFinding,
  reusableSetupCommandResults,
  sweepDigest,
  validateSweepDecision,
} from "./completion-policy.mjs";

const policyScriptPath = fileURLToPath(new URL("./completion-policy.mjs", import.meta.url));

test("configured commands expose pass, skip, and terminal failure results", () => {
  assert.deepEqual(configuredCommandResult("go test ./...", 0), {
    status: "passed",
    terminal: false,
    reviewAllowed: true,
  });
  assert.deepEqual(configuredCommandResult("", undefined), {
    status: "skipped",
    terminal: false,
    reviewAllowed: true,
  });
  assert.deepEqual(configuredCommandResult("go test ./...", 1), {
    status: "failed",
    exitStatus: 1,
    terminal: true,
    reviewAllowed: false,
    forgeStatus: "incomplete",
  });
});

test("all absent command forms skip regardless of exit status", () => {
  for (const command of [undefined, null, "", "   "])
    for (const exitStatus of [0, 1])
      assert.deepEqual(configuredCommandResult(command, exitStatus), {
        status: "skipped",
        terminal: false,
        reviewAllowed: true,
      });
});

function completeSetupResult(overrides = {}) {
  return {
    status: "complete",
    environment: { ready: true, actions: [], cause: null },
    commandExecutions: {
      head: "head-719",
      build: { command: "make build", exitStatus: 0 },
      test: { command: "make test", exitStatus: 0 },
    },
    ...overrides,
  };
}

test("setup command results are reusable only as a complete exact-head pair", () => {
  assert.deepEqual(
    reusableSetupCommandResults(completeSetupResult(), "head-719", "make build", "make test"),
    {
      reusable: true,
      results: {
        build: { status: "passed", terminal: false, reviewAllowed: true },
        test: { status: "passed", terminal: false, reviewAllowed: true },
      },
    },
  );

  for (const [name, result] of [
    ["absent", null],
    ["malformed shape", { status: "complete", environment: { ready: true } }],
    ["partial", completeSetupResult({ commandExecutions: {
      head: "head-719", build: { command: "make build", exitStatus: 0 },
    } })],
    ["non-pass", completeSetupResult({ commandExecutions: {
      ...completeSetupResult().commandExecutions,
      test: { command: "make test", exitStatus: 1 },
    } })],
    ["stale head", completeSetupResult({ commandExecutions: {
      ...completeSetupResult().commandExecutions, head: "old-head",
    } })],
    ["changed command", completeSetupResult({ commandExecutions: {
      ...completeSetupResult().commandExecutions,
      build: { command: "make something-else", exitStatus: 0 },
    } })],
  ]) {
    assert.deepEqual(
      reusableSetupCommandResults(result, "head-719", "make build", "make test"),
      { reusable: false },
      name,
    );
  }
});

test("setup result CLI refuses malformed JSON and accepts genuine current-head passes", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-setup-result-policy-"));
  const record = join(root, "setup-result.json");
  writeFileSync(record, JSON.stringify(completeSetupResult()));
  const args = ["--setup-result", record, "head-719", "make build", "make test"];
  assert.equal(JSON.parse(execFileSync(process.execPath, [policyScriptPath, ...args])).reusable, true);

  writeFileSync(record, "not JSON");
  assert.deepEqual(JSON.parse(execFileSync(process.execPath, [policyScriptPath, ...args])), {
    reusable: false,
  });
});

test("the configured command policy CLI runs from an install-like layout", () => {
  const root = mkdtempSync(join(tmpdir(), "minos-completion-policy-"));
  const installedWorkflows = join(root, "installed", "workflows");
  mkdirSync(installedWorkflows, { recursive: true });
  const installedScript = join(installedWorkflows, "completion-policy.mjs");
  copyFileSync(policyScriptPath, installedScript);

  const output = execFileSync(process.execPath, [installedScript, "go test ./...", "1"], {
    cwd: root,
    encoding: "utf8",
  });
  assert.equal(output.endsWith("\n"), true);
  assert.equal(output.trim().split("\n").length, 1);
  assert.deepEqual(JSON.parse(output), {
    status: "failed",
    exitStatus: 1,
    terminal: true,
    reviewAllowed: false,
    forgeStatus: "incomplete",
  });

  assert.deepEqual(JSON.parse(execFileSync(process.execPath, [installedScript, "", "0"], {
    cwd: root,
    encoding: "utf8",
  })), {
    status: "skipped",
    terminal: false,
    reviewAllowed: true,
  });
});

test("the configured command policy CLI rejects malformed input with usage exit 2", () => {
  for (const args of [[], ["go test ./..."], ["go test ./...", "not-an-exit"], ["cmd", "0", "extra"]]) {
    const result = spawnSync(process.execPath, [policyScriptPath, ...args], { encoding: "utf8" });
    assert.equal(result.status, 2);
    assert.equal(result.stdout, "");
    assert.match(result.stderr, /^usage: node workflows\/completion-policy\.mjs COMMAND EXIT_STATUS\n$/);
    assert.doesNotMatch(result.stderr, /\n\s+at /);
  }
});

function finding(title, severity, path, line) {
  return { id: `specialist:${title}`, title, severity, confidence: 90, path, line, explanation: `${title} breaks the contract` };
}

function digestInput(findings, overrides = {}) {
  return {
    review: { status: "complete", confirmedFindings: findings },
    threshold: "High",
    maximumRounds: null,
    runRecord: { round: 0, confirmedUnfixed: [] },
    ...overrides,
  };
}

function decision(classification, basis = "stated grounds for the call") {
  return { kind: DECISION_KIND, classification, basis };
}

test("finding identity survives cosmetic retitles", () => {
  assert.equal(
    findingKey({ path: "a.go", line: 4, title: "  Unsafe   Transition " }),
    findingKey({ path: "a.go", line: 4, title: "unsafe transition" }),
  );
  assert.notEqual(
    findingKey({ path: "a.go", line: 4, title: "unsafe transition" }),
    findingKey({ path: "a.go", line: 5, title: "unsafe transition" }),
  );
});

test("the digest suppresses drifted rediscoveries without collapsing distinct same-path defects", () => {
  const original = finding("unsafe transition", "High", "a.go", 40);
  original.explanation = "The transition accepts an invalid state after validation.";
  const recorded = preparedFinding(original);
  const runRecord = {
    round: 1,
    confirmedUnfixed: [{ key: recorded.key, finding: recorded, attempts: 2 }],
  };

  const shifted = { ...original, line: 47 };
  const retitled = { ...original, title: "Validation permits an impossible transition" };
  for (const rediscovered of [shifted, retitled]) {
    const digest = sweepDigest(digestInput([rediscovered], { runRecord }));
    assert.deepEqual(digest.candidates, []);
  }

  const distinct = finding("unsafe cleanup", "High", "a.go", 47);
  distinct.explanation = "Cleanup releases the active lease before the write completes.";
  const digest = sweepDigest(digestInput([retitled, distinct], { runRecord }));
  assert.deepEqual(digest.candidates.map((entry) => entry.title), [distinct.title]);
});

test("legacy and identity-absent records retain exact-key matching", () => {
  const original = finding("unsafe transition", "High", "a.go", 40);
  const runRecord = {
    round: 1,
    confirmedUnfixed: [{ key: findingKey(original), finding: original, attempts: 2 }],
  };
  assert.deepEqual(sweepDigest(digestInput([original], { runRecord })).candidates, []);
  assert.equal(sweepDigest(digestInput([{ ...original, line: 41 }], { runRecord })).candidates.length, 1);
});

test("blank explanations retain exact-key matching instead of collapsing same-path defects", () => {
  const absent = finding("unsafe deletion", "High", "a.go", 32);
  delete absent.explanation;
  assert.equal(Object.hasOwn(preparedFinding(absent), "identity"), false);

  const original = finding("unsafe transition", "High", "a.go", 40);
  original.explanation = "";
  const recorded = preparedFinding(original);
  assert.equal(Object.hasOwn(recorded, "identity"), false);

  const distinct = finding("unsafe cleanup", "High", "a.go", 47);
  distinct.explanation = "   \n\t";
  assert.equal(Object.hasOwn(preparedFinding(distinct), "identity"), false);

  const runRecord = {
    round: 1,
    confirmedUnfixed: [{ key: recorded.key, finding: recorded, attempts: 2 }],
  };
  const digest = sweepDigest(digestInput([original, distinct], { runRecord }));
  assert.deepEqual(digest.candidates.map((entry) => entry.title), [distinct.title]);
});

test("the digest separates dispatchable findings by threshold and suppresses confirmed-unfixed", () => {
  const high = finding("unsafe transition", "High", "a.go", 4);
  const low = finding("weak wording", "Low", "b.go", 7);
  const priorKey = findingKey(high);
  const fresh = sweepDigest(digestInput([high, low]));
  assert.equal(fresh.status, "complete");
  assert.equal(fresh.round, 1);
  assert.deepEqual(fresh.aboveThresholdKeys, [priorKey]);
  assert.deepEqual(fresh.belowThresholdKeys, [findingKey(low)]);
  assert.equal(fresh.thresholdIndication, "working");

  const suppressed = sweepDigest(digestInput([high, low], {
    runRecord: {
      round: 2,
      confirmedUnfixed: [{ key: priorKey, finding: high, attempts: 2, reason: "failed twice" }],
    },
  }));
  assert.equal(suppressed.round, 3);
  assert.deepEqual(suppressed.aboveThresholdKeys, []);
  assert.deepEqual(suppressed.candidates.map((entry) => entry.key), [findingKey(low)]);
  assert.deepEqual(suppressed.priorConfirmedUnfixed, [{ key: priorKey, attempts: 2 }]);
  assert.equal(suppressed.thresholdIndication, "terminal");
});

test("the digest reports a reached maximum-rounds ceiling as terminal indication", () => {
  const digest = sweepDigest(digestInput([finding("unsafe transition", "High", "a.go", 4)], {
    maximumRounds: 1,
    runRecord: { round: 1, confirmedUnfixed: [] },
  }));
  assert.equal(digest.round, 2);
  assert.equal(digest.maximumRoundsReached, true);
  assert.equal(digest.thresholdIndication, "terminal");
});

test("the digest fails closed on malformed input", () => {
  for (const [input, reason] of [
    [null, /needs an input object/],
    [{ review: {} }, /confirmedFindings/],
    [digestInput([], { review: { status: "incomplete", confirmedFindings: [] } }), /complete review/],
    [digestInput([], { threshold: "P1" }), /unknown review threshold P1/],
    [digestInput([], { maximumRounds: 0 }), /positive integer/],
  ]) {
    const digest = sweepDigest(input);
    assert.equal(digest.kind, DIGEST_KIND);
    assert.equal(digest.status, "invalid");
    assert.match(digest.reason, reason);
  }
});

test("a coherent working or terminal decision validates against its digest", () => {
  const digest = sweepDigest(digestInput([finding("unsafe transition", "High", "a.go", 4)]));
  assert.deepEqual(validateSweepDecision(decision("working"), digest), { ok: true });
  assert.deepEqual(validateSweepDecision(decision("terminal"), digest), { ok: true });
});

test("judgement may stop the loop with above-threshold findings still on the table", () => {
  const digest = sweepDigest(digestInput([
    finding("unsafe transition", "Critical", "a.go", 4),
    finding("second defect", "High", "b.go", 9),
  ]));
  assert.equal(digest.thresholdIndication, "working");
  assert.deepEqual(validateSweepDecision(decision("terminal", "the findings restate confirmed-unfixed ground"), digest), { ok: true });
});

test("decision validation fails closed on incoherent decisions", () => {
  const digest = sweepDigest(digestInput([finding("unsafe transition", "High", "a.go", 4)]));
  for (const [candidate, reason] of [
    [null, /must be an object/],
    [{ classification: "working", basis: "b" }, new RegExp(`kind ${DECISION_KIND}`)],
    [decision("converged"), /must be working or terminal/],
    [{ kind: DECISION_KIND, classification: "working", basis: "  " }, /non-empty basis/],
  ]) {
    const verdict = validateSweepDecision(candidate, digest);
    assert.equal(verdict.ok, false);
    assert.match(verdict.reason, reason);
  }
});

test("a working decision needs a dispatchable finding and respects the rounds ceiling", () => {
  const key = findingKey(finding("unsafe transition", "High", "a.go", 4));
  const exhausted = sweepDigest(digestInput([finding("unsafe transition", "High", "a.go", 4)], {
    runRecord: {
      round: 1,
      confirmedUnfixed: [{ key, attempts: 2, reason: "failed twice" }],
    },
  }));
  const noCandidates = validateSweepDecision(decision("working"), exhausted);
  assert.equal(noCandidates.ok, false);
  assert.match(noCandidates.reason, /at least one dispatchable finding/);

  const capped = sweepDigest(digestInput([finding("unsafe transition", "High", "a.go", 4)], {
    maximumRounds: 1,
    runRecord: { round: 1, confirmedUnfixed: [] },
  }));
  const overruled = validateSweepDecision(decision("working"), capped);
  assert.equal(overruled.ok, false);
  assert.match(overruled.reason, /maximum rounds has been reached/);
});

test("validation refuses to judge a decision without a complete digest", () => {
  const verdict = validateSweepDecision(decision("working"), sweepDigest(null));
  assert.equal(verdict.ok, false);
  assert.match(verdict.reason, /complete sweep digest/);
});
