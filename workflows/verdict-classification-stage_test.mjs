import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";

import { judgementArgs } from "./judgement-stage.mjs";
import { decisionFrom, stage } from "./judgements/verdict-classification.mjs";
import { classify, filedEntries, groupFindings } from "./run-steps/classify.mjs";
import { operationRoot } from "./temporary-directory-fixture.mjs";
import { DECISION_KIND } from "./verdict-classification.mjs";

const target = "1111111111111111111111111111111111111111";
const head = "2222222222222222222222222222222222222222";
const earlierHead = "3333333333333333333333333333333333333333";
const guidance = [{ repository: null, path: "README.md", origin: "checked-in", content: "A library." }];
const env = { MINOS_PROVISIONED_ENGINES: "claude codex" };

function finding(overrides = {}) {
  return {
    key: "f1",
    severity: "High",
    atOrAboveThreshold: true,
    path: "src/parse.go",
    line: 12,
    title: "Trailing input is accepted",
    explanation: "The parser stops at the first token.",
    verification: "Confirmed: nothing reads past the token.",
    source: "logic",
    ...overrides,
  };
}

function input(overrides = {}) {
  return {
    target,
    head,
    guidance,
    threshold: "High",
    findings: { main: [finding()] },
    earlierFindings: [],
    filedEntries: "No entries are on file.",
    ...overrides,
  };
}

function disposition(overrides = {}) {
  return { key: "f1", gating: true, class: "", defect: "", undergrade: "", ...overrides };
}

