import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { driveRun, judge } from "./run-driver.mjs";
import { operationRoot } from "./temporary-directory-fixture.mjs";

const head = "2".repeat(40);
const target = "1".repeat(40);
const reviewBase = "3".repeat(40);
const blockingFinding = {
  id: "block",
  source: "Correctness",
  title: "The parser rejects a valid record",
  severity: "High",
  confidence: 91,
  verifierConfidence: 92,
  path: "parser.go",
  line: 10,
  explanation: "The empty name is valid.",
};
function decision(verdict, findings = []) {
  return { verdict, basis: "Fixture decision.", findings };
}

function findingDecision(blocking, advisory = false) {
  const findings = blocking || advisory
    ? [{ key: "block", gating: blocking, class: "", defect: "", undergrade: "" }]
    : [];
  return decision(blocking ? "request-changes" : "clean", findings);
}

function fixture(t, {
  skip = false, outstanding = false, blocking = false, briefBlocks = false, advisory = false,
  refusedReview = false, refusedMarker = false, failedSettlement = false, incomplete = false,
  pressure = false, pressureOnPublish = false, moved = false, movedAtSnapshot, movedOnReview = false,
  carry = false, killedFirstReview = false, refusedSummary, refusedStatus, movedOnSummary, movedOnMarker,
  reviewMode, modeAnswer,
} = {}) {
  const runDir = operationRoot(t, "minos-course-");
  const workspace = join(runDir, "workspace");
  const orientation = join(runDir, "orientation.json");
  const guidance = join(workspace, "AGENTS.md");
  const json = (name) => JSON.parse(readFileSync(join(runDir, name), "utf8"));
  const write = (name, data) => writeFileSync(join(runDir, name), JSON.stringify(data));
  const git = (...args) => execFileSync("git", ["-C", workspace, ...args], { encoding: "utf8" });
  mkdirSync(workspace);
  git("init", "-q");
  writeFileSync(guidance, "A parser library.\n");
  git("add", ".");
  git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "Fixture");
  const briefBase = git("rev-parse", "HEAD").trim();
  writeFileSync(join(workspace, "parser.go"), "package parser\n");
  git("add", ".");
  git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "Author change");
  const reviewedHead = git("rev-parse", "HEAD").trim();
  const verdict = {
    status: "complete", complete: true, reviewed: { head: reviewedHead, target: briefBase },
    incomplete: [], confirmedFindings: [], ran: [], skipped: [],
  };
  write("orientation.json", {
    repository: workspace, source: { owner: "owner", repo: "repo", pr: "7", date: "2026-11-27" },
    guidance: [{ source: { path: "AGENTS.md" }, location: guidance, origin: "checked-in" }],
    misconfigurations: [],
  });
  const filingAttempts = join(runDir, "filing-attempts");
  const minos = join(runDir, "minos");
  writeFileSync(minos, `#!/bin/sh
[ "$1:$2:$3" = "forge:file-issue:owner/issues" ] || exit 2
printf '%s\\n' 'attempted' >> "$MINOS_RUN_DIR/filing-attempts"
printf '%s\\n' '{"outcome":"applied","written":1}'
`, { mode: 0o700 });
  const setup = fileURLToPath(new URL("../scripts/run-body/setup-workspace", import.meta.url));
  const failureLog = join(runDir, "failure-log");
  const calls = [];
  const drafts = [];
  const summaries = [];
  let snapshots = 0;
  let headMoved = moved;
  const env = {
    ...process.env, MINOS_BIN: minos, MINOS_SETUP_WORKSPACE: setup, MINOS_ORIENTATION: orientation, MINOS_WORKSPACE: workspace,
    MINOS_REVIEW_THRESHOLD: "High", MINOS_PROVISIONED_ENGINES: "claude", MINOS_ROUTING: "{}",
    MINOS_RUN_DIR: runDir, MINOS_HANDOFF: join(runDir, "handoff.json"), MINOS_FAILURE_LOG: failureLog,
    MINOS_FILING_DESTINATION: advisory ? '{"kind":"issue","repository":"owner/issues"}' : '{"kind":"none"}', MINOS_COMMIT_AUTHOR_NAME: "Minos",
    MINOS_COMMIT_AUTHOR_EMAIL: "minos@example.test", MINOS_OWNER: "owner", MINOS_REPO_NAME: "repo",
    MINOS_HEAD_BRANCH: "work", MINOS_PR: "7",
    MINOS_PUSH_INSPECTION_OUTCOMES: '["no-review","increment","full"]',
    ...(reviewMode === undefined ? {} : { MINOS_REVIEW_MODE: reviewMode }),
  };
  // Give the input builders real Git coordinates and leave a distinct
  // target pinned for all forge operations.
  const run = {
    head: reviewedHead, target, runDir, workspace, orientation, env,
    pullRequest: { owner: "owner", repository: "repo", number: "7" },
    judge: (stage, input) => judge(run, stage, input),
    judgementAnswers: new Map(),
    async forge(operation, ...args) {
      calls.push([operation, ...args.slice(0, 4)]);
      if (operation === "snapshot") {
        if (++snapshots === movedAtSnapshot) headMoved = true;
        return {
          head_sha: headMoved ? head : run.head, target_sha: reviewBase, authenticated_user: "minos",
          reviews: [{ id: 7, user: "minos", commit_id: run.head }, { id: 12, user: "author", commit_id: run.head }],
        };
      }
      if (operation === "filed-issues") return [];
      if (operation === "earlier") return {
        head: briefBase, ancestor: true, outcome: "clean", changed_paths: ["parser.go"],
      };
      if (operation === "threads") {
        const earlierThreads = outstanding ? [{
          path: "lexer.go", line: 5,
          comments: [{ commit_id: head, user: "minos", body: "**Blocking · High: The lexer drops an empty token**\n\nAn empty token is valid." }],
        }] : [];
        const postedThreads = drafts.flat().map(({ path, line, body }) => ({
          path, line, comments: [{ commit_id: run.head, user: "minos", body }],
        }));
        return [...earlierThreads, ...postedThreads];
      }
      if (operation === "approve") return { outcome: "not-needed" };
      if (operation === "summary") {
        const [summaryHead, summaryTarget, state, text] = args;
        assert.equal(summaryHead, run.head);
        assert.equal(summaryTarget, run.target);
        if (state === movedOnSummary) {
          headMoved = true;
          return { outcome: "rejected", reason: "head moved" };
        }
        if (state === refusedSummary)
          return { outcome: "uncertain", reason: "summary write could not be discovered" };
        summaries.push({ state, body: readFileSync(text, "utf8") });
      }
      if (operation === "status" && args[2] === refusedStatus)
        return { outcome: "uncertain", reason: "status write could not be confirmed" };
      if (operation === "review") {
        if (movedOnReview) {
          headMoved = true;
          return { outcome: "rejected", reason: "head moved" };
        }
        if (refusedReview) return { outcome: "uncertain", reason: "findings review was not confirmed" };
        drafts.push(JSON.parse(readFileSync(args[4], "utf8")));
        if (pressureOnPublish) writeFileSync(join(runDir, "memory-pressure"), "fixture pressure");
      }
      if (operation === "marker" && args[2] === movedOnMarker && args[3] === "add") {
        headMoved = true;
        return { outcome: "rejected", reason: "head moved" };
      }
      if (operation === "marker" && args[2] === "attention" && args[3] === "add" && refusedMarker)
        return { outcome: "uncertain", reason: "reaction write failed" };
      if (operation === "marker" && args[3] === "remove" && (blocking || incomplete || headMoved || outstanding)) {
        assert.ok(existsSync(failureLog), "the failure was recorded before removing in-flight");
      }
      return { outcome: "applied" };
    },
    async dispatch(name, workflow, runner) {
      const args = json(`${name}-args.json`);
      if (workflow === "judgement.js") {
        assert.equal(runner, "run-judgement");
        calls.push(["judge", name]);
        let answer;
        switch (args.stage) {
          case "push-inspection":
            answer = { outcome: skip ? "no-review" : "increment", reason: "Fixture inspection." };
            break;
          case "verdict-classification":
            assert.deepEqual(args.input.findings.main.map(({ key }) => key), blocking || advisory ? ["block"] : []);
            assert.deepEqual(args.input.findings.brief.map(({ key }) => key), briefBlocks ? ["block"] : []);
            answer = {
              main: findingDecision(blocking, advisory),
              brief: findingDecision(briefBlocks),
            };
            break;
          case "review-mode":
            assert.equal(args.input.target, briefBase);
            assert.equal(args.input.head, run.head);
            if (modeAnswer instanceof Error) throw modeAnswer;
            answer = modeAnswer;
            break;
          case "earlier-findings":
            assert.deepEqual(args.input.findings.map(({ path, head, replies }) => [path, head, replies]),
              [["lexer.go", head, []]]);
            if (failedSettlement) throw new Error("Earlier findings did not finish.");
            answer = { findings: [{ key: "thread-1", outcome: "stands", reason: "Fixture defect still stands." }] };
            break;
          default:
            assert.fail(`unexpected judgement stage ${args.stage}`);
        }
        return { stage: args.stage, answer };
      }
      calls.push(["dispatch", name, workflow]);
      const input = args;
      assert.equal(input.target, briefBase);
      assert.equal(input.head, run.head);
      assert.equal(input.guidance[0].content, "A parser library.\n");
      if (name === "review" && killedFirstReview) {
        throw Object.assign(new Error("review ended on SIGKILL"), { killed: true });
      }
      const main = name.split("@")[0] === "review";
      const result = {
        ...verdict,
        summarySections: main
          ? [{ kind: "concern", name: "logic", findings: blocking || advisory ? 1 : 0 }]
          : [
            { kind: "brief", name: ".review/parser.md", findings: briefBlocks ? 1 : 0 },
            { kind: "brief", name: ".review/lexer.md", skipped: "no lexing change" },
          ],
        ...(main && (blocking || advisory) || !main && briefBlocks
        ? { confirmedFindings: [{ ...blockingFinding, severity: advisory ? "Low" : "High" }] } : {}),
        ...(main && incomplete ? { status: "incomplete", incomplete: ["The verifier did not finish."] } : {}),
      };
      write(`${name}-result.json`, result);
      if (main && pressure) writeFileSync(join(runDir, "memory-pressure"), "fixture pressure");
      return result;
    },
  };
  // A carried verdict names its mode, as an adjudicated one does.
  if (carry) write("carried-review-result.json", carry === "wrong-base"
    ? { ...verdict, mode: "specialist", reviewed: { ...verdict.reviewed, target: run.head } }
    : { ...verdict, mode: "quick" });
  return {
    calls, drafts, summaries, run, json, failureLog, runDir, filingAttempts,
    drive: () => driveRun(run, { complete: true }),
    read: (name) => readFileSync(join(runDir, name), "utf8"),
  };
}

