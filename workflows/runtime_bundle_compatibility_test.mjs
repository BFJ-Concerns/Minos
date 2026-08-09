import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { pathToFileURL, fileURLToPath } from "node:url";
import test from "node:test";

const workflowsDir = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = dirname(workflowsDir);
const installerPath = join(repositoryRoot, "scripts", "install-review-runtime");
const pinnedRuntimePath = join(repositoryRoot, "runtime", "ensemble.mjs");

const optionBlockPattern =
  /\{\s*\n\s*engine:\s*[^,\n]+,\s*\n\s*schema:\s*[^,\n]+,\s*\n\s*model:\s*[^,\n]+,\s*\n\s*effort:\s*[^,\n]+,\s*\n(?:\s*isolation:\s*[^,\n]+,\s*\n)?\s*label:\s*[^,\n]+,\s*\n\s*phase:\s*[^,\n]+,\s*\n\s*\}/g;

const expectedCallSites = new Map([
  ["review.js", 3],
  ["review-briefs.js", 4],
  ["fix.js", 3],
  ["setup.js", 1],
  ["rootcause.js", 1],
]);

function operationRoot(t, prefix) {
  const root = mkdtempSync(join(tmpdir(), prefix));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  return root;
}

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
    model: "gpt-5.6-sol",
    effort: "high",
    isolation: "worktree",
    label: "minos-runtime-compatibility",
    phase: "Compatibility",
  };
  return Object.fromEntries(keys.map((key) => [key, values[key]]));
}

