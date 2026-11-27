import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { driveRun } from "./run-driver.mjs";
import { CHANGES_REQUESTED, CLEAN, HeadMoved, INCOMPLETE, SUPERSEDED } from "./run-endings.mjs";
import { writeSummary } from "./run-steps/forge-writes.mjs";
import { writeWalkthrough } from "./run-steps/write-walkthrough.mjs";
import { operationRoot } from "./temporary-directory-fixture.mjs";
import { RECORD_KIND } from "./walkthrough.mjs";

// A clone holding two commits, the target and the head, so the duty reads
// the change's paths from Git for real.
function clone(root) {
  const workspace = join(root, "workspace");
  const git = (...args) => execFileSync("git", ["-C", workspace, ...args], {
    encoding: "utf8",
    env: { PATH: process.env.PATH, GIT_CONFIG_GLOBAL: "/dev/null", GIT_CONFIG_NOSYSTEM: "1", HOME: root },
  }).trim();
  execFileSync("git", ["init", "-q", workspace], { env: { PATH: process.env.PATH, GIT_CONFIG_GLOBAL: "/dev/null", HOME: root } });
  const commit = (path, text) => {
    writeFileSync(join(workspace, path), text);
    git("add", path);
    git("-c", "user.name=Author", "-c", "user.email=author@example.test", "commit", "-q", "-m", path);
    return git("rev-parse", "HEAD");
  };
  const target = commit("README.md", "A parser.\n");
  commit("parser.go", "package parser\n");
  const head = commit("parser_test.go", "package parser\n");
  return { workspace, target, head };
}

const flows = [{ title: "Parsing a request", trace: "Client calls Parser.parse, which returns tokens." }];
const drawn = {
  diagrams: [{
    flow: "Parsing a request",
    participants: ["Client", "Parser"],
    steps: [
      { from: "Client", to: "Parser", message: "parse(text)", reply: false },
      { from: "Parser", to: "Client", message: "tokens", reply: true },
    ],
    undrawn: "",
  }],
};

// A run whose forge and stages are stand-ins. `answers` gives each stage's
// answer, or an Error it throws; `forge` may answer an operation itself.
function fakeRun(t, { diagrams = true, enabled = true, answers = {}, forge = {}, models = {} } = {}) {
  const root = operationRoot(t, "minos-write-walkthrough-");
  const { workspace, target, head } = clone(root);
  const orientation = join(root, "orientation.json");
  writeFileSync(orientation, "{}\n");
  const recordPath = join(root, "runs", ".walkthroughs", "pr.json");
  const asked = [];
  const summaries = [];
  const forgeCalls = [];
  const run = {
    head, target, workspace, orientation, runDir: root,
    env: {
      MINOS_PROVISIONED_ENGINES: "claude codex",
      MINOS_ROUTING: "{}",
      MINOS_WALKTHROUGH: JSON.stringify({ enabled, diagrams }),
      MINOS_WALKTHROUGH_RECORD: recordPath,
    },
    judgementModels: new Map(),
    async judge(stage, input) {
      asked.push({ stage: stage.name, input });
      const answer = answers[stage.name];
      if (answer instanceof Error) throw answer;
      if (answer === undefined) throw new Error(`stage ${stage.name} was not expected`);
      if (models[stage.name]) run.judgementModels.set(stage.name, models[stage.name]);
      return answer;
    },
    async forge(operation, ...args) {
      forgeCalls.push(operation);
      if (forge[operation]) return forge[operation](...args);
      if (operation === "snapshot") return { head_sha: head };
      if (operation === "summary") {
        const [, , state, text] = args;
        summaries.push({ state, body: readFileSync(text, "utf8") });
        return { outcome: "applied" };
      }
      throw new Error(`unexpected forge operation ${operation}`);
    },
  };
  return { run, asked, summaries, forgeCalls, recordPath };
}

function written(overrides = {}) {
  return {
    overview: "The parser gains its first file and a test.",
    groups: [{ title: "Parser", summary: "A package and its test.", files: ["parser.go", "parser_test.go"] }],
    flows,
    ...overrides,
  };
}

function settledFor(run, reviewBase = run.target) {
  return { inspection: { outcome: "full", reviewBase }, review: { summarySections: [] }, outstanding: [] };
}

