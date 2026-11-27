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
  carry = false, killedFirstReview = false,
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
    repository: workspace, source: { owner: "owner", repo: "repo" },
    guidance: [{ source: { path: "AGENTS.md" }, location: guidance, origin: "checked-in" }],
    misconfigurations: [],
  });
  const setup = fileURLToPath(new URL("../scripts/run-body/setup-workspace", import.meta.url));
  const failureLog = join(runDir, "failure-log");
  const calls = [];
  const drafts = [];
  let snapshots = 0;
  let headMoved = moved;
  const env = {
    ...process.env, MINOS_SETUP_WORKSPACE: setup, MINOS_ORIENTATION: orientation, MINOS_WORKSPACE: workspace,
    MINOS_REVIEW_THRESHOLD: "High", MINOS_PROVISIONED_ENGINES: "claude", MINOS_ROUTING: "{}",
    MINOS_RUN_DIR: runDir, MINOS_HANDOFF: join(runDir, "handoff.json"), MINOS_FAILURE_LOG: failureLog,
    MINOS_FILING_DESTINATION: advisory ? '{"kind":"issue","repository":"owner/issues"}' : '{"kind":"none"}', MINOS_COMMIT_AUTHOR_NAME: "Minos",
    MINOS_COMMIT_AUTHOR_EMAIL: "minos@example.test", MINOS_OWNER: "owner", MINOS_REPO_NAME: "repo",
    MINOS_HEAD_BRANCH: "work", MINOS_PR: "7",
    MINOS_PUSH_INSPECTION_OUTCOMES: '["no-review","increment","full"]',
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
      if (operation === "earlier") return {
        head: briefBase, ancestor: true, outcome: "clean", changed_paths: ["parser.go"],
      };
      if (operation === "threads") {
        const earlierThreads = outstanding ? [{
          path: "lexer.go", line: 5,
          comments: [{ commit_id: run.head, user: "minos", body: "**Blocking** The lexer drops an empty token." }],
        }] : [];
        const postedThreads = drafts.flat().map(({ path, line, body }) => ({
          path, line, comments: [{ commit_id: run.head, user: "minos", body }],
        }));
        return [...earlierThreads, ...postedThreads];
      }
      if (operation === "approve") return { outcome: "not-needed" };
      if (operation === "review") {
        if (movedOnReview) {
          headMoved = true;
          return { outcome: "rejected", reason: "head moved" };
        }
        if (refusedReview) return { outcome: "uncertain", reason: "findings review was not confirmed" };
        drafts.push(JSON.parse(readFileSync(args[4], "utf8")));
        if (pressureOnPublish) writeFileSync(join(runDir, "memory-pressure"), "fixture pressure");
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
          case "earlier-findings":
            assert.deepEqual(args.input.findings.map(({ path, head, replies }) => [path, head, replies]),
              [["lexer.go", run.head, []]]);
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
        ...(main && (blocking || advisory) || !main && briefBlocks
        ? { confirmedFindings: [{ ...blockingFinding, severity: advisory ? "Low" : "High" }] } : {}),
        ...(main && incomplete ? { status: "incomplete", incomplete: ["The verifier did not finish."] } : {}),
      };
      write(`${name}-result.json`, result);
      if (main && pressure) writeFileSync(join(runDir, "memory-pressure"), "fixture pressure");
      return result;
    },
  };
  if (carry) write("carried-review-result.json", carry === "wrong-base"
    ? { ...verdict, reviewed: { ...verdict.reviewed, target: run.head } }
    : verdict);
  return {
    calls, drafts, run, json, failureLog, runDir,
    drive: () => driveRun(run, { complete: true }),
    read: (name) => readFileSync(join(runDir, name), "utf8"),
  };
}

