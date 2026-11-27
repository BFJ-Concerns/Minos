import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { invokeWorkflow, loadWorkflow } from "./workflow-invocation-fixture.mjs";
import { adjudicateEnvelope } from "./archive-fixture.mjs";
import { findingsFromVerifierPrompt, verdictFor } from "./verifier-prompt-fixture.mjs";

const inputScriptPath = fileURLToPath(new URL("./quick-review-inputs.mjs", import.meta.url));
const script = loadWorkflow("quick-review.js");

// The input the builder emits for a repository whose change touches
// `internal/a.go` and `internal/b.go`, with the two commits' shas.
function builtArgs({ depth = "" } = {}) {
  const root = mkdtempSync(join(tmpdir(), "minos-quick-review-inputs-"));
  try {
    const workspace = join(root, "workspace");
    mkdirSync(join(workspace, "internal"), { recursive: true });
    const git = (...gitArgs) => execFileSync("git", ["-C", workspace, ...gitArgs], {
      encoding: "utf8",
      env: { ...process.env, GIT_CONFIG_GLOBAL: "/dev/null", GIT_CONFIG_SYSTEM: "/dev/null" },
    }).trim();
    git("init", "-q");
    writeFileSync(join(workspace, "README.md"), "MINOS_TEST_COMMISSION_QUICK");
    git("add", ".");
    git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "base");
    const target = git("rev-parse", "HEAD");
    writeFileSync(join(workspace, "internal", "a.go"), "package a\n");
    writeFileSync(join(workspace, "internal", "b.go"), "package b\n");
    git("add", ".");
    git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "change");
    const head = git("rev-parse", "HEAD");
    const orientationPath = join(root, "orientation.json");
    writeFileSync(orientationPath, JSON.stringify({
      repository: workspace,
      guidance: [{ source: { path: "README.md" }, location: join(workspace, "README.md"), origin: "checked-in" }],
      misconfigurations: [],
    }));
    return JSON.parse(execFileSync(process.execPath, [inputScriptPath, target, head], {
      encoding: "utf8",
      env: {
        ...process.env,
        MINOS_ORIENTATION: orientationPath,
        MINOS_PROVISIONED_ENGINES: "claude codex",
        MINOS_ROUTING: "",
        MINOS_FILE_SAFEGUARD_REQUESTS: "",
        MINOS_REVIEW_DEPTH: depth,
      },
      stdio: ["ignore", "pipe", "pipe"],
    }));
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

const ARGS = builtArgs();

function finding(overrides = {}) {
  return {
    title: "incorrect transition",
    severity: "High",
    confidence: 88,
    path: "internal/a.go",
    line: 1,
    explanation: "the new branch admits an invalid state",
    dependsOn: "",
    ...overrides,
  };
}

function wholeChangeResult({ shares = [], findings = [finding()], outOfScopeObservations = [] } = {}) {
  return { shares, findings, outOfScopeObservations };
}

const TWO_SHARES = [
  { id: "a", focus: "The new package a.", paths: ["internal/a.go"] },
  { id: "b", focus: "The new package b.", paths: ["internal/b.go"] },
];

function verifierResult(prompt) {
  return {
    verdicts: findingsFromVerifierPrompt(prompt).map((entry) => verdictFor(entry.id)),
    newFindings: [],
    outOfScopeObservations: [],
  };
}

function separateGroups(prompt) {
  return {
    groups: findingsFromVerifierPrompt(prompt).map((entry) => ({ findingIds: [entry.id], primary: entry.id, sharedCause: "" })),
  };
}

function responder({ whole = wholeChangeResult(), share } = {}) {
  return (label, prompt, opts) => {
    if (label === "general-codex") return typeof whole === "function" ? whole(prompt, opts) : whole;
    if (label.startsWith("general-share-"))
      return share ? share(label, prompt, opts) : { findings: [finding({ title: `from ${label}` })], outOfScopeObservations: [] };
    if (label === "merge" || label.startsWith("merge-round-")) return separateGroups(prompt);
    if (label.startsWith("verify-")) return verifierResult(prompt);
    throw new Error(`unexpected leg ${label}`);
  };
}