test("the course posts only the validated blocking review and records its non-clean ending", async (t) => {
  const f = fixture(t, { blocking: true, outstanding: true });
  assert.deepEqual(await f.drive(), { outcome: "changes-requested" });
  assert.deepEqual(f.calls.map(([op, ...args]) => op === "status" ? args[2] : op),
    ["snapshot", "claim", "summary", "working", "earlier", "judge", "summary", "dispatch", "dispatch", "snapshot", "threads",
      "judge", "snapshot", "threads", "judge", "review", "summary", "attention", "marker", "marker"]);
  assert.deepEqual(f.calls.find(([op]) => op === "review").slice(1, 4), [f.run.head, target, "request-changes"]);
  assert.match(f.drafts[0][0].body, /Blocking/);
  assert.match(f.read("failure-log"), /cause=this run published blocking findings/);
  assert.equal(f.read("lead-complete"), "non-clean\n");
  assert.match(f.read("report.md"), /Fixture inspection/);
  assert.equal(f.json("verdict-decision.json").verdict, "request-changes");
  assert.equal(f.json("triage-result.json").outcome, "nothing-to-file");
  assert.deepEqual(f.json("judgements/earlier-findings.json").input.findings.map(({ path, head }) => [path, head]),
    [["lexer.go", head]]);
});

