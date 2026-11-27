import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmodSync, copyFileSync, existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { pathToFileURL, fileURLToPath } from "node:url";
import test from "node:test";
import { operationRoot } from "./temporary-directory-fixture.mjs";
import { shippedContracts } from "./shipped-prompts-fixture.mjs";

const workflowsDir = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = dirname(workflowsDir);
const installerPath = join(repositoryRoot, "scripts", "install-review-runtime");
const pinnedRuntimePath = join(repositoryRoot, "runtime", "ensemble.mjs");

const installerBundle = "DISTINCT-INSTALLER-BUNDLE-0728\n";

const optionBlockPattern =
  /\{\s*\n\s*engine:\s*[^,\n]+,\s*\n\s*schema:\s*[^,\n]+,\s*\n\s*model:\s*[^,\n]+,\s*\n\s*effort:\s*[^,\n]+,\s*\n(?:\s*strip:\s*\[[^\n]+\],\s*\n)?(?:\s*identity:\s*true,\s*\n)?\s*label:\s*[^,\n]+,\s*\n\s*phase:\s*[^,\n]+,\s*\n\s*\}/g;

const expectedCallSites = new Map([
  ["judgement.js", 1],
  ["review.js", 1],
  ["quick-review.js", 0],
  ["review-briefs.js", 2],
  ["review-stages.mjs", 3],
]);

function optionKeySetsFromShippedWorkflows() {
  const callSites = [];
  for (const [filename, expectedCount] of expectedCallSites) {
    const source = readFileSync(join(workflowsDir, filename), "utf8");
    const matches = [...source.matchAll(optionBlockPattern)];
    assert.equal(
      matches.length,
      expectedCount,
      `${filename} agent option call sites changed shape; inspect every real call site`,
    );
    for (const match of matches) {
      const keys = [...match[0].matchAll(/^\s*([A-Za-z][A-Za-z0-9]*):/gm)]
        .map((entry) => entry[1]);
      callSites.push({ filename, keys });
    }
  }
  return callSites;
}

function valuesFor(keys) {
  const values = {
    engine: "codex",
    schema: {
      type: "object",
      properties: {},
      additionalProperties: false,
    },
    model: "gpt-6.1-sol",
    effort: "high",
    strip: ["skills", "agents"],
    identity: true,
    label: "minos-runtime-compatibility",
    phase: "Compatibility",
  };
  return Object.fromEntries(keys.map((key) => [key, values[key]]));
}

// Installing in place is not a deploy: the preflight's declared tool set
// describes the box, not whichever machine runs this gate, so in-place
// installs point it at an empty fixture set.
function installInPlace(root) {
  const fixtureTools = join(root, "expected-tool-versions");
  writeFileSync(fixtureTools, "# no declared tools for the gate's install\n");
  return spawnSync(installerPath, [root], {
    cwd: repositoryRoot,
    encoding: "utf8",
    env: { ...process.env, MINOS_EXPECTED_TOOLS: fixtureTools },
  });
}

async function installedOptionValidator(t) {
  const root = operationRoot(t, "minos-installed-runtime-");
  const installation = installInPlace(root);
  assert.equal(installation.status, 0, installation.stderr);
  assert.match(installation.stdout, /ensemble\.mjs: OK/);

  const installedRuntime = join(root, "runtime", "ensemble.mjs");
  const probeRuntime = join(root, "runtime-option-probe.mjs");
  writeFileSync(
    probeRuntime,
    `${readFileSync(installedRuntime, "utf8")}
export { assertValidAgentOptions as minosAssertValidAgentOptions };
`,
  );
  const module = await import(`${pathToFileURL(probeRuntime).href}?probe=${Date.now()}`);
  return module.minosAssertValidAgentOptions;
}

async function installedSchemaValidator(t) {
  const root = operationRoot(t, "minos-installed-runtime-schemas-");
  const installation = installInPlace(root);
  assert.equal(installation.status, 0, installation.stderr);
  const probeRuntime = join(root, "runtime-schema-probe.mjs");
  writeFileSync(
    probeRuntime,
    `${readFileSync(join(root, "runtime", "ensemble.mjs"), "utf8")}
export {
  assertCompilableSchema as minosAssertCompilableSchema,
  normaliseForCodexOutputSchema as minosNormaliseForCodexOutputSchema,
  validateJsonSchema as minosValidateJsonSchema,
};
`,
  );
  const module = await import(`${pathToFileURL(probeRuntime).href}?probe=${Date.now()}`);
  return {
    root,
    compile: module.minosAssertCompilableSchema,
    forCodex: module.minosNormaliseForCodexOutputSchema,
    validate: (value, schema) => module.minosValidateJsonSchema(value, schema).ok,
  };
}

