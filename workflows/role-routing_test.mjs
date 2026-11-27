import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import test from "node:test";

import { readFileSync } from "node:fs";

import { EFFORTS, ENGINES, ROLES, resolveRouting, routingFromEnvironment } from "./role-routing.mjs";

test("both engines provisioned and nothing configured is the cross-family pairing at the documented efforts", () => {
  const routing = resolveRouting({ provisioned: ["claude", "codex"] });
  assert.deepEqual(Object.keys(routing), ROLES);
  assert.deepEqual(routing, {
    exploration: { engine: "codex", model: "gpt-6.1-sol", effort: "high" },
    proposer: { engine: "codex", model: "gpt-6.2-astra", effort: "medium" },
    verifier: { engine: "claude", model: "claude-opus-5-5", effort: "medium" },
    "brief-planner": { engine: "codex", model: "gpt-6.1-sol", effort: "high" },
    judgement: { engine: "claude", model: "claude-opus-5-5", effort: "medium" },
    description: { engine: "claude", model: "claude-opus-5-5", effort: "medium" },
    walkthrough: { engine: "claude", model: "claude-opus-5-5", effort: "xhigh" },
    "walkthrough-diagrams": { engine: "codex", model: "gpt-6.1-sol", effort: "xhigh" },
  });
});

test("one provisioned engine routes every role to it, on the model each role takes there", () => {
  for (const engine of ENGINES) {
    const routing = resolveRouting({ provisioned: [engine] });
    assert.ok(ROLES.every((role) => routing[role].engine === engine), engine);
    for (const role of ROLES) {
      const expected = engine === "claude" ? "claude-opus-5-5" : role === "proposer" ? "gpt-6.2-astra" : "gpt-6.1-sol";
      assert.equal(routing[role].model, expected, `${engine} ${role}`);
    }
  }
});

test("a configured role overrides only the fields it sets, the model defaulting to the chosen engine's", () => {
  const routing = resolveRouting({
    provisioned: ["claude", "codex"],
    configured: { verifier: { engine: "codex", effort: "low" }, proposer: { engine: "claude", model: "claude-sonnet-5-5" } },
  });
  assert.deepEqual(routing.verifier, { engine: "codex", model: "gpt-6.1-sol", effort: "low" });
  assert.deepEqual(routing.proposer, { engine: "claude", model: "claude-sonnet-5-5", effort: "medium" });
  assert.deepEqual(routing.exploration, { engine: "codex", model: "gpt-6.1-sol", effort: "high" });
});

test("routing that cannot be honoured is refused before any dispatch could read it", () => {
  const both = ["claude", "codex"];
  for (const [input, message] of [
    [{ provisioned: [] }, /provisioned engines must be a non-empty subset/],
    [{ provisioned: ["opencode"] }, /provisioned engines must be a non-empty subset/],
    [{ provisioned: both, configured: [] }, /routing must be an object keyed by role/],
    [{ provisioned: both, configured: { judge: {} } }, /routing\.judge is not a workflow role/],
    [{ provisioned: both, configured: { verifier: "claude" } }, /routing\.verifier must be an object/],
    [{ provisioned: both, configured: { verifier: { engine: "opencode" } } }, /routing\.verifier\.engine must be one of claude, codex/],
    [{ provisioned: both, configured: { verifier: { model: "" } } }, /routing\.verifier\.model must be a non-empty string/],
    [{ provisioned: both, configured: { verifier: { effort: "maximal" } } }, /routing\.verifier\.effort must be one of none, minimal, low, medium, high, xhigh, max/],
    [{ provisioned: both, configured: { verifier: { model: "gpt-6-astra" } } }, /routing\.verifier\.model needs routing\.verifier\.engine/],
    [{ provisioned: both, configured: { verifier: { temperature: 1 } } }, /routing\.verifier\.temperature is not a routing field/],
    [{ provisioned: ["claude"], configured: { verifier: { engine: "codex" } } }, /names codex, which this deployment has not provisioned/],
  ]) assert.throws(() => resolveRouting(input), message, JSON.stringify(input));
});

test("the environment form reads the provisioned list and the optional routing JSON", () => {
  assert.deepEqual(
    routingFromEnvironment({ MINOS_PROVISIONED_ENGINES: "  codex " }).verifier,
    { engine: "codex", model: "gpt-6.1-sol", effort: "medium" },
  );
  assert.equal(
    routingFromEnvironment({ MINOS_PROVISIONED_ENGINES: "claude codex", MINOS_ROUTING: '{"verifier":{"engine":"codex"}}' }).verifier.engine,
    "codex",
  );
  assert.equal(routingFromEnvironment({ MINOS_PROVISIONED_ENGINES: "claude codex", MINOS_ROUTING: "" }).verifier.engine, "claude");
  assert.throws(() => routingFromEnvironment({}), /MINOS_PROVISIONED_ENGINES is required/);
  assert.throws(() => routingFromEnvironment({ MINOS_PROVISIONED_ENGINES: "claude", MINOS_ROUTING: "{" }), /MINOS_ROUTING is not valid JSON/);
});

test("the effort domain is the vendored runtime's, so every level it dispatches can be configured", () => {
  const bundle = readFileSync(new URL("../runtime/ensemble.mjs", import.meta.url), "utf8");
  const declared = bundle.match(/var CANONICAL_EFFORT_DOMAIN = (\[[^\]]*\]);/);
  assert.ok(declared, "runtime/ensemble.mjs declares no CANONICAL_EFFORT_DOMAIN");
  assert.deepEqual(EFFORTS, JSON.parse(declared[1]));
  const both = ["claude", "codex"];
  assert.equal(resolveRouting({ provisioned: both, configured: { verifier: { effort: "max" } } }).verifier.effort, "max");
});