test("the claim makes the summary comment say the head is being inspected", async (t) => {
  const f = fixture(t, { skip: true });
  assert.deepEqual(await f.drive(), { outcome: "clean" });
  assert.deepEqual(f.calls.find(([op]) => op === "summary"),
    ["summary", f.run.head, target, "inspecting", join(f.runDir, "summary-comment.md")]);
  assert.deepEqual(f.summaries.map(({ state }) => state), ["inspecting", "clean"]);
  assert.equal(f.summaries[0].body,
    `**Minos: looking at what changed.**\n\nHead \`${f.run.head.slice(0, 7)}\` against target \`${target.slice(0, 7)}\`.\n`);
});

test("a summary comment the forge did not take at the claim is reported and the course goes on", async (t) => {
  const f = fixture(t, { skip: true, refusedSummary: "inspecting" });
  assert.deepEqual(await f.drive(), { outcome: "clean" });
  assert.match(f.read("report.md"), /forge summary: summary write could not be discovered/);
});

test("a no-review run approves only a clean outcome and never restates earlier findings", async (t) => {
  const clean = fixture(t, { skip: true });
  assert.deepEqual(await clean.drive(), { outcome: "clean" });
  assert.deepEqual(clean.calls.map(([op]) => op), ["snapshot", "claim", "summary", "status", "earlier", "judge", "snapshot", "threads", "approve", "summary", "status", "marker", "marker"]);
  assert.deepEqual(clean.calls.slice(-3), [
    ["status", clean.run.head, target, "clean"], ["marker", clean.run.head, target, "clean", "add"],
    ["marker", clean.run.head, target, "in-flight", "remove"],
  ]);
  assert.equal(clean.read("lead-complete"), "clean\n");
  assert.equal(existsSync(clean.failureLog), false);

  const blocked = fixture(t, { skip: true, outstanding: true });
  assert.deepEqual(await blocked.drive(), { outcome: "changes-requested" });
  assert.deepEqual(blocked.calls.map(([op]) => op), ["snapshot", "claim", "summary", "status", "earlier", "judge", "snapshot", "threads", "judge", "summary", "status", "marker", "marker"]);
  assert.match(blocked.read("failure-log"), /earlier review remain outstanding/);
  assert.match(blocked.read("report.md"), /Fixture defect still stands/);
  const settlement = blocked.json("judgements/earlier-findings.json");
  assert.equal(settlement.input.findings[0].head, head);
  assert.equal(settlement.answer.findings[0].outcome, "stands");
  assert.deepEqual(blocked.summaries.map(({ state }) => state), ["inspecting", "changes-requested"]);
  assert.match(blocked.summaries.at(-1).body,
    /The lexer drops an empty token.*lexer\.go:5.*raised at `2222222`/);
  assert.doesNotMatch(blocked.summaries.at(-1).body, /Section/);
});

