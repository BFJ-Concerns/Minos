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

test("observations and misconfigurations render as unverified non-findings in one pull-request payload", () => {
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
  assert.equal(payload.destination, "pull-request", "observations must not be routed to a reviewed project's annexe");
  assert.equal(payload.body, "Unverified observations from the review, for the author's judgement.");
  assert.equal(payload.comments.length, 2);

  const observation = payload.comments[0];
  assert.equal(observation.path, "pkg/server.go");
  assert.equal(observation.line, 12);
  assert.equal(
    observation.body,
    "**Out-of-scope observation (unverified): Unrelated nil deref**\n\n" +
      "A nil map write outside the reviewed range.\n\n" +
      "This was noticed outside the review's scope and has not been verified.",
  );

  const misconfiguration = payload.comments[1];
  assert.equal(misconfiguration.path, ".review/security.md");
  assert.equal(misconfiguration.line, 1);
  assert.equal(
    misconfiguration.body,
    "**Review brief misconfiguration: Security brief**\n\n" +
      "declared scope directory does not exist\n\n" +
      "This was noticed outside the review's scope and has not been verified.",
  );

  // The exact bodies above pin both channel labels and their common
  // unverified marking. Name the leaks they prevent: neither channel may
  // acquire confirmed-finding language or an annexe destination.
  for (const [channel, comment] of [["observation", observation], ["misconfiguration", misconfiguration]]) {
    assert.ok(!/finding/i.test(comment.body), `${channel} rendered as a finding`);
    assert.ok(!/annexe/i.test(comment.body), `${channel} named an annexe delivery path`);
  }
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