test("the prompt shows the threshold, the findings by group and the earlier findings as the stage is told them", () => {
  const earlier = { path: "src/parse.go", line: 9, head: earlierHead, finding: "**Blocking · High: Old**" };
  const { prompt, schema } = judgementArgs(stage, input({
    findings: { main: [finding()], brief: [] },
    earlierFindings: [earlier],
    filedEntries: "- Use the shared parser.",
  }), { env });

  assert.match(prompt, /reaches this repository's threshold, which is `High`/);
  assert.ok(prompt.includes(JSON.stringify({ main: [finding()], brief: [] }, null, 2)));
  assert.ok(prompt.includes(JSON.stringify([earlier], null, 2)));
  assert.match(prompt, /<filed-entries>\n- Use the shared parser\.\n<\/filed-entries>/);
  assert.doesNotMatch(prompt, /\{\{[a-z-]+\}\}/);
  assert.deepEqual(schema.required, ["main", "brief"]);
});

test("a run with no brief verdict is asked for the main decision alone, every field required", () => {
  const { schema } = judgementArgs(stage, input(), { env });

  assert.deepEqual(schema.required, ["main"]);
  assert.deepEqual(Object.keys(schema.properties), ["main"]);
  const item = schema.properties.main.properties.findings.items;
  assert.deepEqual(item.required, ["key", "gating", "class", "defect", "undergrade"]);
  assert.deepEqual(item.properties.class.enum, ["", "declared-out-of-scope", "speculative-hardening", "already-raised"]);
});

test("an input missing what the stage is told is refused, naming the part", () => {
  assert.match(stage.inputProblem(input({ threshold: "Urgent" })), /threshold/);
  assert.match(stage.inputProblem(input({ findings: { brief: [] } })), /findings\.main is required/);
  assert.match(stage.inputProblem(input({ findings: { main: [], extra: [] } })), /findings\.extra is not a group/);
  assert.match(stage.inputProblem(input({ findings: { main: [finding(), finding()] } })), /names f1 twice/);
  assert.match(stage.inputProblem(input({ findings: { main: [finding({ verification: undefined })] } })), /verification/);
  assert.match(stage.inputProblem(input({ earlierFindings: [{ path: "a" }] })), /earlier finding needs/);
  assert.match(stage.inputProblem(input({ filedEntries: "" })), /filedEntries/);
  assert.equal(stage.inputProblem(input()), null);
});

test("an answer becomes a decision with its empty fields dropped", () => {
  assert.deepEqual(decisionFrom({
    verdict: "request-changes",
    basis: "One parser defect gates.",
    findings: [disposition({ defect: "trailing-input" }), disposition({ key: "f2", gating: false, class: "already-raised" })],
  }), {
    kind: DECISION_KIND,
    verdict: "request-changes",
    basis: "One parser defect gates.",
    findings: [
      { key: "f1", gating: true, defect: "trailing-input" },
      { key: "f2", gating: false, class: "already-raised" },
    ],
  });
});

test("an answer is held to the decision validation for every group shown, and for none other", () => {
  const clean = { verdict: "clean", basis: "Nothing found.", findings: [] };
  const gating = { verdict: "request-changes", basis: "The parser accepts trailing input.", findings: [disposition()] };
  const both = input({ findings: { main: [finding()], brief: [] } });

  assert.equal(stage.answerProblem({ main: gating, brief: clean }, both), null);
  assert.match(stage.answerProblem({ main: gating }, both), /no decision for brief/);
  assert.match(stage.answerProblem({ main: gating, brief: clean }, input()), /brief was not shown/);
  assert.match(
    stage.answerProblem({ main: { ...gating, verdict: "clean", findings: [disposition({ gating: false })] } }, input()),
    /the main decision does not hold: an at-or-above-threshold finding may be judged non-gating only as/,
  );
  assert.match(
    stage.answerProblem({ main: { ...gating, findings: [disposition({ class: "already-raised" })] } }, input()),
    /a gating finding carries no never-gating class/,
  );
});

const verdict = {
  status: "complete",
  confirmedFindings: [{
    id: "logic-1",
    severity: "Medium",
    confidence: 80,
    verifierConfidence: 90,
    path: "src/parse.go",
    line: 12,
    title: "Trailing input is accepted",
    explanation: "The parser stops at the first token.",
    verification: "Confirmed.",
    source: "logic",
  }],
};

test("a verdict's findings are shown with their digest facts and the reviewer's and verifier's words, no confidence", () => {
  assert.deepEqual(groupFindings(verdict, "High"), [{
    key: "logic-1",
    severity: "Medium",
    atOrAboveThreshold: false,
    path: "src/parse.go",
    line: 12,
    title: "Trailing input is accepted",
    explanation: "The parser stops at the first token.",
    verification: "Confirmed.",
    source: "logic",
  }]);
  assert.throws(() => groupFindings({ ...verdict, status: "incomplete" }, "High"), /cannot be classified/);
});

function git(cwd, ...args) {
  return execFileSync("git", ["-C", cwd, ...args], {
    encoding: "utf8",
    env: { ...process.env, GIT_CONFIG_GLOBAL: "/dev/null", GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@t", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@t" },
  }).trim();
}

// A run as the step sees it: a clone whose target carries ISSUES.md, an
// orientation naming one guidance document, a forge holding one earlier
// thread and one review with a folded finding, and a stage that answers as
// told.
function fixtureRun(t, { answer, filing = { kind: "file", path: "ISSUES.md" }, otherReviews = [] } = {}) {
  const root = operationRoot(t, "minos-classify-");
  const workspace = join(root, "workspace");
  mkdirSync(workspace, { recursive: true });
  git(workspace, "init", "-q");
  writeFileSync(join(workspace, "README.md"), "A library.\n");
  writeFileSync(join(workspace, "ISSUES.md"), "- Use the shared parser.\n");
  git(workspace, "add", ".");
  git(workspace, "commit", "-q", "-m", "default");
  git(workspace, "branch", "-m", "default");
  git(workspace, "checkout", "-qb", "maintenance");
  writeFileSync(join(workspace, "ISSUES.md"), "- This advice is on the pull request target alone.\n");
  git(workspace, "commit", "-qam", "target");
  const targetCommit = git(workspace, "rev-parse", "HEAD");
  const orientation = join(root, "orientation.json");
  writeFileSync(orientation, JSON.stringify({
    repository: workspace,
    head,
    guidance: [{ source: { path: "README.md" }, location: join(workspace, "README.md"), origin: "checked-in" }],
    misconfigurations: [],
  }));
  const snapshot = {
    head_sha: head,
    authenticated_user: "minos",
    author: "alice",
    reviews: [{
      id: 7,
      user: "minos",
      commit_id: earlierHead,
      submitted_at: "2026-11-20T10:00:00Z",
      body: "**Minos: changes need attention.**\n\n---\n\nFindings that could not be anchored inline:\n\nThese have no thread to resolve.\n\n#### `docs/a.md` line 3\n\n**Blocking · High: Stale claim**\n\nThe page says otherwise.\n\nReviewed by: `claude-opus-5-5`.\n\n<!-- Minos: head=old target=base -->",
    }, ...otherReviews],
  };
  const threads = [{ path: "src/parse.go", line: 9, resolved: false, comments: [{ user: "minos", body: "**Blocking · High: Old**", commit_id: earlierHead }] }];
  const asked = [];
  return {
    asked, snapshot,
    run: {
      env: { ...process.env, MINOS_REVIEW_THRESHOLD: "High", MINOS_FILING_DESTINATION: JSON.stringify(filing) },
      head,
      target: targetCommit,
      runDir: root,
      pullRequest: { owner: "owner", repository: "repo" },
      workspace,
      orientation,
      forge: async (command, ...args) => {
        if (command === "repository-metadata" || command === "clone-credential")
          assert.deepEqual(args, ["owner", filing.repository ? "plans" : "repo"]);
        return {
          snapshot, threads,
          "repository-metadata": { clone_url: workspace, default_branch: "default" },
          "clone-credential": { username: "fixture", password: "fixture" },
        }[command];
      },
      judge: async (judged, given) => {
        asked.push({ judged, given });
        return answer;
      },
    },
  };
}

test("the step asks once for both verdicts with everything gathered, and settles the decisions", async (t) => {
  const answer = {
    main: { verdict: "clean", basis: "Below the threshold and advisory.", findings: [disposition({ key: "logic-1", gating: false })] },
    brief: { verdict: "clean", basis: "No brief findings.", findings: [] },
  };
  const { run, asked } = fixtureRun(t, { answer });

  const settled = await classify(run, { review: verdict, briefReview: { status: "complete", confirmedFindings: [] } });

  assert.equal(asked.length, 1);
  assert.equal(asked[0].judged, stage);
  const given = asked[0].given;
  assert.equal(stage.inputProblem(given), null);
  assert.deepEqual(Object.keys(given.findings), ["main", "brief"]);
  assert.deepEqual(given.earlierFindings, [
    { path: "src/parse.go", line: 9, head: earlierHead, finding: "**Blocking · High: Old**" },
    { path: "docs/a.md", line: 3, head: earlierHead, finding: "**Blocking · High: Stale claim**\n\nThe page says otherwise." },
  ]);
  assert.equal(given.filedEntries, "- Use the shared parser.\n");
  assert.deepEqual(settled.decisions.main, { kind: DECISION_KIND, verdict: "clean", basis: "Below the threshold and advisory.", findings: [{ key: "logic-1", gating: false }] });
  assert.deepEqual(settled.decisions.brief.findings, []);
});

test("a run with no review classifies nothing, and one with no confirmed findings asks no stage", async (t) => {
  const { run, asked } = fixtureRun(t);

  assert.equal(await classify(run, {}), undefined);
  const settled = await classify(run, { review: { status: "complete", confirmedFindings: [] } });

  assert.equal(asked.length, 0);
  assert.deepEqual(settled.decisions, { main: { kind: DECISION_KIND, verdict: "clean", basis: "The review confirmed no findings.", findings: [] } });
});

test("an unread filing destination is distinguished from an empty or missing filing file", async (t) => {
  const { run, snapshot } = fixtureRun(t);
  assert.match(await filedEntries(run, { kind: "issue", repository: "owner/plans" }, snapshot), /could not be read/);
  assert.match(await filedEntries(run, { kind: "pull-request-comment" }, snapshot), /filed no entries/);
  assert.match(await filedEntries(run, { kind: "none" }, snapshot), /files nothing/);
  assert.match(await filedEntries(run, { kind: "file", path: "MISSING.md" }, snapshot), /does not exist on the default branch/);
  const offline = { ...run, forge: async () => { throw new Error("network unavailable"); } };
  assert.match(await filedEntries(offline, { kind: "file", path: "ISSUES.md" }, snapshot), /could not be read/);
});

test("an external file destination is read on its repository's default branch", async (t) => {
  const filing = { kind: "file", repository: "owner/plans", path: "ISSUES.md" };
  const { run, snapshot } = fixtureRun(t, { filing });
  assert.equal(await filedEntries(run, filing, snapshot), "- Use the shared parser.\n");
});

test("classification receives marked filing comments from Minos, including on older heads", async (t) => {
  const marker = "<!-- minos:WyJmaXh0dXJlIl0 -->";
  const filedAdvice = `- Use the shared parser. ${marker}`;
  const answer = {
    main: { verdict: "clean", basis: "This advice was already filed.", findings: [disposition({ key: "logic-1", gating: false, class: "already-raised" })] },
  };
  const { run, asked } = fixtureRun(t, {
    filing: { kind: "pull-request-comment" }, answer,
    otherReviews: [
      { id: 8, user: "minos", commit_id: earlierHead, body: `${filedAdvice}\n\n<!-- Minos: head=old target=base -->` },
      { id: 9, user: "alice", commit_id: head, body: `Unrelated advice. ${marker}` },
      { id: 10, user: "minos", commit_id: head, body: "No blocking finding remains at this head." },
    ],
  });
  const { decisions } = await classify(run, { review: verdict });
  assert.equal(asked[0].given.filedEntries, filedAdvice);
  assert.equal(decisions.main.findings[0].class, "already-raised");
});