async function installedOptionValidator(t) {
  const root = operationRoot(t, "minos-installed-runtime-");
  const installation = spawnSync(installerPath, [root], {
    cwd: repositoryRoot,
    encoding: "utf8",
  });
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

function sandboxBindingKeys(runtimeSource) {
  const match = runtimeSource.match(
    /function createSandboxContext\(hooks, meta\) \{[\s\S]*?\n  const bindings = \{\n(?<bindings>[\s\S]*?)\n  \};\n  const context = vm\.createContext\(bindings,/,
  );
  assert.ok(
    match?.groups?.bindings,
    "installed runtime createSandboxContext binding block changed shape; update the gate deliberately",
  );

  const keys = [];
  for (const line of match.groups.bindings.split("\n")) {
    const property = /^    ([A-Za-z_$][A-Za-z0-9_$]*):/.exec(line);
    const shorthand = /^    ([A-Za-z_$][A-Za-z0-9_$]*),$/.exec(line);
    if (property !== null || shorthand !== null) {
      keys.push((property ?? shorthand)[1]);
    }
  }
  assert.ok(keys.length > 0, "installed runtime sandbox binding block has no recognisable keys");
  assert.equal(
    new Set(keys).size,
    keys.length,
    "installed runtime sandbox binding block contains duplicate keys",
  );
  return keys;
}

function sandboxRuntimeSource(bindings) {
  return `function createSandboxContext(hooks, meta) {
  const bindings = {
${bindings}
  };
  const context = vm.createContext(bindings, {});
}`;
}

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
  const installation = spawnSync(installerPath, [root], {
    cwd: repositoryRoot,
    encoding: "utf8",
  });
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
  compileWorkflowScript as minosCompileWorkflowScript,
  extractWorkflowSource as minosExtractWorkflowSource,
  WorkflowScriptError as MinosWorkflowScriptError,
};
`,
  );
  const module = await import(`${pathToFileURL(probeRuntime).href}?probe=${Date.now()}`);
  const context = Object.fromEntries(
    sandboxBindingKeys(runtimeSource).map((key) => [key, undefined]),
  );
  return (source, filename) => {
    const extracted = module.minosExtractWorkflowSource(source);
    try {
      module.minosCompileWorkflowScript(extracted, filename);
    } catch (error) {
      if (error instanceof module.MinosWorkflowScriptError) {
        throw error;
      }
      if (!(error instanceof SyntaxError)) {
        throw error;
      }
      throw new SyntaxError(
        `Workflow ${filename} could not be parsed: ${error.message}`,
        { cause: error },
      );
    }
    module.minosAssertKnownFreeIdentifiers(
      module.minosBuildWrappedSource(extracted),
      context,
      filename,
    );
  };
}

test("sandbox binding extraction fails loudly when the installed runtime shape drifts", () => {
  assert.throws(
    () => sandboxBindingKeys("function createSandboxContext() {}"),
    /createSandboxContext binding block changed shape/,
  );
  assert.throws(
    () => sandboxBindingKeys(sandboxRuntimeSource("    // no bindings")),
    /sandbox binding block has no recognisable keys/,
  );
  assert.throws(
    () => sandboxBindingKeys(sandboxRuntimeSource("    repeated: hooks.first,\n    repeated: hooks.second,")),
    /sandbox binding block contains duplicate keys/,
  );
});

test("the installed runtime recognises every agent option key used by shipped workflows", async (t) => {
  const validate = await installedOptionValidator(t);
  const callSites = optionKeySetsFromShippedWorkflows();

  assert.deepEqual(
    [...new Set(callSites.flatMap(({ keys }) => keys))].sort(),
    ["effort", "engine", "isolation", "label", "model", "phase", "schema"],
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
      `${filename} uses only identifiers available in the installed runtime sandbox`,
    );
  }
});

test("the installed runtime free-identifier rule reports absent globals and rejects dynamic imports", async (t) => {
  const validate = await installedFreeIdentifierValidator(t);
  const workflow = (body) => `export const meta = { name: "probe" };\n${body}\n`;

  assert.doesNotThrow(() => validate(workflow("return typeof Buffer;"), "typeof-probe.js"));
  assert.throws(
    () => validate(workflow("return Buffer.from('probe');"), "buffer-probe.js"),
    (error) => error?.name === "WorkflowScriptError" &&
      error.message.includes("buffer-probe.js") &&
      error.message.includes("Buffer"),
  );
  assert.throws(
    () => validate(workflow("return import('probe');"), "import-probe.js"),
    (error) => error?.name === "WorkflowScriptError" &&
      error.message.includes("import-probe.js") &&
      error.message.includes("dynamic import()"),
  );
  assert.throws(
    () => validate(workflow("Buffer.from('probe'); const broken = (((;"), "unparseable-probe.js"),
    (error) => error instanceof SyntaxError &&
      error.message.includes("unparseable-probe.js") &&
      error.message.includes("could not be parsed"),
  );
  assert.throws(
    () => validate(
      `${workflow("const intervening = true;")}export const defaults = {};\n`,
      "misplaced-defaults-probe.js",
    ),
    (error) => error?.name === "WorkflowScriptError" &&
      error.message.includes("export const defaults") &&
      error.message.includes("must follow `meta`") &&
      !error.message.includes("could not be parsed"),
  );
});

function writeInstallerFixture(t, checksumLine) {
  const sourceRoot = operationRoot(t, "minos-installer-source-");
  mkdirSync(join(sourceRoot, "scripts"), { recursive: true });
  mkdirSync(join(sourceRoot, "runtime"), { recursive: true });
  mkdirSync(join(sourceRoot, "workflows"), { recursive: true });
  copyFileSync(installerPath, join(sourceRoot, "scripts", "install-review-runtime"));
  chmodSync(join(sourceRoot, "scripts", "install-review-runtime"), 0o755);

  const bundle = "DISTINCT-INSTALLER-BUNDLE-0728\n";
  writeFileSync(join(sourceRoot, "runtime", "ensemble.mjs"), bundle);
  writeFileSync(join(sourceRoot, "runtime", "ensemble.mjs.sha256"), checksumLine);
  writeFileSync(
    join(sourceRoot, "runtime", "ensemble.source-version"),
    "revision=installer-fixture-0728\n",
  );
  writeFileSync(join(sourceRoot, "workflows", "installed.js"), "return {};\n");

  return {
    sourceRoot,
    bundle,
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
  const bundle = "DISTINCT-INSTALLER-BUNDLE-0728\n";
  const digest = createHash("sha256").update(bundle).digest("hex");
  const result = runRejectedInstallation(t, `${digest}  runtime/ensemble.mjs\n`);

  assert.match(result.stderr, /runtime\/ensemble\.mjs: No such file or directory/);
});

test("the installer rejects a launcher whose bare-filename checksum does not match", (t) => {
  const result = runRejectedInstallation(t, `${"0".repeat(64)}  ensemble.mjs\n`);

  assert.match(result.stdout + result.stderr, /ensemble\.mjs: FAILED/);
});
