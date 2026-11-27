import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";

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
