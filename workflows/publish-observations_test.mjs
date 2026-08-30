import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const cliPath = fileURLToPath(new URL("./publish-observations.mjs", import.meta.url));

function runCli(entries) {
  const scratch = mkdtempSync(join(tmpdir(), "publish-observations-"));
  const entriesPath = join(scratch, "entries.json");
  writeFileSync(entriesPath, JSON.stringify(entries));
  return JSON.parse(execFileSync(process.execPath, [cliPath, entriesPath], { encoding: "utf8" }));
}

test("observations and misconfigurations render as one pull-request payload", () => {
  const payload = runCli([
    {
      kind: "out-of-scope-observation",
      title: "Unrelated nil deref",
      path: "pkg/server.go",
      line: 12,
      explanation: "A nil map write outside the reviewed range.",
    },
    {
      kind: "review-brief-misconfiguration",
      title: "Security brief",
      brief: ".review/security.md",
      reason: "declared scope directory does not exist",
    },
  ]);
  assert.equal(payload.destination, "pull-request");
  assert.equal(payload.comments.length, 2);

  const observation = payload.comments[0];
  assert.equal(observation.path, "pkg/server.go");
  assert.equal(observation.line, 12);
  // The unverified marking is the payload's whole point: material here has
  // no verifier verdict and must never read as a finding.
  assert.match(observation.body, /Out-of-scope observation \(unverified\)/);
  assert.match(observation.body, /has not been verified/);

  const misconfiguration = payload.comments[1];
  assert.equal(misconfiguration.path, ".review/security.md");
  assert.equal(misconfiguration.line, 1);
  assert.match(misconfiguration.body, /Review brief misconfiguration/);
  assert.match(misconfiguration.body, /declared scope directory does not exist/);

  assert.match(payload.body, /Unverified observations/);
});

test("a confirmed-finding kind is refused: findings ride the review, never this channel", () => {
  const scratch = mkdtempSync(join(tmpdir(), "publish-observations-"));
  const entriesPath = join(scratch, "entries.json");
  writeFileSync(entriesPath, JSON.stringify([
    { kind: "review-finding", title: "x", path: "a", line: 1, explanation: "y" },
  ]));
  try {
    execFileSync(process.execPath, [cliPath, entriesPath], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
    assert.fail("expected a refusal");
  } catch (error) {
    assert.notEqual(error.status, 0);
    assert.match(String(error.stderr), /unknown entry kind/);
  }
});

test("argument errors exit 2 with usage", () => {
  try {
    execFileSync(process.execPath, [cliPath], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
    assert.fail("expected a usage exit");
  } catch (error) {
    assert.equal(error.status, 2);
    assert.match(String(error.stderr), /usage:/);
  }
});
