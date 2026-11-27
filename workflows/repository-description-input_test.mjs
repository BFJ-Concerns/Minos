import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";

import { descriptionInput, writeDescriptionArgs } from "./repository-description-input.mjs";
import { RECORD_KIND } from "./repository-description.mjs";
import { operationRoot } from "./temporary-directory-fixture.mjs";

const commit = "1111111111111111111111111111111111111111";
const earlierCommit = "2222222222222222222222222222222222222222";

test("the unit asks the stage about its commit with the kept description as the earlier one", async (t) => {
  const runDir = operationRoot(t, "minos-describe-");
  const record = join(runDir, "kept.json");
  writeFileSync(record, JSON.stringify({ kind: RECORD_KIND, commit: earlierCommit, description: "A parser.", written_at: "2026-11-27T00:00:00Z" }));

  await writeDescriptionArgs({
    MINOS_DESCRIPTION_COMMIT: commit, MINOS_DESCRIPTION_RECORD: record, MINOS_RUN_DIR: runDir,
    MINOS_PROVISIONED_ENGINES: "claude codex", MINOS_ROUTING: JSON.stringify({ description: { effort: "low" } }),
  });

  const args = JSON.parse(readFileSync(join(runDir, "repository-description-args.json"), "utf8"));
  assert.equal(args.stage, "repository-description");
  assert.deepEqual(args.input, { commit, earlier: { commit: earlierCommit, description: "A parser." } });
  assert.deepEqual(args.routing, { engine: "claude", model: "claude-opus-5-5", effort: "low" });
  assert.match(args.prompt, new RegExp(`<earlier-description commit="${earlierCommit}">`));
  assert.deepEqual(JSON.parse(readFileSync(join(runDir, "repository-description-input.json"), "utf8")), args.input);
});

test("with no record kept, or one that is not a whole record, the stage writes afresh", (t) => {
  const runDir = operationRoot(t, "minos-describe-");
  assert.deepEqual(descriptionInput(commit, join(runDir, "absent.json")), { commit, earlier: null });

  const broken = join(runDir, "broken.json");
  writeFileSync(broken, "{\"kind\": \"something else\"}");
  const warnings = [];
  assert.deepEqual(descriptionInput(commit, broken, { warn: (message) => warnings.push(message) }), { commit, earlier: null });
  assert.equal(warnings.length, 1);
  assert.match(warnings[0], /kept description is not used/);
});

test("the unit refuses to build an input without its commit", async (t) => {
  const runDir = operationRoot(t, "minos-describe-");
  await assert.rejects(
    writeDescriptionArgs({ MINOS_DESCRIPTION_RECORD: join(runDir, "kept.json"), MINOS_RUN_DIR: runDir, MINOS_PROVISIONED_ENGINES: "claude" }),
    /MINOS_DESCRIPTION_COMMIT is required/,
  );
});
