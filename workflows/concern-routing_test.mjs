import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import test from "node:test";

import { concernRoutingFromEnvironment, concernRoutingFromInput, resolveConcernRouting } from "./concern-routing.mjs";
import { resolveRouting } from "./role-routing.mjs";

const CONCERNS = ["failure", "logic", "security"];
const BOTH = ["claude", "codex"];
const proposer = resolveRouting({ provisioned: BOTH }).proposer;

test("an unrouted concern proposes on the proposer's routing", () => {
  const routing = resolveConcernRouting({ concerns: CONCERNS, proposer, provisioned: BOTH });
  assert.deepEqual(Object.keys(routing), CONCERNS);
  for (const name of CONCERNS) assert.deepEqual(routing[name], proposer);
});

test("a routed concern replaces only the fields it sets, taking the proposer's default model on another engine", () => {
  const custom = { engine: "codex", model: "gpt-6.1-sol", effort: "high" };
  const routing = resolveConcernRouting({
    concerns: CONCERNS,
    proposer: custom,
    provisioned: BOTH,
    configured: {
      security: { engine: "claude" },
      logic: { effort: "low" },
      failure: { engine: "codex", model: "gpt-6.2-astra" },
    },
  });
  assert.deepEqual(routing.security, { engine: "claude", model: "claude-opus-5-5", effort: "high" });
  assert.deepEqual(routing.logic, { engine: "codex", model: "gpt-6.1-sol", effort: "low" });
  assert.deepEqual(routing.failure, { engine: "codex", model: "gpt-6.2-astra", effort: "high" });
});

test("concern routing that cannot be honoured is refused", () => {
  for (const [configured, message] of [
    [{ style: {} }, /routing\.concerns\.style is not a specialist concern; the concerns are failure, logic, security/],
    [{ logic: "claude" }, /routing\.concerns\.logic must be an object/],
    [{ logic: { engin: "claude" } }, /routing\.concerns\.logic\.engin is not a routing field/],
    [{ logic: { engine: "opencode" } }, /routing\.concerns\.logic\.engine must be one of claude, codex/],
    [{ logic: { engine: "claude", model: "" } }, /routing\.concerns\.logic\.model must be a non-empty string/],
    [{ logic: { effort: "maximal" } }, /routing\.concerns\.logic\.effort must be one of/],
    [{ logic: { model: "claude-opus-5-5" } }, /routing\.concerns\.logic\.model needs routing\.concerns\.logic\.engine/],
    [[], /concern routing must be an object keyed by concern/],
  ]) assert.throws(() => resolveConcernRouting({ configured, concerns: CONCERNS, proposer, provisioned: BOTH }), message);
  assert.throws(
    () => resolveConcernRouting({ configured: { logic: { engine: "claude" } }, concerns: CONCERNS, proposer, provisioned: ["codex"] }),
    /routing\.concerns\.logic\.engine names claude, which this deployment has not provisioned/,
  );
});

test("the environment's concern routing is optional and must be JSON", () => {
  const env = { MINOS_PROVISIONED_ENGINES: "claude codex" };
  assert.deepEqual(concernRoutingFromEnvironment({ concerns: CONCERNS, proposer }, env).logic, proposer);
  assert.equal(
    concernRoutingFromEnvironment({ concerns: CONCERNS, proposer }, { ...env, MINOS_CONCERN_ROUTING: '{"logic":{"engine":"claude"}}' }).logic.engine,
    "claude",
  );
  assert.throws(
    () => concernRoutingFromEnvironment({ concerns: CONCERNS, proposer }, { ...env, MINOS_CONCERN_ROUTING: "{" }),
    /MINOS_CONCERN_ROUTING is not valid JSON/,
  );
});

test("a workflow accepts only a complete resolved table for the concerns it was given", () => {
  const concerns = CONCERNS.map((name) => ({ name, summary: name }));
  const concernRouting = resolveConcernRouting({ concerns: CONCERNS, proposer, provisioned: BOTH });
  assert.equal(concernRoutingFromInput({ concernRouting }, concerns), concernRouting);
  assert.equal(concernRoutingFromInput({}, concerns), null);
  const { logic, ...missing } = concernRouting;
  assert.equal(concernRoutingFromInput({ concernRouting: missing }, concerns), null);
  assert.equal(concernRoutingFromInput({ concernRouting: { ...concernRouting, style: proposer } }, concerns), null);
  assert.equal(concernRoutingFromInput({ concernRouting: { ...concernRouting, logic: { ...proposer, engine: "opencode" } } }, concerns), null);
});
