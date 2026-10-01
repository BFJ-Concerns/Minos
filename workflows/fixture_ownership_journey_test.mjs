import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";
import { operationRoot } from "./temporary-directory-fixture.mjs";
import { invokeWorkflow } from "./workflow-invocation-fixture.mjs";

for (const outcome of ["success", "failure"]) {
  test(`the Node test runner removes an owned fixture after ${outcome}`, (t) => {
    const root = operationRoot(t, "minos-fixture-ownership-");
    const marker = join(root, "allocated.txt");
    const childTest = join(root, "ownership_test.mjs");
    writeFileSync(childTest, `
import test from "node:test";
import { writeFileSync } from "node:fs";
import { operationRoot } from ${JSON.stringify(new URL("./temporary-directory-fixture.mjs", import.meta.url).href)};
test("owned fixture", (t) => {
  const directory = operationRoot(t, "minos-owned-child-");
  writeFileSync(${JSON.stringify(marker)}, directory);
  ${outcome === "failure" ? 'throw new Error("deliberate fixture failure");' : ""}
});
`);
    const childEnvironment = { ...process.env };
    // A nested runner must not inherit the parent's worker identity.
    delete childEnvironment.NODE_TEST_CONTEXT;
    const result = spawnSync(process.execPath, ["--test", childTest], {
      encoding: "utf8",
      env: childEnvironment,
    });
    assert.equal(result.status, outcome === "failure" ? 1 : 0, result.stdout + result.stderr);
    if (outcome === "failure") assert.match(result.stdout + result.stderr, /deliberate fixture failure/);
    const directory = readFileSync(marker, "utf8");
    // Keep a regressed teardown from leaking the child's allocation.
    t.after(() => rmSync(directory, { recursive: true, force: true }));
    assert.equal(existsSync(directory), false, "the runner executes teardown even when the fixture body throws");
  });
}

test("workflow fixture responder errors reach the calling test", async () => {
  const failure = new Error("broken fixture responder");
  const script = (agent, parallel) => parallel([() => agent("prompt", { label: "specialist" })]);
  await assert.rejects(invokeWorkflow(script, {}, () => { throw failure; }), (error) => error === failure);
});

test("workflow fixture rejects an unexpected pipeline call", async () => {
  const script = (_agent, _parallel, pipeline) => pipeline([]);
  await assert.rejects(invokeWorkflow(script, {}, () => null), /workflow unexpectedly called pipeline/);
});