test("an incomplete review or unconfirmed write withdraws the run without a clean marker", async (t) => {
  for (const options of [{ incomplete: true }, { blocking: true, refusedReview: true }]) {
    const f = fixture(t, options);
    const ending = await f.drive();
    assert.equal(ending.outcome, "incomplete");
    assert.match(ending.cause, /did not finish|not confirmed/);
    assert.deepEqual(f.calls.slice(-2), [
      ["status", f.run.head, target, "incomplete"], ["marker", f.run.head, target, "in-flight", "remove"],
    ]);
    assert.equal(f.read("failure-log").trim().split("\n").length, 1);
    assert.deepEqual(f.summaries.map(({ state }) => state), ["inspecting", "reviewing", "incomplete"]);
    assert.match(f.summaries.at(-1).body, options.incomplete
      ? /The specialist review did not complete/
      : /Minos could not confirm publication of the findings/);
  }
});

test("a moved head withdraws against the fresh coordinates without setting a status", async (t) => {
  const f = fixture(t, { moved: true });
  assert.equal((await f.drive()).outcome, "superseded");
  assert.deepEqual(f.calls, [["snapshot"], ["snapshot"], ["marker", head, reviewBase, "in-flight", "remove"]]);
  assert.match(f.read("failure-log"), /head moved/);
});

test("settlement failure leaves a confirmed new blocker unpublished", async (t) => {
  const f = fixture(t, { blocking: true, outstanding: true, failedSettlement: true });
  assert.equal((await f.drive()).outcome, "incomplete");
  assert.equal(f.calls.some(([op]) => op === "review" || op === "approve"), false);
  assert.equal(existsSync(join(f.runDir, "triage-result.json")), false);
  assert.deepEqual(f.calls.slice(-2), [
    ["status", f.run.head, target, "incomplete"], ["marker", f.run.head, target, "in-flight", "remove"],
  ]);
});

