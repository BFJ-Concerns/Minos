import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { answerFrom, judgementArgs } from "./judgement-stage.mjs";
import { stage as pushInspection } from "./judgements/push-inspection.mjs";
import { stage } from "./judgements/repository-description.mjs";
import {
  MAXIMUM_REPOSITORY_DESCRIPTION_CHARACTERS,
  RECORD_KIND,
  descriptionFromOrientation,
  descriptionFromRecord,
  repositoryDescriptionFromInput,
  repositoryDescriptionSection,
} from "./repository-description.mjs";
import { classify } from "./run-steps/classify.mjs";
import { inspectPush } from "./run-steps/inspect-push.mjs";
import { invokeWorkflow, loadWorkflow } from "./workflow-invocation-fixture.mjs";
import { operationRoot } from "./temporary-directory-fixture.mjs";

const commit = "1111111111111111111111111111111111111111";
const earlierCommit = "2222222222222222222222222222222222222222";
const description = "A parsing library.\n\n## Layout\n\n- `src/`: the parser.";
const env = { MINOS_PROVISIONED_ENGINES: "claude codex" };

function keptRecord(t, record) {
  const path = join(operationRoot(t, "minos-description-"), "repository-description.json");
  writeFileSync(path, typeof record === "string" ? record : JSON.stringify(record));
  return path;
}

test("the stage is asked on the description role, about the commit it was given", () => {
  const args = judgementArgs(stage, { commit, earlier: null }, { env: { ...env, MINOS_ROUTING: JSON.stringify({ description: { effort: "high" } }) } });

  assert.deepEqual(args.routing, { engine: "claude", model: "claude-opus-5-5", effort: "high" });
  assert.match(args.prompt, new RegExp(`checked out at \`${commit}\``));
  assert.match(args.prompt, new RegExp(`more than ${MAXIMUM_REPOSITORY_DESCRIPTION_CHARACTERS} characters is refused`));
});

test("a repository with no description yet is shown none, and one with a description is shown it under its commit", () => {
  const first = judgementArgs(stage, { commit, earlier: null }, { env });
  assert.match(first.prompt, /\n<earlier-description>none<\/earlier-description>\n/);

  const later = judgementArgs(stage, { commit, earlier: { commit: earlierCommit, description } }, { env });
  assert.ok(later.prompt.includes(`\n<earlier-description commit="${earlierCommit}">\n${description}\n</earlier-description>\n`));
});

test("an input without a full commit, or with an earlier description that is not whole, is refused before dispatch", () => {
  for (const input of [
    { commit: "main", earlier: null },
    { commit },
    { commit, earlier: { commit: earlierCommit, description: " " } },
    { commit, earlier: { commit: "abc", description } },
  ]) assert.throws(() => judgementArgs(stage, input, { env }), /repository-description input:/);
});

test("a blank description, or one past the bound, is no answer", () => {
  const input = { commit, earlier: null };
  const answered = (text) => answerFrom(stage, input, { stage: stage.name, answer: { description: text } });

  assert.deepEqual(answered(description), { description });
  assert.throws(() => answered(" \n"), /the description is blank/);
  assert.throws(() => answered("x".repeat(MAXIMUM_REPOSITORY_DESCRIPTION_CHARACTERS + 1)), /longer than/);
});

test("a record file gives its description and commit, no file gives none, and a file that is not a whole record throws", (t) => {
  const kept = keptRecord(t, { kind: RECORD_KIND, commit, description, written_at: "2026-11-27T10:00:00Z" });
  assert.deepEqual(descriptionFromRecord(kept), { commit, description });
  assert.equal(descriptionFromRecord(join(operationRoot(t, "minos-description-"), "absent.json")), undefined);

  for (const record of ["not json", { kind: "something-else", commit, description }, { kind: RECORD_KIND, commit, description: "" }, { kind: RECORD_KIND, description }])
    assert.throws(() => descriptionFromRecord(keptRecord(t, record)), /repository description record/);
});

test("an orientation that names no record gives none, and one that names a missing record fails the build", (t) => {
  assert.equal(descriptionFromOrientation({}), undefined);
  assert.deepEqual(
    descriptionFromOrientation({ repositoryDescription: keptRecord(t, { kind: RECORD_KIND, commit, description }) }),
    { commit, description },
  );
  assert.throws(
    () => descriptionFromOrientation({ repositoryDescription: join(operationRoot(t, "minos-description-"), "absent.json") }),
    /names a missing repository-description record/,
  );
});

