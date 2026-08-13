import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
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

const provenPullRequestCause = {
  side: "pull-request",
  determinism: "deterministic",
  locus: "unproven",
};

async function run(args, response = {
  diagnosis: "proved cause",
  commit: "abc123",
  writeUp: "Repaired code.",
  cause: provenPullRequestCause,
}) {
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
    evidencePath: "/run/test-command-output.log",
    excerptPath: "/run/test-command-output.log.excerpt",
  };
  const { result, calls, phases } = await run(input);

  assert.deepEqual(result, {
    diagnosis: "proved cause",
    commit: "abc123",
    writeUp: "Repaired code.",
    cause: provenPullRequestCause,
  });
  assert.deepEqual(phases, ["Root cause"]);
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].options, {
    engine: "codex",
    schema: {
      type: "object",
      additionalProperties: false,
      required: ["diagnosis", "commit", "writeUp", "cause"],
      properties: {
        diagnosis: { type: "string" },
        commit: { type: "string" },
        writeUp: { type: "string" },
        cause: {
          type: "object",
          additionalProperties: false,
          required: ["side", "determinism", "locus"],
          properties: {
            side: { enum: ["pull-request", "target", "unproven"] },
            determinism: { enum: ["deterministic", "intermittent", "unproven"] },
            locus: { enum: ["test-expectation", "product", "unproven"] },
          },
        },
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
  assert.match(
    calls[0].prompt,
    /read \/run\/test-command-output\.log\.excerpt — a failure-relevant excerpt[\s\S]*complete original is at \/run\/test-command-output\.log/,
  );
  assert.match(calls[0].prompt, /reconciled target[\s\S]*fix only what you prove/);
  assert.match(
    calls[0].prompt,
    /target-side cause is out of scope[\s\S]*never revert or override target-side changes[\s\S]*never retarget or remove the pull request's test expectations[\s\S]*empty commit and a diagnosis naming the target-side commit/,
  );
  assert.match(
    calls[0].prompt,
    /`side`[\s\S]*`determinism`[\s\S]*`locus`[\s\S]*Report `unproven` on any axis you have not proved/,
  );
  assert.match(
    calls[0].prompt,
    /`intermittent` only once you have measured a failure rate[\s\S]*A single observed failure is not a measured rate/,
  );
  assert.match(
    calls[0].prompt,
    /One exception, and only this one: a target-side cause proven `intermittent` with locus `test-expectation` is yours to repair[\s\S]*let the fix ride this pull request/,
  );
  assert.match(
    calls[0].prompt,
    /a `product` locus stays out of scope even when intermittent[\s\S]*only witness to a real bug[\s\S]*ships that bug/,
  );
  assert.match(
    calls[0].prompt,
    /repaired target-side flaky expectation carries the same disclosure[\s\S]*measured rate that proved the flake/,
  );
  assert.match(calls[0].prompt, /configured Minos identity[\s\S]*Never push/);
  assert.match(
    calls[0].prompt,
    /Delivery geometry[\s\S]*delivered by pushing to the pull-request branch[\s\S]*reconciliation merge[\s\S]*does not prove it is deliverable[\s\S]*pull-request side of the history[\s\S]*solely on the target side[\s\S]*cannot be delivered through this pull request[\s\S]*empty commit[\s\S]*never that the file does not exist[\s\S]*Do not author an undeliverable fix/,
  );
  assert.match(
    calls[0].prompt,
    /retargeting or removing a test expectation[\s\S]*name the superseding commit[\s\S]*coverage was reduced/,
  );
});

test("finishing evidence needs no invented command and permits an empty commit", async () => {
  const response = {
    diagnosis: "flake did not reproduce",
    commit: "",
    writeUp: "No code changed.",
    cause: { side: "unproven", determinism: "unproven", locus: "unproven" },
  };
  const { result, calls } = await run({
    skillPath: "/opt/minos/root-cause",
    command: "",
    exitStatus: null,
    evidencePath: "/run/check-logs.json",
    excerptPath: "/run/check-logs.json",
  }, response);

  assert.deepEqual(result, response);
  assert.match(calls[0].prompt, /No failing local command is available/);
  assert.match(calls[0].prompt, /read the captured output at \/run\/check-logs\.json/);
  assert.doesNotMatch(calls[0].prompt, /failure-relevant excerpt/);
  assert.match(calls[0].prompt, /empty commit string/);
});

test("a repaired target-side flake and an unrepairable one are distinguishable by verdict", async () => {
  const input = {
    skillPath: "/opt/minos/root-cause",
    command: "cargo run -p xtask -- test",
    exitStatus: 1,
    evidencePath: "/run/test-command-output.log",
    excerptPath: "/run/test-command-output.log.excerpt",
  };

  const ridden = await run(input, {
    diagnosis: "target-side test asserts an ordering the product never guaranteed",
    commit: "d4e5f60",
    writeUp: "Relaxed the ordering assumption.",
    cause: { side: "target", determinism: "intermittent", locus: "test-expectation" },
  });
  assert.equal(ridden.result.commit, "d4e5f60");
  assert.deepEqual(ridden.result.cause, {
    side: "target",
    determinism: "intermittent",
    locus: "test-expectation",
  });

  const heldRace = await run(input, {
    diagnosis: "target-side race in the scheduler, witnessed by the test",
    commit: "",
    writeUp: "No code changed.",
    cause: { side: "target", determinism: "intermittent", locus: "product" },
  });
  assert.equal(heldRace.result.commit, "");
  assert.equal(heldRace.result.cause.locus, "product");
});

test("caller evidence is optional when an exact failing command is supplied", async () => {
  const { calls } = await run({
    skillPath: "/opt/minos/root-cause",
    command: "go test ./...",
    exitStatus: 2,
    evidencePath: null,
    excerptPath: null,
  });
  assert.match(calls[0].prompt, /no captured output[\s\S]*Reproduce the failure/);
});

test("invalid inputs fail closed without dispatch", async () => {
  for (const input of [
    {},
    { skillPath: "relative", command: "test", exitStatus: 1, evidencePath: "/e.log", excerptPath: "/e.log" },
    { skillPath: "/skill", command: "test", exitStatus: "1", evidencePath: "/e.log", excerptPath: "/e.log" },
    { skillPath: "/skill", command: "", exitStatus: null, evidencePath: null, excerptPath: null },
    { skillPath: "/skill", command: "test", exitStatus: 1, evidence: "inline text" },
    { skillPath: "/skill", command: "test", exitStatus: 1, evidencePath: "/e.log", excerptPath: null },
    { skillPath: "/skill", command: "test", exitStatus: 1, evidencePath: "relative.log", excerptPath: "relative.log" },
  ]) {
    const { result, calls, phases } = await run(input);
    assert.equal(result.status, "incomplete");
    assert.match(result.reason, /needs args/);
    assert.deepEqual(calls, []);
    assert.deepEqual(phases, []);
  }
});

test("the input builder passes small evidence whole, by path, and validates its modes", (t) => {
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
    evidencePath,
    excerptPath: evidencePath,
  });
  assert.equal(existsSync(`${evidencePath}.excerpt`), false);

  const finishing = spawnSync(inputScriptPath, [
    "--skill", "/opt/minos/root-cause",
    "--evidence", evidencePath,
  ], { encoding: "utf8" });
  assert.equal(finishing.status, 0, finishing.stderr);
  assert.equal(JSON.parse(finishing.stdout).exitStatus, null);

  const checkLogsPath = join(root, "check-logs.json");
  writeFileSync(checkLogsPath, JSON.stringify({
    head_sha: "b1e6c548",
    runs: [{ id: 7, status: "failure", jobs: [{ id: 9, name: "test", status: "failure", log: "FAIL: TestX" }] }],
  }));
  const checkLogs = spawnSync(inputScriptPath, [
    "--skill", "/opt/minos/root-cause",
    "--evidence", checkLogsPath,
  ], { encoding: "utf8" });
  assert.equal(checkLogs.status, 0, checkLogs.stderr);
  assert.equal(JSON.parse(checkLogs.stdout).excerptPath, checkLogsPath);
  assert.equal(existsSync(`${checkLogsPath}.excerpt`), false);

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

test("large plain-text evidence is excerpted to failure-relevant lines", (t) => {
  const root = mkdtempSync(join(tmpdir(), "minos-rootcause-excerpt-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const evidencePath = join(root, "test-command-output.log");
  const passing = Array.from({ length: 4000 }, (_, index) => `        PASS [   0.01s] relay-app case_${index}`);
  passing[1500] = "        TRY 1 FAIL [   2.31s] relay-app parser::keeps_attribution";
  passing[1501] = "thread 'parser::keeps_attribution' panicked at src/parser.rs:44";
  passing[3990] = "     Summary [  41.20s] 4000 tests run: 3999 passed (1 flaky), 0 skipped";
  writeFileSync(evidencePath, passing.join("\n"));

  const result = spawnSync(inputScriptPath, [
    "--skill", "/opt/minos/root-cause",
    "--evidence", evidencePath,
  ], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  const output = JSON.parse(result.stdout);
  assert.equal(output.evidencePath, evidencePath);
  assert.equal(output.excerptPath, `${evidencePath}.excerpt`);

  const excerpt = readFileSync(output.excerptPath, "utf8");
  assert.match(excerpt, /TRY 1 FAIL[\s\S]*panicked at src\/parser\.rs/);
  assert.match(excerpt, /Summary \[/);
  assert.match(excerpt, /lines elided/);
  assert.ok(excerpt.length < readFileSync(evidencePath, "utf8").length / 4);
});

test("check-logs evidence keeps its structure while each job log is excerpted", (t) => {
  const root = mkdtempSync(join(tmpdir(), "minos-rootcause-checklogs-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const evidencePath = join(root, "check-logs.json");
  const noisy = Array.from({ length: 3000 }, (_, index) => `ok line ${index}`);
  noisy[2000] = "FLAKY 2/3 parser::boundary_only_input";
  writeFileSync(evidencePath, JSON.stringify({
    head_sha: "b1e6c548",
    target_sha: "9fc4aa7b",
    runs: [{ id: 7, index: 84, status: "success", title: "CI", jobs: [
      { id: 9, name: "test", status: "success", log: noisy.join("\n") },
    ] }],
  }));

  const result = spawnSync(inputScriptPath, [
    "--skill", "/opt/minos/root-cause",
    "--evidence", evidencePath,
  ], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  const output = JSON.parse(result.stdout);
  assert.equal(output.excerptPath, `${evidencePath}.excerpt`);

  const excerpt = JSON.parse(readFileSync(output.excerptPath, "utf8"));
  assert.equal(excerpt.head_sha, "b1e6c548");
  assert.equal(excerpt.runs[0].jobs[0].name, "test");
  assert.match(excerpt.runs[0].jobs[0].log, /FLAKY 2\/3 parser::boundary_only_input/);
  assert.match(excerpt.runs[0].jobs[0].log, /lines elided/);
  assert.ok(excerpt.runs[0].jobs[0].log.length < noisy.join("\n").length / 4);
});
