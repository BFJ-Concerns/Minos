import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import test from "node:test";

import { pathWithinScope } from "./review-stages.mjs";

test("a path lies within a whole scope only under its directory, and never by climbing out", () => {
  assert.ok(pathWithinScope("pkg/x.go", "pkg/"));
  assert.ok(pathWithinScope("./pkg/sub/x.go", "pkg/"));
  assert.ok(pathWithinScope("cmd/main.go", "./"));
  assert.ok(!pathWithinScope("pkgx/a.go", "pkg/"));
  assert.ok(!pathWithinScope("cmd/main.go", "pkg/"));
  assert.ok(!pathWithinScope("pkg/../cmd/main.go", "pkg/"));
  assert.ok(!pathWithinScope("../outside.go", "./"));
  assert.ok(!pathWithinScope("/etc/passwd", "./"));
  assert.ok(!pathWithinScope(undefined, "pkg/"));
  assert.ok(!pathWithinScope("", "./"));
});