test("a brief's blocker alone publishes a request for changes", async (t) => {
  const f = fixture(t, { briefBlocks: true });
  assert.deepEqual(await f.drive(), { outcome: "changes-requested" });
  assert.deepEqual(f.calls.find(([op]) => op === "review").slice(1, 4), [f.run.head, target, "request-changes"]);
  assert.match(f.drafts[0][0].body, /The parser rejects a valid record/);
  assert.deepEqual(f.summaries.map(({ state }) => state), ["inspecting", "reviewing", "changes-requested"]);
  assert.match(f.summaries.at(-1).body, /\| logic \| none \|/);
  assert.match(f.summaries.at(-1).body, /Brief `\.review\/parser\.md` \| 1/);
  assert.match(f.summaries.at(-1).body, /Brief `\.review\/lexer\.md` \| skipped: no lexing change/);
  assert.equal(f.json("verdict-decision.json").verdict, "clean");
  assert.equal(f.json("verdict-decision-brief.json").verdict, "request-changes");
  assert.equal(existsSync(join(f.runDir, "judgements/earlier-findings.json")), false);
});

test("a current advisory-only run files its finding", async (t) => {
  const f = fixture(t, { advisory: true });
  assert.deepEqual(await f.drive(), { outcome: "clean" });
  assert.equal(f.read("filing-attempts"), "attempted\n");
  assert.equal(f.json("triage-result.json").written, 1);
});

test("a moved head observed during classification, settlement or filing stops an advisory-only run", async (t) => {
  for (const [movedAtSnapshot, step] of [[2, "classify"], [3, "settleEarlierFindings"], [4, "publish"]]) {
    const f = fixture(t, { advisory: true, movedAtSnapshot });
    const ending = await f.drive();
    assert.equal(ending.outcome, "superseded");
    assert.equal(ending.step, step);
    assert.equal(existsSync(join(f.runDir, "triage-result.json")), false);
    assert.equal(existsSync(f.filingAttempts), false, "no filing was attempted for the moved head");
    assert.equal(f.calls.some(([op]) => op === "review" || op === "approve"), false);
    assert.deepEqual(f.calls.at(-1), ["marker", head, reviewBase, "in-flight", "remove"]);
  }
});

test("a guarded publication can refuse a head that moved after settlement", async (t) => {
  const f = fixture(t, { blocking: true, movedOnReview: true });
  const ending = await f.drive();
  assert.equal(ending.outcome, "superseded");
  assert.equal(ending.step, "publish");
  assert.equal(f.drafts.length, 0);
  assert.deepEqual(f.summaries.map(({ state }) => state), ["inspecting", "reviewing"]);
  assert.deepEqual(f.calls.slice(-2), [["snapshot"], ["marker", head, reviewBase, "in-flight", "remove"]]);
});

test("a missing terminal reaction is reported after a durable blocking review", async (t) => {
  const f = fixture(t, { blocking: true, refusedMarker: true });
  assert.deepEqual(await f.drive(), { outcome: "changes-requested" });
  assert.equal(f.drafts.length, 1);
  assert.match(f.read("report.md"), /reaction write failed/);
  assert.equal(f.calls.some(([op, , , state]) => op === "status" && state === "incomplete"), false);
});

test("a terminal marker refused after a head move withdraws the superseded run", async (t) => {
  const f = fixture(t, { blocking: true, movedOnMarker: "attention" });
  assert.deepEqual(await f.drive(), { outcome: "superseded", step: "conclude", cause: "forge marker: head moved" });
  assert.equal(f.drafts.length, 1);
  assert.deepEqual(f.calls.slice(-3), [
    ["marker", f.run.head, target, "attention", "add"],
    ["snapshot"],
    ["marker", head, reviewBase, "in-flight", "remove"],
  ]);
  assert.equal(f.read("lead-complete"), "non-clean\n");
});

test("a memory handoff records durable progress and keeps in-flight for the successor", async (t) => {
  const f = fixture(t, { pressure: true });
  const predecessorProgress = { stage: "review", head: f.run.head, latestReview: 6 };
  f.run.env.MINOS_PREDECESSOR_PROGRESS = JSON.stringify(predecessorProgress);
  assert.deepEqual(await f.drive(), { outcome: "continuation", step: "review", cause: "memory pressure" });
  assert.deepEqual(f.calls.slice(-2), [["snapshot"], ["status", f.run.head, reviewBase, "continuation"]]);
  const { writtenAt, ...handoff } = f.json("handoff.json");
  assert.ok(Number.isFinite(Date.parse(writtenAt)));
  assert.deepEqual(handoff, {
    kind: "minos-run-handoff-v1",
    pullRequest: { owner: "owner", repo: "repo", number: "7" },
    head: f.run.head,
    runDir: f.runDir,
    stoppedAt: "review",
    predecessorProgress,
    progress: { stage: "review", head: f.run.head, latestReview: 7 },
  });
  assert.equal(f.read("lead-complete"), "continuation\n");
  assert.deepEqual(f.summaries.map(({ state }) => state), ["inspecting", "reviewing"]);
  assert.equal(existsSync(f.failureLog), false);
});