test("a prompt that carries the reviewed change shows the attached description under its commit, and nothing where none is attached", () => {
  const input = {
    target: earlierCommit,
    head: "3333333333333333333333333333333333333333",
    guidance: [{ repository: null, path: "README.md", origin: "checked-in", content: "A library." }],
    earlier: { outcome: "none" },
    outcomes: ["full"],
  };
  const block = `<repository-description commit="${commit}">\n${description}\n</repository-description>`;
  assert.equal(repositoryDescriptionSection(repositoryDescriptionFromInput({ repositoryDescription: { commit, description } })), block);

  const shown = judgementArgs(pushInspection, { ...input, repositoryDescription: { commit, description } }, { env });
  assert.ok(shown.prompt.includes(`\n\n${block}\n\n## The author's description`));

  const without = judgementArgs(pushInspection, input, { env });
  assert.ok(!without.prompt.includes("<repository-description"));
  assert.equal(repositoryDescriptionFromInput({ repositoryDescription: { commit, description: "" } }), null);
});

test("an orientation's kept description reaches the main and brief reviewers and the run judgements", async (t) => {
  const workspace = operationRoot(t, "minos-description-run-");
  const git = (...args) => execFileSync("git", ["-C", workspace, ...args], { encoding: "utf8" }).trim();
  git("init", "-q");
  const guide = join(workspace, "README.md");
  writeFileSync(guide, "A library.\n");
  mkdirSync(join(workspace, ".review"));
  writeFileSync(join(workspace, ".review/parser.md"), "---\nrelevance: Changes to parsing\n---\nInspect the parser.\n");
  git("add", ".");
  git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "Base");
  const target = git("rev-parse", "HEAD");
  writeFileSync(join(workspace, "parser.js"), "export default () => null;\n");
  git("add", ".");
  git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "Parser");
  const head = git("rev-parse", "HEAD");
  const orientation = join(workspace, "orientation.json");
  writeFileSync(orientation, JSON.stringify({
    repository: workspace,
    guidance: [{ source: { path: "README.md" }, location: guide, origin: "checked-in" }],
    repositoryDescription: keptRecord(t, { kind: RECORD_KIND, commit, description }),
  }));
  const runEnv = {
    ...process.env, ...env, MINOS_WORKSPACE: workspace, MINOS_ORIENTATION: orientation,
    MINOS_PUSH_INSPECTION_OUTCOMES: '["full"]',
    MINOS_REVIEW_THRESHOLD: "High", MINOS_FILING_DESTINATION: '{"kind":"none"}',
  };
  const block = `<repository-description commit="${commit}">\n${description}\n</repository-description>`;

  for (const [workflow, label] of [["review", "exploration"], ["review-briefs", "brief-relevance"]]) {
    const builder = workflow === "review" ? "review-inputs" : "review-brief-inputs";
    const args = JSON.parse(execFileSync(process.execPath, [
      fileURLToPath(new URL(`./${builder}.mjs`, import.meta.url)), target, head,
    ], { env: runEnv }));
    const { calls } = await invokeWorkflow(loadWorkflow(`${workflow}.js`), args, () => null);
    assert.equal(calls[0]?.opts.label, label);
    assert.ok(calls[0].prompt.includes(block), `${workflow} receives the description`);
  }

  const prompts = new Map();
  const run = {
    env: runEnv, workspace, orientation, head, target,
    async forge(operation) {
      switch (operation) {
        case "earlier": return { outcome: "none" };
        case "snapshot": return { head_sha: head, target_sha: target, authenticated_user: "minos", reviews: [] };
        case "threads": return [];
        default: assert.fail(`unexpected forge command ${operation}`);
      }
    },
    async judge(judgedStage, input) {
      prompts.set(judgedStage.name, judgementArgs(judgedStage, input, { env: runEnv }).prompt);
      if (judgedStage === pushInspection) return { outcome: "full", reason: "Fixture answer." };
      return { main: { verdict: "clean", basis: "Fixture answer.", findings: [{ key: "fixture", gating: false }] } };
    },
  };
  await inspectPush(run);
  await classify(run, { review: {
    status: "complete",
    confirmedFindings: [{ key: "fixture", severity: "Low", path: "parser.js", line: 1, title: "Fixture advice" }],
  } });
  for (const name of ["push-inspection", "verdict-classification"])
    assert.ok(prompts.get(name)?.includes(block), `${name} receives the description`);
});