const sampleFinding = {
  title: "t", severity: "High", confidence: 80, path: "a.go", line: 1, explanation: "e", dependsOn: "",
};
const observationSample = { title: "t", path: "a.go", line: 1, explanation: "e" };

// One answer each shipped stage schema must accept, as a leg would return it.
const schemaSamples = {
  explorationSchema: {
    applicability: { reason: "r" },
    files: [{ path: "a.go", added: 1, deleted: 0 }],
    plan: [{ id: "u", concern: "c", scope: ["a.go"], specialistType: "logic" }],
  },
  specialistSchema: {
    applicability: { status: "applicable", reason: "r" },
    findings: [sampleFinding],
    outsideConcernFindings: [],
    outOfScopeObservations: [observationSample],
  },
  generalSchema: { findings: [sampleFinding], outOfScopeObservations: [observationSample] },
  generalWholeChangeSchema: {
    shares: [
      { id: "parser", focus: "The parser's new record type.", paths: ["a.go"] },
      { id: "store", focus: "The store that keeps it.", paths: ["b.go", "c.go"] },
    ],
    findings: [],
    outOfScopeObservations: [],
  },
  mergeSchema: { groups: [{ findingIds: ["a", "b"], primary: "a", sharedCause: "one fix" }] },
  verifierSchema: {
    verdicts: [{
      findingId: "a", verdict: "unsettled", confidence: 50, severity: "Medium",
      reason: "r", outsideFact: "f", wouldSettle: "w",
    }],
    newFindings: [{ ...sampleFinding, foundWhileChecking: "a" }],
    outOfScopeObservations: [],
  },
};

const cleanDecision = { verdict: "clean", basis: "No finding gates.", findings: [] };
const judgementSamples = {
  "push-inspection": [{
    input: { outcomes: ["no-review", "increment", "full"] },
    answer: { outcome: "increment", reason: "Only the parser changed." },
  }],
  "verdict-classification": [
    { input: { findings: { main: [] } }, answer: { main: cleanDecision } },
    {
      input: { findings: { main: [], brief: [{ key: "parser" }] } },
      answer: {
        main: cleanDecision,
        brief: {
          verdict: "request-changes",
          basis: "The parser drops a valid record.",
          findings: [{ key: "parser", gating: true, class: "", defect: "", undergrade: "" }],
        },
      },
    },
  ],
  "earlier-findings": [{
    input: { findings: [{ key: "thread-1" }] },
    answer: { findings: [{ key: "thread-1", outcome: "stands", reason: "The parser still drops the record." }] },
  }],
  "repository-description": [{
    input: { commit: "1111111111111111111111111111111111111111", earlier: null },
    answer: { description: "A parsing library.\n\n## Layout\n\n- `src/`: the parser." },
  }],
  walkthrough: [
    {
      input: { files: ["src/parser.js"], diagrams: false },
      answer: { overview: "Parses a quoted field.", groups: [{ title: "Read quoted fields", summary: "`parse` keeps a quoted comma.", files: ["src/parser.js"] }] },
    },
    {
      input: { files: ["src/parser.js"], diagrams: true },
      answer: {
        overview: "Parses a quoted field.",
        groups: [{ title: "Read quoted fields", summary: "`parse` keeps a quoted comma.", files: ["src/parser.js"] }],
        flows: [{ title: "Reading a record", trace: "From `read` in `src/reader.js` to `parse` in `src/parser.js`." }],
      },
    },
  ],
  "walkthrough-diagrams": [{
    input: { flows: [{ title: "Reading a record", trace: "From `read` to `parse`." }] },
    answer: {
      diagrams: [{
        flow: "Reading a record",
        participants: ["reader", "parser"],
        steps: [{ from: "reader", to: "parser", message: "parse(line)", reply: false }],
        undrawn: "",
      }],
    },
  }],
};