test("a published blocking review finishes terminal work under memory pressure", async (t) => {
  const f = fixture(t, { blocking: true, pressureOnPublish: true });
  assert.deepEqual(await f.drive(), { outcome: "changes-requested" });
  assert.equal(f.drafts.length, 1);
  assert.deepEqual(f.calls.slice(-3), [
    ["status", f.run.head, target, "attention"],
    ["marker", f.run.head, target, "attention", "add"],
    ["marker", f.run.head, target, "in-flight", "remove"],
  ]);
  assert.equal(f.read("lead-complete"), "non-clean\n");
  assert.equal(existsSync(join(f.runDir, "handoff.json")), false);
});

test("a matching carried main verdict goes through classification without re-dispatch", async (t) => {
  const f = fixture(t, { carry: true });
  assert.deepEqual(await f.drive(), { outcome: "clean" });
  assert.deepEqual(f.calls.filter(([op]) => op === "dispatch"), [["dispatch", "review-brief", "review-briefs.js"]]);
  assert.equal(existsSync(join(f.runDir, "carried-review-result.json")), false);
  assert.match(f.read("report.md"), /Reused the predecessor/);
});

test("an earlier verdict for the same head but another base is dispatched again", async (t) => {
  const f = fixture(t, { carry: "wrong-base" });
  assert.deepEqual(await f.drive(), { outcome: "clean" });
  assert.equal(f.json("stale-review-result.json").reviewed.target, f.run.head);
  assert.deepEqual(f.calls.filter(([op]) => op === "dispatch").map(([, name]) => name), ["review", "review-brief"]);
  assert.notEqual(f.json("review-args.json").target, f.run.head);
});

// Each mode followed from the setting through the course to the input its
// builder made and the workflow dispatched: the specialist review's input
// carries its concerns and the exploration prompt, the quick review's
// neither.
function mainReview(f) {
  const [, name, workflow] = f.calls.find(([op, name]) => op === "dispatch" && name === "review");
  const args = f.json(`${name}-args.json`);
  return { workflow, concerns: args.concerns !== undefined, exploration: args.prompts.exploration !== undefined };
}

test("each review mode the setting names reaches its own input builder and workflow, and the report names it", async (t) => {
  for (const [setting, expected] of [
    [undefined, { workflow: "review.js", concerns: true, exploration: true, mode: "specialist" }],
    ["specialist", { workflow: "review.js", concerns: true, exploration: true, mode: "specialist" }],
    ["quick", { workflow: "quick-review.js", concerns: false, exploration: false, mode: "quick" }],
  ]) {
    const f = fixture(t, { reviewMode: setting });
    assert.deepEqual(await f.drive(), { outcome: "clean" });
    const { mode, ...review } = expected;
    assert.deepEqual(mainReview(f), review, `review.mode ${setting}`);
    assert.ok(f.read("report.md").includes(`- Main review in the **${mode}** mode.`));
    assert.ok(!f.calls.some(([op, name]) => op === "judge" && name === "review-mode"), "no review-mode stage is asked");
  }
});

test("under automatic the review-mode stage's choice is the review dispatched, with its reason in the report", async (t) => {
  for (const mode of ["quick", "specialist"]) {
    const f = fixture(t, { reviewMode: "automatic", modeAnswer: { mode, reason: `Fixture chose ${mode}.` } });
    assert.deepEqual(await f.drive(), { outcome: "clean" });
    assert.equal(mainReview(f).workflow, mode === "quick" ? "quick-review.js" : "review.js");
    assert.deepEqual(f.calls.filter(([op]) => op === "judge").map(([, name]) => name).slice(0, 2), ["push-inspection", "review-mode"]);
    assert.equal(f.json("judgements/review-mode.json").answer.mode, mode);
    const report = f.read("report.md");
    assert.ok(report.includes(`- Main review in the **${mode}** mode.`));
    assert.ok(report.includes(`the review-mode stage chose it. Fixture chose ${mode}.`), report);
  }
});

