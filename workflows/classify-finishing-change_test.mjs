import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import { classifyPaths, isTestPath } from "./classify-finishing-change.mjs";

test("test-only paths are recognised without treating production source as tests", () => {
  for (const path of [
    "crates/relay-app/tests/session_isolation_journey.rs",
    "internal/cache/cache_test.go",
    "src/parser.spec.ts",
    "pkg/testdata/input.json",
  ])
    assert.equal(isTestPath(path), true, path);

  for (const path of [
    "crates/relay-app/src/session.rs",
    "internal/testing/runtime.go",
    "docs/test-strategy.md",
    ".forgejo/workflows/test.yml",
  ])
    assert.equal(isTestPath(path), false, path);
});

test("classification requires every changed path to be test-only", () => {
  assert.deepEqual(
    classifyPaths(["crates/app/tests/journey.rs", "internal/cache/cache_test.go"]),
    {
      classification: "tests-only",
      paths: ["crates/app/tests/journey.rs", "internal/cache/cache_test.go"],
    },
  );
  assert.equal(
    classifyPaths(["crates/app/tests/journey.rs", "crates/app/src/runtime.rs"]).classification,
    "review-required",
  );
  assert.equal(classifyPaths([]).classification, "empty");
});

test("the command classifies the actual commit range consumed by finishing", () => {
  const repository = mkdtempSync(join(tmpdir(), "minos-finishing-change-"));
  execFileSync("git", ["init", "-q"], { cwd: repository });
  execFileSync("git", ["config", "user.name", "Fixture"], { cwd: repository });
  execFileSync("git", ["config", "user.email", "fixture@example.invalid"], { cwd: repository });

  const testPath = "crates/app/tests/journey.rs";
  mkdirSync(dirname(join(repository, testPath)), { recursive: true });
  writeFileSync(join(repository, testPath), "before\n");
  execFileSync("git", ["add", "."], { cwd: repository });
  execFileSync("git", ["commit", "-q", "-m", "before"], { cwd: repository });
  const before = execFileSync("git", ["rev-parse", "HEAD"], { cwd: repository, encoding: "utf8" }).trim();

  writeFileSync(join(repository, testPath), "after\n");
  execFileSync("git", ["commit", "-qam", "repair test"], { cwd: repository });
  const after = execFileSync("git", ["rev-parse", "HEAD"], { cwd: repository, encoding: "utf8" }).trim();

  const script = join(import.meta.dirname, "classify-finishing-change.mjs");
  const result = JSON.parse(execFileSync(script, [repository, before, after], { encoding: "utf8" }));
  assert.deepEqual(result, { classification: "tests-only", paths: [testPath] });
});