function assertPinnedRuntimeUnchanged(t) {
  const original = readFileSync(pinnedRuntimePath);
  t.after(() => {
    assert.deepEqual(
      readFileSync(pinnedRuntimePath),
      original,
      "the compatibility gate leaves the pinned runtime byte-identical",
    );
  });
}

function shippedWorkflowSources() {
  return readdirSync(workflowsDir, { withFileTypes: true })
    .filter((entry) => entry.isFile() && entry.name.endsWith(".js"))
    .map((entry) => ({
      filename: entry.name,
      source: readFileSync(join(workflowsDir, entry.name), "utf8"),
    }))
    .sort((left, right) => left.filename.localeCompare(right.filename));
}

async function installedFreeIdentifierValidator(t) {
  assertPinnedRuntimeUnchanged(t);
  const root = operationRoot(t, "minos-installed-runtime-globals-");
  const installation = installInPlace(root);
  assert.equal(installation.status, 0, installation.stderr);
  assert.match(installation.stdout, /ensemble\.mjs: OK/);

  const installedRuntime = join(root, "runtime", "ensemble.mjs");
  const runtimeSource = readFileSync(installedRuntime, "utf8");
  const probeRuntime = join(root, "runtime-global-probe.mjs");
  writeFileSync(
    probeRuntime,
    `${runtimeSource}
export {
  assertKnownFreeIdentifiers as minosAssertKnownFreeIdentifiers,
  buildWrappedSource as minosBuildWrappedSource,
  extractWorkflowSource as minosExtractWorkflowSource,
};
`,
  );
  const module = await import(`${pathToFileURL(probeRuntime).href}?probe=${Date.now()}`);
  return (source, filename) => {
    module.minosAssertKnownFreeIdentifiers(
      module.minosBuildWrappedSource(module.minosExtractWorkflowSource(source)),
      filename,
    );
  };
}

test("the installed runtime recognises every agent option key used by shipped workflows", async (t) => {
  const validate = await installedOptionValidator(t);
  const callSites = optionKeySetsFromShippedWorkflows();

  assert.deepEqual(
    [...new Set(callSites.flatMap(({ keys }) => keys))].sort(),
    ["effort", "engine", "identity", "label", "model", "phase", "schema", "strip"],
  );
  for (const { filename, keys } of callSites) {
    assert.doesNotThrow(
      () => validate(valuesFor(keys)),
      `${filename} passes only options recognised by the installed runtime`,
    );
  }
});

test("the installed runtime rejects removed and unknown agent options before execution", async (t) => {
  const validate = await installedOptionValidator(t);
  const base = valuesFor(["engine", "model", "effort", "label", "phase"]);

  for (const key of ["sandbox", "network", "webSearch"]) {
    assert.throws(
      () => validate({ ...base, [key]: true }),
      (error) => error?.name === "RemovedAgentOptionError" &&
        error.message.includes(key),
      `${key} is rejected as a removed option`,
    );
  }
  assert.throws(
    () => validate({ ...base, unrecognisedMinosOption: true }),
    (error) => error?.name === "UnrecognisedAgentOptionError" &&
      error.message.includes("unrecognisedMinosOption"),
  );
});

test("the installed runtime accepts the free identifiers in every shipped workflow", async (t) => {
  const validate = await installedFreeIdentifierValidator(t);

  for (const { filename, source } of shippedWorkflowSources()) {
    assert.doesNotThrow(
      () => validate(source, filename),
      `${filename} uses only identifiers the installed runtime defines`,
    );
  }
});