test("the builder hands the workflow the change's paths and the general, merge and verifier prompts alone", () => {
  assert.deepEqual(ARGS.changedPaths, ["internal/a.go", "internal/b.go"]);
  assert.deepEqual(Object.keys(ARGS.prompts).sort(), ["general", "generalAssignment", "merge", "verifier"]);
  assert.deepEqual(Object.keys(ARGS.prompts.generalAssignment).sort(), ["share", "wholeChange"]);
  assert.equal(ARGS.concerns, undefined);
  assert.equal(ARGS.concernRouting, undefined);
  assert.match(ARGS.prompts.general, /MINOS_TEST_COMMISSION_QUICK|\{\{project-documents\}\}/);
  assert.equal(ARGS.routing.proposer.engine, "codex");
  assert.equal(ARGS.routing.verifier.engine, "claude");
  assert.notEqual(builtArgs({ depth: "thorough" }).prompts.general, ARGS.prompts.general);
});

test("an undivided change is one general leg on the proposer's route, its findings verified on the verifier's", async (t) => {
  const { result, calls } = await invokeWorkflow(script, ARGS, responder({
    whole: wholeChangeResult({ findings: [finding(), finding({ title: "second", path: "internal/b.go" })] }),
  }));
  const labels = calls.map((call) => call.opts.label);
  assert.deepEqual(labels, ["general-codex", "merge", "verify-1-claude"]);
  const [general] = calls;
  assert.equal(general.opts.engine, "codex");
  assert.deepEqual(general.opts.schema, ARGS.contracts.generalWholeChangeSchema);
  assert.match(general.prompt, new RegExp(`git diff ${ARGS.target}\\.\\.\\.${ARGS.head}`));
  assert.doesNotMatch(general.prompt, /\{\{[a-z-]+\}\}/);
  assert.equal(calls[2].opts.engine, "claude");
  assert.equal(result.mode, "quick");
  assert.equal(result.incomplete, undefined);
  assert.deepEqual(result.requiredModelEvidence.map((leg) => [leg.label, leg.role]), [
    ["general-codex", "general"], ["merge", "merge"], ["verify-1-claude", "verifier"],
  ]);
  assert.deepEqual(result.proposedFindings.map((item) => [item.id, item.source]), [
    ["general-codex:1", "general review of the whole change"],
    ["general-codex:2", "general review of the whole change"],
  ]);
  const shownToMerge = findingsFromVerifierPrompt(calls[1].prompt);
  assert.deepEqual(shownToMerge.map((item) => [item.reportedBy, item.outsideConcern]), [["general", false], ["general", false]]);
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete");
  assert.equal(verdict.mode, "quick");
  assert.equal(verdict.confirmedFindings.length, 2);
});

test("no shares is no division, and the whole-change answer is the review", async () => {
  const { calls, result } = await invokeWorkflow(script, ARGS, responder({ whole: wholeChangeResult() }));
  assert.deepEqual(calls.map((call) => call.opts.label), ["general-codex", "verify-1-claude"]);
  assert.deepEqual(result.dispatches, []);
});

// A reviewer that divides leaves the reading to the share reviewers, so a
// one-share answer with no findings is a change nobody read.
test("a single share leaves the review incomplete, never clean", async (t) => {
  const { calls, result } = await invokeWorkflow(script, ARGS, responder({
    whole: wholeChangeResult({ shares: [TWO_SHARES[0]], findings: [] }),
  }));
  assert.deepEqual(calls.map((call) => call.opts.label), ["general-codex"]);
  assert.match(result.incomplete.join("\n"), /division/);
  assert.equal((await adjudicateEnvelope(t, result)).status, "incomplete");
});

test("a divided change sends one reviewer for each share, told its share and the others', and keeps the divider's findings", async () => {
  const { calls, result } = await invokeWorkflow(script, ARGS, responder({
    whole: wholeChangeResult({ shares: TWO_SHARES }),
  }));
  const shareCalls = calls.filter((call) => call.opts.label.startsWith("general-share-"));
  assert.deepEqual(shareCalls.map((call) => call.opts.label), ["general-share-1-codex", "general-share-2-codex"]);
  for (const call of shareCalls) {
    assert.deepEqual(call.opts.schema, ARGS.contracts.generalSchema);
    assert.equal(call.opts.engine, "codex");
    assert.doesNotMatch(call.prompt, /\{\{[a-z-]+\}\}/);
  }
  const [first, second] = shareCalls.map((call) => call.prompt);
  const yours = (prompt) => prompt.slice(prompt.indexOf("Your share:"), prompt.indexOf("The other reviewers' shares:"));
  const others = (prompt) => prompt.slice(prompt.indexOf("The other reviewers' shares:"));
  assert.match(yours(first), /The new package a\.\n\n- `internal\/a\.go`/);
  assert.doesNotMatch(yours(first), /internal\/b\.go/);
  assert.match(others(first), /The new package b\.\n\n- `internal\/b\.go`/);
  assert.match(yours(second), /internal\/b\.go/);
  assert.match(others(second), /internal\/a\.go/);
  assert.deepEqual(result.proposedFindings.map((item) => [item.id, item.source]), [
    ["general-codex:1", "general review of the whole change"],
    ["general-share-1-codex:1", "The new package a."],
    ["general-share-2-codex:1", "The new package b."],
  ]);
  assert.deepEqual(result.dispatches.map((dispatch) => [dispatch.id, dispatch.paths]), [["a", ["internal/a.go"]], ["b", ["internal/b.go"]]]);
  assert.deepEqual(result.reviewers.map((reviewer) => [reviewer.label, reviewer.share?.id]), [
    ["general-codex", undefined], ["general-share-1-codex", "a"], ["general-share-2-codex", "b"],
  ]);
  assert.equal(result.incomplete, undefined);
});

