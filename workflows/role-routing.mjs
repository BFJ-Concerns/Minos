// Resolves which engine, model and effort each workflow role runs on.
// The service exports the operator's explicit choices as MINOS_ROUTING and
// the run body exports the engines whose auth it seeded as
// MINOS_PROVISIONED_ENGINES; an unset role defaults to the cross-family
// pairing when both engines are provisioned and to the one provisioned
// engine otherwise. The input builders attach the resolved table to every
// workflow's arguments, so no workflow carries an engine or model of its
// own — a workflow that received no routing fails before dispatch.

// `judgement` is the role every one-shot judgement stage of the run driver
// takes (workflows/judgements/README.md).
export const ROLES = ["exploration", "proposer", "verifier", "brief-planner", "judgement"];
export const ENGINES = ["claude", "codex"];
// The Ensemble runtime's canonical effort domain; the Go loader validates the
// configured value against the same list, so nothing the run receives can be
// refused at dispatch.
export const EFFORTS = ["minimal", "low", "medium", "high", "xhigh"];

const DEFAULT_MODEL = { claude: "claude-opus-5-5", codex: "gpt-6.1-sol" };
// The shipped pairing proposes findings on GPT Astra and verifies them on
// Claude Opus, both at medium: on the pull requests it was tried on,
// Astra's specialists found most of the defects their authors later fixed,
// and Opus was the verifier whose own severity grades sorted them. The
// proposer takes Astra whenever it runs on Codex; any other role on Codex
// runs on Sol.
const PAIRED_MODEL = { proposer: { codex: "gpt-6.2-astra" } };
const DEFAULT_EFFORT = {
  exploration: "high",
  proposer: "medium",
  verifier: "medium",
  "brief-planner": "high",
  judgement: "medium",
};
// The documented recommendation: every finding-proposing and planning role on
// GPT, every verifier on Claude, so a finding is checked by the family that
// did not propose it. A judgement stage decides a run's course or its
// published result and proposes nothing; it takes Claude.
const PAIRED_ENGINE = {
  exploration: "codex",
  proposer: "codex",
  verifier: "claude",
  "brief-planner": "codex",
  judgement: "claude",
};

function defaultModel(role, engine) {
  return (PAIRED_MODEL[role] && PAIRED_MODEL[role][engine]) || DEFAULT_MODEL[engine];
}

function assertConfiguredRole(role, entry) {
  if (entry === undefined) return;
  if (!entry || typeof entry !== "object" || Array.isArray(entry))
    throw new Error(`routing.${role} must be an object`);
  for (const key of Object.keys(entry))
    if (!["engine", "model", "effort"].includes(key)) throw new Error(`routing.${role}.${key} is not a routing field`);
  if (entry.engine !== undefined && !ENGINES.includes(entry.engine))
    throw new Error(`routing.${role}.engine must be one of ${ENGINES.join(", ")}`);
  if (entry.model !== undefined && (typeof entry.model !== "string" || entry.model === ""))
    throw new Error(`routing.${role}.model must be a non-empty string`);
  if (entry.effort !== undefined && !EFFORTS.includes(entry.effort))
    throw new Error(`routing.${role}.effort must be one of ${EFFORTS.join(", ")}`);
  if (entry.model !== undefined && entry.engine === undefined)
    throw new Error(`routing.${role}.model needs routing.${role}.engine, which names the model's family`);
}

export function resolveRouting({ configured = {}, provisioned }) {
  if (!Array.isArray(provisioned) || provisioned.length === 0 || provisioned.some((engine) => !ENGINES.includes(engine)))
    throw new Error(`provisioned engines must be a non-empty subset of ${ENGINES.join(", ")}`);
  if (!configured || typeof configured !== "object" || Array.isArray(configured))
    throw new Error("routing must be an object keyed by role");
  for (const role of Object.keys(configured))
    if (!ROLES.includes(role)) throw new Error(`routing.${role} is not a workflow role`);
  const bothProvisioned = ENGINES.every((engine) => provisioned.includes(engine));
  const routing = {};
  for (const role of ROLES) {
    const entry = configured[role];
    assertConfiguredRole(role, entry);
    const engine = entry && entry.engine !== undefined
      ? entry.engine
      : (bothProvisioned ? PAIRED_ENGINE[role] : provisioned[0]);
    if (!provisioned.includes(engine))
      throw new Error(`routing.${role}.engine names ${engine}, which this deployment has not provisioned`);
    routing[role] = {
      engine,
      model: entry && entry.model !== undefined ? entry.model : defaultModel(role, engine),
      effort: entry && entry.effort !== undefined ? entry.effort : DEFAULT_EFFORT[role],
    };
  }
  return routing;
}

// The run's routing, from the environment the service and run body
// exported. MINOS_PROVISIONED_ENGINES is required; MINOS_ROUTING is
// optional and holds only the roles the operator set.
export function routingFromEnvironment(env = process.env) {
  const provisionedText = env.MINOS_PROVISIONED_ENGINES;
  if (typeof provisionedText !== "string" || provisionedText.trim() === "")
    throw new Error("MINOS_PROVISIONED_ENGINES is required");
  let configured = {};
  if (env.MINOS_ROUTING !== undefined && env.MINOS_ROUTING !== "") {
    try {
      configured = JSON.parse(env.MINOS_ROUTING);
    } catch (error) {
      throw new Error(`MINOS_ROUTING is not valid JSON (${error.message})`);
    }
  }
  return resolveRouting({ configured, provisioned: provisionedText.trim().split(/\s+/) });
}

// A workflow's check of the resolved routing its input builder attached: every role's engine,
// model and effort, already defaulted for what the deployment provisioned.
// Nothing here is fixed in source; a missing or malformed table fails the
// workflow before any dispatch.
export function routingFromInput(input) {
  const routing = input && input.routing;
  if (!routing || typeof routing !== "object" || Array.isArray(routing)) return null;
  for (const role of ROLES) {
    const entry = routing[role];
    if (
      !entry ||
      !ENGINES.includes(entry.engine) ||
      typeof entry.model !== "string" || entry.model === "" ||
      typeof entry.effort !== "string" || entry.effort === ""
    ) return null;
  }
  return routing;
}