test("under automatic a stage with no usable answer gives the head the specialist review and reports why", async (t) => {
  const f = fixture(t, { reviewMode: "automatic", modeAnswer: { mode: "quick", reason: " " } });
  assert.deepEqual(await f.drive(), { outcome: "clean" });
  assert.equal(mainReview(f).workflow, "review.js");
  const report = f.read("report.md");
  assert.match(report, /no choice was made; the specialist review stood in/);
  assert.match(report, /## Anomalies[\s\S]*review-mode stage gave no usable answer[\s\S]*reason is blank/);
});

test("under automatic a carried main verdict is reused without asking the review-mode stage", async (t) => {
  const f = fixture(t, { reviewMode: "automatic", carry: true, modeAnswer: new Error("the stage was asked") });
  assert.deepEqual(await f.drive(), { outcome: "clean" });
  assert.ok(!f.calls.some(([op, name]) => op === "judge" && name === "review-mode"));
  assert.match(f.read("report.md"), /the reused review settled it, and no choice was asked for/);
});

test("a killed dispatch retries once and keeps both attempts' input", async (t) => {
  const f = fixture(t, { killedFirstReview: true });
  assert.deepEqual(await f.drive(), { outcome: "clean" });
  assert.deepEqual(f.calls.filter(([op]) => op === "dispatch").map(([, name]) => name), [
    "review", "review@2", "review-brief",
  ]);
  assert.deepEqual(f.json("review-args.json"), f.json("review@2-args.json"));
  assert.deepEqual(f.json("review-result.json"), f.json("review@2-result.json"));
  assert.match(f.read("report.md"), /review ended on SIGKILL/);
});


test("a clean summary completes the head when the optional write of its status fails", async (t) => {
  const f = fixture(t, { skip: true, refusedStatus: "clean" });
  assert.deepEqual(await f.drive(), { outcome: "clean" });
  assert.deepEqual(f.summaries.map(({ state }) => state), ["inspecting", "clean"]);
  assert.match(f.read("report.md"), /status write could not be confirmed/);
});

test("an unconfirmed terminal summary leaves the run incomplete before writing a clean status", async (t) => {
  const f = fixture(t, { skip: true, refusedSummary: "clean" });
  const ending = await f.drive();
  assert.equal(ending.outcome, "incomplete");
  assert.equal(ending.step, "conclude");
  assert.deepEqual(f.summaries.map(({ state }) => state), ["inspecting", "incomplete"]);
  assert.match(f.summaries.at(-1).body, /could not confirm the final result/);
  assert.doesNotMatch(f.summaries.at(-1).body, /summary write could not be discovered/);
  assert.match(f.read("report.md"), /summary write could not be discovered/);
  assert.equal(f.calls.some(([op, , , state]) => op === "status" && state === "clean"), false);
});

test("a head move that the terminal summary write discovers leaves no later summary", async (t) => {
  const f = fixture(t, { skip: true, movedOnSummary: "clean" });
  assert.equal((await f.drive()).outcome, "superseded");
  assert.deepEqual(f.summaries.map(({ state }) => state), ["inspecting"]);
  assert.deepEqual(f.calls.slice(-2), [["snapshot"], ["marker", head, reviewBase, "in-flight", "remove"]]);
});

test("a failed reviewing summary is recorded even when the specialist review then fails", async (t) => {
  const f = fixture(t, { incomplete: true, refusedSummary: "reviewing" });
  assert.equal((await f.drive()).outcome, "incomplete");
  assert.deepEqual(f.summaries.map(({ state }) => state), ["inspecting", "incomplete"]);
  assert.match(f.read("report.md"), /summary write could not be discovered/);
});

test("a failed incomplete summary does not prevent withdrawal", async (t) => {
  const f = fixture(t, { incomplete: true, refusedSummary: "incomplete" });
  assert.equal((await f.drive()).outcome, "incomplete");
  assert.deepEqual(f.summaries.map(({ state }) => state), ["inspecting", "reviewing"]);
  assert.deepEqual(f.calls.at(-1), ["marker", f.run.head, target, "in-flight", "remove"]);
  assert.match(f.read("report.md"), /summary write could not be discovered/);
});