test("the verdict's summary shows the general reviewers as one section, crediting each confirmed finding once", async (t) => {
  const { result } = await invokeWorkflow(script, ARGS, (label, prompt, opts) => {
    if (label === "merge") {
      // The divider's finding and the first share's are one fault.
      const ids = findingsFromVerifierPrompt(prompt).map((entry) => entry.id);
      return { groups: [
        { findingIds: [ids[0], ids[1]], primary: ids[1], sharedCause: "one fix" },
        { findingIds: [ids[2]], primary: ids[2], sharedCause: "" },
      ] };
    }
    return responder({ whole: wholeChangeResult({ shares: TWO_SHARES }) })(label, prompt, opts);
  });
  const verdict = await adjudicateEnvelope(t, result);
  assert.equal(verdict.status, "complete");
  assert.deepEqual(verdict.summarySections, [{ kind: "general", name: "General review", findings: 2 }]);
});

test("a division no reviewer can be sent on leaves the review incomplete before any share is dispatched", async (t) => {
  const malformed = [
    [{ ...TWO_SHARES[0] }, { ...TWO_SHARES[1], id: "a" }],
    [TWO_SHARES[0], { ...TWO_SHARES[1], focus: " " }],
    [TWO_SHARES[0], { ...TWO_SHARES[1], paths: [] }],
    [TWO_SHARES[0], { ...TWO_SHARES[1], paths: ["internal/a.go"] }],
    [TWO_SHARES[0], { ...TWO_SHARES[1], paths: ["internal/unchanged.go"] }],
  ];
  for (const shares of malformed) {
    const { calls, result } = await invokeWorkflow(script, ARGS, responder({ whole: wholeChangeResult({ shares }) }));
    assert.deepEqual(calls.map((call) => call.opts.label), ["general-codex"]);
    assert.match(result.incomplete.join("\n"), /division/);
    assert.equal((await adjudicateEnvelope(t, result)).status, "incomplete");
  }
});

test("a general leg that fails leaves the review incomplete and publishes nothing", async (t) => {
  const failedWhole = await invokeWorkflow(script, ARGS, responder({ whole: null }));
  assert.deepEqual(failedWhole.calls.map((call) => call.opts.label), ["general-codex"]);
  assert.equal((await adjudicateEnvelope(t, failedWhole.result)).status, "incomplete");

  const failedShare = await invokeWorkflow(script, ARGS, responder({
    whole: wholeChangeResult({ shares: TWO_SHARES, findings: [] }),
    share: (label) => (label === "general-share-2-codex" ? null : { findings: [], outOfScopeObservations: [] }),
  }));
  assert.match(failedShare.result.incomplete.join("\n"), /general-share-2-codex/);
  assert.deepEqual(failedShare.result.reviewers.map((reviewer) => reviewer.status), ["done", "done", "no-result"]);
  assert.equal((await adjudicateEnvelope(t, failedShare.result)).status, "incomplete");
});

test("what a general reviewer saw outside the change is recorded under its source", async () => {
  const observation = { title: "old defect", path: "internal/old.go", line: 2, explanation: "Unverified: it was already wrong." };
  const { result } = await invokeWorkflow(script, ARGS, responder({
    whole: wholeChangeResult({ findings: [], outOfScopeObservations: [observation] }),
  }));
  assert.deepEqual(result.outOfScopeObservations.map((item) => [item.source, item.observingLabel, item.title]), [
    ["general review of the whole change", "general-codex", "old defect"],
  ]);
});