test("a reviewed head's walkthrough goes into the summary in the state the run ended on, and is kept for the next head", async (t) => {
  const fake = fakeRun(t, {
    answers: { walkthrough: written(), "walkthrough-diagrams": drawn },
    models: { walkthrough: "claude-opus-5-5-served", "walkthrough-diagrams": "claude-opus-5-5-fallback" },
  });
  const { run } = fake;
  const problems = [];

  const sections = [
    { kind: "concern", name: "Correctness", findings: 2 },
    { kind: "brief", name: "Parser", findings: 1 },
  ];
  const outstanding = [{ path: "lexer.go", line: 18, head: "4".repeat(40), title: "Lexer skips tokens" }];
  const settled = {
    ...settledFor(run),
    review: { summarySections: sections.slice(0, 1) },
    briefReview: { summarySections: sections.slice(1) },
    outstanding,
  };

  await writeSummary(run, CHANGES_REQUESTED, { sections, outstanding });
  await writeWalkthrough(run, settled, { outcome: CHANGES_REQUESTED }, problems);

  assert.deepEqual(problems, []);
  assert.deepEqual(fake.asked.map(({ stage }) => stage), ["walkthrough", "walkthrough-diagrams"]);
  assert.deepEqual(fake.asked[0].input, {
    target: run.target, head: run.head, files: ["parser.go", "parser_test.go"], earlier: null, diagrams: true,
  });
  assert.deepEqual(fake.asked[1].input, { target: run.target, head: run.head, flows });
  assert.equal(fake.summaries.length, 2);
  assert.equal(fake.summaries[1].state, CHANGES_REQUESTED);
  const { body } = fake.summaries[1];
  assert.ok(fake.summaries[0].body.includes("| Correctness | 2 |"));
  assert.ok(fake.summaries[0].body.includes("| Brief `Parser` | 1 |"));
  assert.ok(fake.summaries[0].body.includes("- `Lexer skips tokens` at `lexer.go:18`, raised at `4444444`"));
  assert.equal(body.split("### Walkthrough")[0].trimEnd(), fake.summaries[0].body.trimEnd());
  assert.match(body, /### Walkthrough\n\nThe parser gains its first file and a test\./);
  assert.match(body, /```mermaid\nsequenceDiagram\n    participant p1 as Client\n/);
  assert.match(body, /The review read the whole change against `.{7}`\. Written by `claude-opus-5-5-served`; diagrams traced by `claude-opus-5-5-fallback`\./);

  const kept = JSON.parse(readFileSync(fake.recordPath, "utf8"));
  assert.equal(kept.kind, RECORD_KIND);
  assert.equal(kept.head, run.head);
  assert.equal(kept.reviewBase, run.target);
  assert.deepEqual(kept.models, { walkthrough: "claude-opus-5-5-served", diagrams: "claude-opus-5-5-fallback" });
  assert.equal(kept.diagrams.length, 1);
});

test("the walkthrough names the routed models where the stages' results named none", async (t) => {
  const fake = fakeRun(t, { diagrams: false, answers: { walkthrough: written({ flows: undefined }) } });
  const problems = [];

  await writeWalkthrough(fake.run, settledFor(fake.run), { outcome: CLEAN }, problems);

  assert.deepEqual(problems, []);
  assert.deepEqual(fake.asked.map(({ stage }) => stage), ["walkthrough"]);
  assert.equal(fake.asked[0].input.diagrams, false);
  assert.match(fake.summaries[0].body, /Written by `claude-opus-5-5`\.$/m);
});

test("an increment's walkthrough revises the kept one and says the review read the increment", async (t) => {
  const fake = fakeRun(t, { diagrams: false, answers: { walkthrough: written({ flows: undefined }) } });
  const { run } = fake;
  const earlierHead = "4".repeat(40);
  const earlier = {
    kind: RECORD_KIND, head: earlierHead, target: run.target, reviewBase: run.target,
    overview: "The parser gains its first file.",
    groups: [{ title: "Parser", summary: "A package.", files: ["parser.go"] }],
    diagrams: [], models: { walkthrough: "claude-opus-5-5" },
  };
  execFileSync("mkdir", ["-p", join(fake.recordPath, "..")]);
  writeFileSync(fake.recordPath, JSON.stringify(earlier));
  const problems = [];

  await writeWalkthrough(run, settledFor(run, earlierHead), { outcome: CLEAN }, problems);

  assert.deepEqual(problems, []);
  assert.deepEqual(fake.asked[0].input.earlier, { head: earlierHead, overview: earlier.overview, groups: earlier.groups });
  assert.match(fake.summaries[0].body, /The review read what changed since `4444444`; this walkthrough covers the whole pull request\./);
  assert.equal(JSON.parse(readFileSync(fake.recordPath, "utf8")).head, run.head);
});

test("nothing is written where the walkthrough is off, the head was not reviewed or the run did not finish it", async (t) => {
  for (const [name, options, settled, ending] of [
    ["off", { enabled: false }, undefined, { outcome: CLEAN }],
    ["no review", {}, { inspection: { outcome: "no-review", reviewBase: null } }, { outcome: CLEAN }],
    ["incomplete", {}, undefined, { outcome: INCOMPLETE, step: "review", cause: "a leg failed" }],
    ["superseded", {}, undefined, { outcome: SUPERSEDED, step: "publish", cause: "moved" }],
  ]) {
    const fake = fakeRun(t, { ...options, answers: { walkthrough: written() } });
    const problems = [];
    await writeWalkthrough(fake.run, settled ?? settledFor(fake.run), ending, problems);
    assert.deepEqual(problems, [], name);
    assert.deepEqual(fake.asked, [], name);
    assert.deepEqual(fake.forgeCalls, [], name);
  }
});

test("a diagrams stage that fails leaves a walkthrough without diagrams and one anomaly", async (t) => {
  const fake = fakeRun(t, { answers: { walkthrough: written(), "walkthrough-diagrams": new Error("stage walkthrough-diagrams gave no answer") } });
  const problems = [];

  await writeWalkthrough(fake.run, settledFor(fake.run), { outcome: CLEAN }, problems);

  assert.deepEqual(problems, ["The walkthrough's diagrams were not drawn. stage walkthrough-diagrams gave no answer"]);
  assert.equal(fake.summaries.length, 1);
  assert.doesNotMatch(fake.summaries[0].body, /mermaid/);
  assert.match(fake.summaries[0].body, /Written by `claude-opus-5-5`\.$/m);
});

test("a flow the diagrams stage left undrawn is left out without an anomaly", async (t) => {
  const undrawn = { diagrams: [{ flow: "Parsing a request", participants: [], steps: [], undrawn: "The flow is one call." }] };
  const fake = fakeRun(t, { answers: { walkthrough: written(), "walkthrough-diagrams": undrawn } });
  const problems = [];

  await writeWalkthrough(fake.run, settledFor(fake.run), { outcome: CLEAN }, problems);

  assert.deepEqual(problems, []);
  assert.doesNotMatch(fake.summaries[0].body, /mermaid/);
});

test("a walkthrough stage that fails writes nothing and is one anomaly", async (t) => {
  const fake = fakeRun(t, { answers: { walkthrough: new Error("stage walkthrough ended without a result") } });
  const problems = [];

  await writeWalkthrough(fake.run, settledFor(fake.run), { outcome: CLEAN }, problems);

  assert.deepEqual(problems, ["The walkthrough was not written. stage walkthrough ended without a result"]);
  assert.deepEqual(fake.summaries, []);
  assert.equal(existsSync(fake.recordPath), false);
});

test("a head that moved before or during the writing gets no walkthrough and no anomaly", async (t) => {
  const before = fakeRun(t, { answers: { walkthrough: written() }, forge: { snapshot: () => ({ head_sha: "5".repeat(40) }) } });
  const problems = [];
  await writeWalkthrough(before.run, settledFor(before.run), { outcome: CLEAN }, problems);
  assert.deepEqual(before.asked, []);

  const during = fakeRun(t, {
    answers: { walkthrough: written(), "walkthrough-diagrams": drawn },
    forge: { summary: () => ({ outcome: "rejected", reason: "head moved" }) },
  });
  await writeWalkthrough(during.run, settledFor(during.run), { outcome: CLEAN }, problems);
  assert.equal(existsSync(during.recordPath), false);

  const thrown = fakeRun(t, { answers: { walkthrough: new HeadMoved("6".repeat(40)) } });
  await writeWalkthrough(thrown.run, settledFor(thrown.run), { outcome: CLEAN }, problems);

  assert.deepEqual(problems, []);
});

test("the report distinguishes an unconfirmed summary edit from a rejected edit", async (t) => {
  for (const { outcome, reason, report } of [
    { outcome: "uncertain", reason: "summary write could not be discovered", report: "could not be confirmed" },
    { outcome: "rejected", reason: "permission refused", report: "was not written" },
  ]) {
    let editedBody = "";
    const fake = fakeRun(t, {
      diagrams: false,
      answers: { walkthrough: written({ flows: undefined }) },
      forge: {
        summary: (_head, _target, _state, text) => {
          if (outcome === "uncertain") editedBody = readFileSync(text, "utf8");
          return { outcome, reason };
        },
      },
    });
    const problems = [];

    await writeWalkthrough(fake.run, settledFor(fake.run), { outcome: CLEAN }, problems);

    assert.deepEqual(problems, [`The walkthrough ${report}. forge summary: ${reason}`]);
    assert.equal(existsSync(fake.recordPath), false);
    assert.equal(editedBody.includes("### Walkthrough"), outcome === "uncertain");
  }
});

test("the shipped duties withdraw, record completion, then generate the walkthrough before reporting", async (t) => {
  const trace = [];
  const fake = fakeRun(t, {
    answers: { walkthrough: written(), "walkthrough-diagrams": drawn },
    forge: { marker: (_head, _target, name, action) => {
      trace.push([name, action]);
      return { outcome: "applied" };
    } },
  });
  const { run } = fake;
  run.pullRequest = { owner: "owner", repository: "repo", number: "1" };
  run.env.MINOS_SETUP_WORKSPACE = fileURLToPath(new URL("../scripts/run-body/setup-workspace", import.meta.url));
  const judge = run.judge;
  run.judge = (stage, input) => {
    trace.push([stage.name, readFileSync(join(run.runDir, "lead-complete"), "utf8"), existsSync(join(run.runDir, "report.md"))]);
    return judge(stage, input);
  };

  const ending = await driveRun(run, {
    complete: true,
    course: [async () => ({ ...settledFor(run), outcome: CLEAN, terminalStatusProblem: "The status write was refused." })],
  });

  assert.equal(ending.outcome, CLEAN);
  assert.deepEqual(trace, [
    ["in-flight", "remove"],
    ["walkthrough", "clean\n", false],
    ["walkthrough-diagrams", "clean\n", false],
  ]);
  assert.equal(fake.summaries.length, 1);
  assert.match(readFileSync(join(run.runDir, "report.md"), "utf8"), /The status write was refused/);
});

test("settings the duty cannot read are an anomaly, never a failure of the run", async (t) => {
  const fake = fakeRun(t, { answers: { walkthrough: written() } });
  fake.run.env.MINOS_WALKTHROUGH = "{";
  const problems = [];

  await writeWalkthrough(fake.run, settledFor(fake.run), { outcome: CLEAN }, problems);

  assert.equal(problems.length, 1);
  assert.match(problems[0], /^The walkthrough was not written\. MINOS_WALKTHROUGH is not valid JSON/);
  assert.deepEqual(fake.forgeCalls, []);
});

test("a summary write for a head with no new walkthrough shows the kept one without diagrams where they are off", async (t) => {
  const fake = fakeRun(t, { diagrams: false });
  const { run } = fake;
  execFileSync("mkdir", ["-p", join(fake.recordPath, "..")]);
  writeFileSync(fake.recordPath, JSON.stringify({
    kind: RECORD_KIND, head: "4".repeat(40), target: run.target, reviewBase: run.target,
    overview: "The parser gains its first file.",
    groups: [{ title: "Parser", summary: "A package.", files: ["parser.go"] }],
    diagrams: [{ title: drawn.diagrams[0].flow, participants: drawn.diagrams[0].participants, steps: drawn.diagrams[0].steps }],
    models: { walkthrough: "claude-opus-5-5", diagrams: "gpt-6.1-sol" },
  }));

  await writeSummary(run, CLEAN, { sections: [], outstanding: [] });

  assert.match(fake.summaries[0].body, /### Walkthrough of `4444444`/);
  assert.doesNotMatch(fake.summaries[0].body, /mermaid/);
});