test("the installed runtime compiles every shipped stage schema and accepts a well-formed answer to it", async (t) => {
  const { root, compile, forCodex, validate } = await installedSchemaValidator(t);
  const contracts = shippedContracts();
  const { loadStage } = await import(pathToFileURL(join(root, "workflows", "judgement-stage.mjs")));
  const directory = join(root, "workflows", "judgements");
  const stages = readdirSync(directory).filter((entry) => entry.endsWith(".mjs")).map((entry) => entry.slice(0, -4));
  assert.deepEqual(stages.sort(), Object.keys(judgementSamples).sort(), "each installed judgement needs a sample");
  for (const [name, sample] of Object.entries(schemaSamples)) assertSchema(name, contracts[name], sample);
  for (const name of stages) {
    const stage = await loadStage(name);
    for (const { input, answer } of judgementSamples[name]) assertSchema(name, stage.answerSchema(input), answer);
  }

  function assertSchema(name, schema, sample) {
    assert.doesNotThrow(() => compile(schema), `${name} compiles under the installed runtime`);
    const codexSchema = forCodex(schema);
    assert.doesNotThrow(() => compile(codexSchema), `${name} compiles as a Codex output schema`);
    assert.ok(validate(sample, schema), `${name} accepts a well-formed answer`);
    assert.ok(validate(sample, codexSchema), `${name} accepts a well-formed answer as a Codex output schema`);
  }

  assert.ok(!validate({ ...schemaSamples.verifierSchema, verdicts: [{ ...schemaSamples.verifierSchema.verdicts[0], verdict: "upheld" }] }, contracts.verifierSchema),
    "a verdict outside the three is refused");
  assert.ok(!validate({ ...schemaSamples.verifierSchema, newFindings: [sampleFinding] }, contracts.verifierSchema),
    "a raised finding that does not name the finding it was found while checking is refused");
});

test("the installed runtime refuses a stage schema shape it cannot compile", async (t) => {
  const { compile } = await installedSchemaValidator(t);
  const contracts = shippedContracts();
  contracts.verifierSchema.properties.verdicts.items.properties.verdict.enum = "confirmed";
  assert.throws(() => compile(contracts.verifierSchema), (error) => error?.name === "InvalidAgentSchemaError");
});

function writeInstallerFixture(t, checksumLine) {
  const sourceRoot = operationRoot(t, "minos-installer-source-");
  mkdirSync(join(sourceRoot, "scripts"), { recursive: true });
  mkdirSync(join(sourceRoot, "runtime"), { recursive: true });
  mkdirSync(join(sourceRoot, "workflows"), { recursive: true });
  copyFileSync(installerPath, join(sourceRoot, "scripts", "install-review-runtime"));
  chmodSync(join(sourceRoot, "scripts", "install-review-runtime"), 0o755);
  // The fixture declares no tools, so the preflight passes without
  // reading the machine the tests happen to run on.
  writeFileSync(join(sourceRoot, "scripts", "expected-tool-versions"), "# fixture declared tool set\n");

  writeFileSync(join(sourceRoot, "runtime", "ensemble.mjs"), installerBundle);
  writeFileSync(join(sourceRoot, "runtime", "ensemble.mjs.sha256"), checksumLine);
  writeFileSync(
    join(sourceRoot, "runtime", "ensemble.source-version"),
    "revision=installer-fixture-0728\n",
  );
  writeFileSync(join(sourceRoot, "workflows", "installed.js"), "return {};\n");

  return {
    sourceRoot,
    bundle: installerBundle,
    installer: join(sourceRoot, "scripts", "install-review-runtime"),
  };
}

function runRejectedInstallation(t, checksumLine) {
  const fixture = writeInstallerFixture(t, checksumLine);
  const destination = operationRoot(t, "minos-installer-destination-");
  mkdirSync(join(destination, "workflows"), { recursive: true });
  const staleTest = join(destination, "workflows", "stale_test.mjs");
  writeFileSync(staleTest, "must survive validation failure\n");

  const result = spawnSync(fixture.installer, [destination], {
    cwd: tmpdir(),
    encoding: "utf8",
  });
  assert.notEqual(result.status, 0, "invalid bundle metadata must fail installation");
  assert.equal(
    readFileSync(join(destination, "runtime", "ensemble.mjs"), "utf8"),
    fixture.bundle,
    "the assertion observes the runtime copy that the destination consumer checked",
  );
  assert.equal(
    existsSync(staleTest),
    true,
    "checksum rejection happens before the installer mutates the workflow tree",
  );
  return result;
}

test("the installer rejects a checksum that records a path instead of the destination filename", (t) => {
  const digest = createHash("sha256").update(installerBundle).digest("hex");
  const result = runRejectedInstallation(t, `${digest}  runtime/ensemble.mjs\n`);

  assert.match(result.stderr, /runtime\/ensemble\.mjs: No such file or directory/);
});

test("the installer rejects a launcher whose bare-filename checksum does not match", (t) => {
  const result = runRejectedInstallation(t, `${"0".repeat(64)}  ensemble.mjs\n`);

  assert.match(result.stdout + result.stderr, /ensemble\.mjs: FAILED/);
});
