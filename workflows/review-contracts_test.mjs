import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { reviewContracts } from "./review-contracts.mjs";
import { findingsFromVerifierPrompt } from "./verifier-prompt-fixture.mjs";
import { invokeWorkflow } from "./workflow-invocation-fixture.mjs";

const workflowsDir = dirname(fileURLToPath(import.meta.url));
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;

function workflow(filename) {
  const body = readFileSync(join(workflowsDir, filename), "utf8")
    .replace(/^export const meta =/m, "const meta =");
  return new AsyncFunction("agent", "parallel", "pipeline", "phase", "log", "args", body);
}

const workflows = {
  review: workflow("review.js"),
  briefs: workflow("review-briefs.js"),
};

function git(root, ...rest) {
  return execFileSync("git", ["-C", root, ...rest], { encoding: "utf8" }).trim();
}

// Both builders run as the lifecycle runs them, over one small repository
// whose head adds a brief and changes one file, so each workflow receives
// exactly the input a deployment would hand it.
function builtInputs() {
  const root = mkdtempSync(join(tmpdir(), "minos-review-contracts-"));
  try {
    execFileSync("git", ["init", "-q", root]);
    git(root, "config", "user.name", "Fixture");
    git(root, "config", "user.email", "fixture@example.test");
    writeFileSync(join(root, "AGENTS.md"), "CONTRACTS_GUIDANCE_OCHRE");
    writeFileSync(join(root, "code.go"), "before\n");
    git(root, "add", ".");
    git(root, "commit", "-qm", "base");
    const target = git(root, "rev-parse", "HEAD");
    mkdirSync(join(root, ".review"));
    writeFileSync(join(root, ".review", "errors.md"), "---\nrelevance: Error paths.\n---\nJudge errors.");
    writeFileSync(join(root, "code.go"), "after\n");
    git(root, "add", ".");
    git(root, "commit", "-qm", "head");
    const head = git(root, "rev-parse", "HEAD");
    const orientationPath = join(root, "orientation.json");
    writeFileSync(orientationPath, JSON.stringify({
      repository: root,
      guidance: [{ source: { path: "AGENTS.md" }, location: join(root, "AGENTS.md"), origin: "checked-in" }],
      misconfigurations: [],
    }));
    const env = {
      ...process.env,
      MINOS_ORIENTATION: orientationPath,
      MINOS_WORKSPACE: root,
      MINOS_PROVISIONED_ENGINES: "claude codex",
      MINOS_ROUTING: "",
    };
    const build = (script) => JSON.parse(execFileSync(
      process.execPath,
      [join(workflowsDir, script), target, head],
      { encoding: "utf8", env, stdio: ["ignore", "pipe", "pipe"] },
    ));
    return { review: build("review-inputs.mjs"), briefs: build("review-brief-inputs.mjs") };
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

const INPUTS = builtInputs();

const twoFindings = {
  applicability: { status: "applicable", reason: "the concern applies" },
  findings: [
    { title: "first defect", severity: "High", confidence: 80, path: "code.go", line: 1, explanation: "first" },
    { title: "second defect", severity: "High", confidence: 80, path: "code.go", line: 1, explanation: "second" },
  ],
  outOfScopeObservations: [],
};

function respond(label, prompt) {
  if (label === "exploration") {
    return {
      files: [{ path: "code.go", added: 1, deleted: 1 }],
      plan: [{ id: "logic", concern: "logic", scope: ["code.go"], specialistType: "correctness" }],
      applicability: { reason: "the change engages the plan" },
    };
  }
  if (label === "brief-relevance") {
    const concerns = JSON.parse(prompt.match(/Concerns: (\[[^\n]+\])/)[1]);
    return { decisions: concerns.map((entry) => ({ brief: entry.brief, applicable: true, reason: "relevant" })) };
  }
  if (label.startsWith("brief-partition-")) {
    const files = JSON.parse(prompt.match(/Assigned file inventory: (\[[^\n]+\])/)[1]);
    return { units: [{ id: "complete-scope", concern: "complete assigned scope", files }] };
  }
  if (label.startsWith("verify-")) {
    return {
      verdicts: findingsFromVerifierPrompt(prompt).map((entry) => ({
        findingId: entry.id, verdict: "upheld", confidence: 90, reason: "confirmed",
      })),
    };
  }
  return twoFindings;
}

function specialistCalls(calls) {
  return calls.filter(({ opts }) =>
    opts.label !== "exploration" && opts.label !== "brief-relevance" &&
    !opts.label.startsWith("brief-partition-") && !opts.label.startsWith("verify-"));
}

function verifierCalls(calls) {
  return calls.filter(({ opts }) => opts.label.startsWith("verify-"));
}

test("both builders hand the workflows the one contracts module's exports", () => {
  assert.deepEqual(INPUTS.review.contracts, reviewContracts());
  assert.deepEqual(INPUTS.briefs.contracts, reviewContracts());
});

for (const [name, run] of Object.entries(workflows)) {
  test(`${name} workflow throws before any dispatch when the contracts are absent`, async () => {
    const { contracts, ...withoutContracts } = INPUTS[name];
    const calls = [];
    await assert.rejects(
      invokeWorkflow(run, withoutContracts, (label, prompt, opts) => {
        calls.push(label);
        return respond(label, prompt, opts);
      }),
      /review contracts/,
    );
    assert.deepEqual(calls, []);
  });

  for (const [breakage, mutate] of [
    ["a verifier schema without properties", (contracts) => { delete contracts.verifierSchema.properties; }],
    ["a non-object specialist schema", (contracts) => { contracts.specialistSchema = "specialist"; }],
    ["a zero verifier batch size", (contracts) => { contracts.verifierBatchSize = 0; }],
  ]) {
    test(`${name} workflow throws before any dispatch on ${breakage}`, async () => {
      const contracts = reviewContracts();
      mutate(contracts);
      const calls = [];
      await assert.rejects(
        invokeWorkflow(run, { ...INPUTS[name], contracts }, (label, prompt, opts) => {
          calls.push(label);
          return respond(label, prompt, opts);
        }),
        /review contracts/,
      );
      assert.deepEqual(calls, []);
    });
  }
}

test("both workflows dispatch with the contracts they are handed, keeping no copy", async () => {
  const contracts = reviewContracts();
  contracts.specialistSchema.properties.findings.items.properties.title = { type: "string", minLength: 7 };
  contracts.verifierSchema.properties.verdicts.items.properties.reason = { type: "string", minLength: 3 };
  contracts.verifierBatchSize = 1;

  for (const [name, run] of Object.entries(workflows)) {
    const { calls } = await invokeWorkflow(run, { ...INPUTS[name], contracts }, respond);
    const specialists = specialistCalls(calls);
    const verifiers = verifierCalls(calls);
    assert.ok(specialists.length > 0, `${name} dispatched a specialist`);
    for (const { opts } of specialists)
      assert.deepEqual(opts.schema, contracts.specialistSchema, `${name} specialist schema`);
    assert.equal(verifiers.length, twoFindings.findings.length * specialists.length, `${name} batches one finding per verifier`);
    for (const { opts } of verifiers)
      assert.deepEqual(opts.schema, contracts.verifierSchema, `${name} verifier schema`);
  }
});
