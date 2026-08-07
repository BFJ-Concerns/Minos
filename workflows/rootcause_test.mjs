import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const scriptPath = fileURLToPath(new URL("./rootcause.js", import.meta.url));
const inputScriptPath = fileURLToPath(new URL("./rootcause-inputs.mjs", import.meta.url));
const source = readFileSync(scriptPath, "utf8");
const body = source.replace(/^export const meta =/m, "const meta =");
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const script = new AsyncFunction("agent", "phase", "args", body);

async function run(args, response = { diagnosis: "proved cause", commit: "abc123", writeUp: "Repaired code." }) {
  const calls = [];
  const phases = [];
  const result = await script(
    async (prompt, options) => {
      calls.push({ prompt, options });
      return response;
    },
    (name) => phases.push(name),
    args,
  );
  return { result, calls, phases };
}

test("valid command evidence dispatches one pinned isolated repair agent", async () => {
  const input = {
    skillPath: "/opt/minos/root-cause",
    command: "just verify",
    exitStatus: 1,
    evidence: "FAIL integration test",
  };
  const { result, calls, phases } = await run(input);

  assert.deepEqual(result, { diagnosis: "proved cause", commit: "abc123", writeUp: "Repaired code." });
  assert.deepEqual(phases, ["Root cause"]);
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].options, {
    engine: "codex",
    schema: {
      type: "object",
      additionalProperties: false,
      required: ["diagnosis", "commit", "writeUp"],
      properties: {
        diagnosis: { type: "string" },
        commit: { type: "string" },
        writeUp: { type: "string" },
      },
    },
    model: "gpt-5.6-sol",
    effort: "high",
    isolation: "worktree",
    label: "root-cause-repair",
    phase: "Root cause",
  });
  assert.match(calls[0].prompt, /\/opt\/minos\/root-cause\/SKILL\.md/);
  assert.match(calls[0].prompt, /Failing command: "just verify"[\s\S]*Exit status: 1/);
  assert.match(calls[0].prompt, /FAIL integration test/);
  assert.match(calls[0].prompt, /reconciled target[\s\S]*fix only what you prove/);
  assert.match(calls[0].prompt, /configured Minos identity[\s\S]*Never push/);
  assert.match(
    calls[0].prompt,
    /Delivery geometry[\s\S]*delivered by pushing to the pull-request branch[\s\S]*files it must change exist on the branch[\s\S]*solely on the target side[\s\S]*cannot be delivered through this pull request[\s\S]*empty commit[\s\S]*never that the file does not exist[\s\S]*Do not author an undeliverable fix/,
  );
  assert.match(
    calls[0].prompt,
    /retargeting or removing a test expectation[\s\S]*name the superseding commit[\s\S]*coverage was reduced/,
  );
});

test("finishing evidence needs no invented command and permits an empty commit", async () => {
  const response = { diagnosis: "flake did not reproduce", commit: "", writeUp: "No code changed." };
  const { result, calls } = await run({
    skillPath: "/opt/minos/root-cause",
    command: "",
    exitStatus: null,
    evidence: '{"runs":[]}',
  }, response);

  assert.deepEqual(result, response);
  assert.match(calls[0].prompt, /No failing local command is available/);
  assert.match(calls[0].prompt, /\{"runs":\[\]\}/);
  assert.match(calls[0].prompt, /empty commit string/);
});

test("caller evidence is optional when an exact failing command is supplied", async () => {
  const { calls } = await run({
    skillPath: "/opt/minos/root-cause",
    command: "go test ./...",
    exitStatus: 2,
    evidence: null,
  });
  assert.match(calls[0].prompt, /no captured output[\s\S]*Reproduce the failure/);
});

test("invalid inputs fail closed without dispatch", async () => {
  for (const input of [
    {},
    { skillPath: "relative", command: "test", exitStatus: 1, evidence: "failed" },
    { skillPath: "/skill", command: "test", exitStatus: "1", evidence: "failed" },
    { skillPath: "/skill", command: "", exitStatus: null, evidence: null },
  ]) {
    const { result, calls, phases } = await run(input);
    assert.equal(result.status, "incomplete");
    assert.match(result.reason, /needs args/);
    assert.deepEqual(calls, []);
    assert.deepEqual(phases, []);
  }
});

test("the input builder carries file evidence and validates its modes", (t) => {
  const root = mkdtempSync(join(tmpdir(), "minos-rootcause-inputs-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const evidencePath = join(root, "evidence.log");
  writeFileSync(evidencePath, "captured failure\n");

  const command = spawnSync(inputScriptPath, [
    "--skill", "/opt/minos/root-cause/",
    "--command", "just verify",
    "--exit-status", "1",
    "--evidence", evidencePath,
  ], { encoding: "utf8" });
  assert.equal(command.status, 0, command.stderr);
  assert.deepEqual(JSON.parse(command.stdout), {
    skillPath: "/opt/minos/root-cause",
    command: "just verify",
    exitStatus: 1,
    evidence: "captured failure\n",
  });

  const finishing = spawnSync(inputScriptPath, [
    "--skill", "/opt/minos/root-cause",
    "--evidence", evidencePath,
  ], { encoding: "utf8" });
  assert.equal(finishing.status, 0, finishing.stderr);
  assert.equal(JSON.parse(finishing.stdout).exitStatus, null);

  for (const invalidArgs of [
    [],
    ["--skill", "relative", "--evidence", evidencePath],
    ["--skill", "/skill", "--command", "test", "--exit-status", "nope"],
    ["--skill", "/skill"],
    ["--skill", "/skill", "--evidence", join(root, "missing.log")],
  ]) {
    const invalid = spawnSync(inputScriptPath, invalidArgs, { encoding: "utf8" });
    assert.equal(invalid.status, 2);
    assert.match(invalid.stderr, /^usage:/);
  }
});
