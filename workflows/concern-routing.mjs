// Resolves which engine, model and effort each specialist concern proposes
// on. The service exports the concerns the operator routed as
// MINOS_CONCERN_ROUTING, an object keyed by concern name whose entries take
// the role routing's fields; a concern it does not name proposes on the
// resolved proposer routing. A routed concern starts from that routing
// too: the fields it sets replace the proposer's, and a concern that moves
// to another engine without naming a model takes the proposer's default
// model on that engine. The input builder attaches the resolved table, one
// entry for every shipped concern, and review.js dispatches each
// specialist on its concern's entry.

import { ENGINES, EFFORTS, defaultModel } from "./role-routing.mjs";

function assertConfiguredConcern(name, entry) {
  if (!entry || typeof entry !== "object" || Array.isArray(entry))
    throw new Error(`routing.concerns.${name} must be an object`);
  for (const key of Object.keys(entry))
    if (!["engine", "model", "effort"].includes(key))
      throw new Error(`routing.concerns.${name}.${key} is not a routing field`);
  if (entry.engine !== undefined && !ENGINES.includes(entry.engine))
    throw new Error(`routing.concerns.${name}.engine must be one of ${ENGINES.join(", ")}`);
  if (entry.model !== undefined && (typeof entry.model !== "string" || entry.model === ""))
    throw new Error(`routing.concerns.${name}.model must be a non-empty string`);
  if (entry.effort !== undefined && !EFFORTS.includes(entry.effort))
    throw new Error(`routing.concerns.${name}.effort must be one of ${EFFORTS.join(", ")}`);
  if (entry.model !== undefined && entry.engine === undefined)
    throw new Error(`routing.concerns.${name}.model needs routing.concerns.${name}.engine, which names the model's family`);
}

// Every concern's route, keyed by concern name: `configured` holds the
// concerns the operator set, `concerns` the shipped concern names,
// `proposer` the resolved proposer route and `provisioned` the engines the
// deployment holds.
export function resolveConcernRouting({ configured = {}, concerns, proposer, provisioned }) {
  if (!configured || typeof configured !== "object" || Array.isArray(configured))
    throw new Error("concern routing must be an object keyed by concern");
  for (const name of Object.keys(configured))
    if (!concerns.includes(name))
      throw new Error(`routing.concerns.${name} is not a specialist concern; the concerns are ${concerns.join(", ")}`);
  const routing = {};
  for (const name of concerns) {
    const entry = configured[name];
    if (entry === undefined) {
      routing[name] = { ...proposer };
      continue;
    }
    assertConfiguredConcern(name, entry);
    const engine = entry.engine !== undefined ? entry.engine : proposer.engine;
    if (!provisioned.includes(engine))
      throw new Error(`routing.concerns.${name}.engine names ${engine}, which this deployment has not provisioned`);
    routing[name] = {
      engine,
      model: entry.model !== undefined
        ? entry.model
        : (engine === proposer.engine ? proposer.model : defaultModel("proposer", engine)),
      effort: entry.effort !== undefined ? entry.effort : proposer.effort,
    };
  }
  return routing;
}

// The run's concern routing, from the environment the service and run body
// exported. MINOS_CONCERN_ROUTING is optional and holds only the concerns
// the operator set.
export function concernRoutingFromEnvironment({ concerns, proposer }, env = process.env) {
  let configured = {};
  if (env.MINOS_CONCERN_ROUTING !== undefined && env.MINOS_CONCERN_ROUTING !== "") {
    try {
      configured = JSON.parse(env.MINOS_CONCERN_ROUTING);
    } catch (error) {
      throw new Error(`MINOS_CONCERN_ROUTING is not valid JSON (${error.message})`);
    }
  }
  const provisioned = (env.MINOS_PROVISIONED_ENGINES || "").trim().split(/\s+/);
  return resolveConcernRouting({ configured, concerns, proposer, provisioned });
}

// A workflow's check of the concern routing its input builder attached: an
// engine, model and effort for every concern the input lists, and nothing
// else. A missing or malformed table fails the workflow before dispatch.
export function concernRoutingFromInput(input, concerns) {
  const routing = input && input.concernRouting;
  if (!routing || typeof routing !== "object" || Array.isArray(routing)) return null;
  const names = concerns.map((concern) => concern.name);
  if (Object.keys(routing).some((name) => !names.includes(name))) return null;
  for (const name of names) {
    const entry = routing[name];
    if (
      !entry ||
      !ENGINES.includes(entry.engine) ||
      typeof entry.model !== "string" || entry.model === "" ||
      typeof entry.effort !== "string" || entry.effort === ""
    ) return null;
  }
  return routing;
}