test("the course posts only the validated blocking review and records its non-clean ending", async (t) => {
  const f = fixture(t, { blocking: true, outstanding: true });
  assert.deepEqual(await f.drive(), { outcome: "changes-requested" });
  assert.deepEqual(f.calls.map(([op, ...args]) => op === "status" ? args[2] : op),
    ["snapshot", "claim", "working", "earlier", "judge", "dispatch", "dispatch", "snapshot", "threads",
      "judge", "snapshot", "threads", "judge", "review", "attention", "marker", "marker"]);
  assert.deepEqual(f.calls.find(([op]) => op === "review").slice(1, 4), [f.run.head, target, "request-changes"]);
  assert.match(f.drafts[0][0].body, /Blocking/);
  assert.match(f.read("failure-log"), /cause=this run published blocking findings/);
  assert.equal(f.read("lead-complete"), "non-clean\n");
  assert.match(f.read("report.md"), /Fixture inspection/);
  assert.equal(f.json("verdict-decision.json").verdict, "request-changes");
  assert.equal(f.json("triage-result.json").outcome, "nothing-to-file");
  assert.deepEqual(f.json("judgements/earlier-findings.json").input.findings.map(({ path, head }) => [path, head]),
    [["lexer.go", f.run.head]]);
});

test("a no-review run approves only a clean outcome and never restates earlier findings", async (t) => {
  const clean = fixture(t, { skip: true });
  assert.deepEqual(await clean.drive(), { outcome: "clean" });
  assert.deepEqual(clean.calls.map(([op]) => op), ["snapshot", "claim", "status", "earlier", "judge", "snapshot", "threads", "approve", "status", "marker", "marker"]);
  assert.deepEqual(clean.calls.slice(-3), [
    ["status", clean.run.head, target, "clean"], ["marker", clean.run.head, target, "clean", "add"],
    ["marker", clean.run.head, target, "in-flight", "remove"],
  ]);
  assert.equal(clean.read("lead-complete"), "clean\n");
  assert.equal(existsSync(clean.failureLog), false);

  const blocked = fixture(t, { skip: true, outstanding: true });
  assert.deepEqual(await blocked.drive(), { outcome: "changes-requested" });
  assert.deepEqual(blocked.calls.map(([op]) => op), ["snapshot", "claim", "status", "earlier", "judge", "snapshot", "threads", "judge", "status", "marker", "marker"]);
  assert.match(blocked.read("failure-log"), /earlier review remain outstanding/);
  assert.match(blocked.read("report.md"), /Fixture defect still stands/);
  const settlement = blocked.json("judgements/earlier-findings.json");
  assert.equal(settlement.input.findings[0].head, blocked.run.head);
  assert.equal(settlement.answer.findings[0].outcome, "stands");
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
  assert.equal(f.json("verdict-decision.json").verdict, "clean");
  assert.equal(f.json("verdict-decision-brief.json").verdict, "request-changes");
  assert.equal(existsSync(join(f.runDir, "judgements/earlier-findings.json")), false);
});

test("a moved head observed during classification, settlement or filing stops an advisory-only run", async (t) => {
  for (const [movedAtSnapshot, step] of [[2, "classify"], [3, "settleEarlierFindings"], [4, "publish"]]) {
    const f = fixture(t, { advisory: true, movedAtSnapshot });
    const ending = await f.drive();
    assert.equal(ending.outcome, "superseded");
    assert.equal(ending.step, step);
    assert.equal(existsSync(join(f.runDir, "triage-result.json")), false);
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
  assert.deepEqual(f.calls.slice(-2), [["snapshot"], ["marker", head, reviewBase, "in-flight", "remove"]]);
});

test("a missing terminal reaction is reported after a durable blocking review", async (t) => {
  const f = fixture(t, { blocking: true, refusedMarker: true });
  assert.deepEqual(await f.drive(), { outcome: "changes-requested" });
  assert.equal(f.drafts.length, 1);
  assert.match(f.read("report.md"), /reaction write failed/);
  assert.equal(f.calls.some(([op, , , state]) => op === "status" && state === "incomplete"), false);
});

test("a memory handoff records durable progress and keeps in-flight for the successor", async (t) => {
  const f = fixture(t, { pressure: true });
  assert.deepEqual(await f.drive(), { outcome: "continuation", step: "review", cause: "memory pressure" });
  assert.deepEqual(f.calls.slice(-2), [["snapshot"], ["status", f.run.head, reviewBase, "continuation"]]);
  assert.deepEqual(f.json("handoff.json").progress, { stage: "review", head: f.run.head, latestReview: 7 });
  assert.equal(f.read("lead-complete"), "continuation\n");
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
