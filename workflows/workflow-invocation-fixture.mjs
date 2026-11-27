import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";

// The hook bindings a workflow body is handed, in the order invokeWorkflow
// passes them.
const HOOK_BINDINGS = ["agent", "parallel", "pipeline", "phase", "log", "args"];

// Workflow bodies by the URL each is imported under: the script's own URL
// with a query naming the load. The hooks serve those URLs and pass every
// other specifier on.
const workflowBodies = new Map();
registerHooks({
  resolve(specifier, context, nextResolve) {
    return workflowBodies.has(specifier)
      ? { url: specifier, format: "module", shortCircuit: true }
      : nextResolve(specifier, context);
  },
  load(url, context, nextLoad) {
    return workflowBodies.has(url)
      ? { format: "module", source: workflowBodies.get(url), shortCircuit: true }
      : nextLoad(url, context);
  },
});

// Loads a workflow script from this directory as the runtime runs it: the
// body of one async function over the hook bindings, in a module at the
// script's own URL, with its `meta` export kept as an ordinary binding. A
// module the script imports therefore resolves from the script's path, as
// it does in a run. The opening of the function shares the script's first
// line, so an error's line number is the script's own.
export function loadWorkflow(filename) {
  const script = new URL(filename, import.meta.url);
  const source = readFileSync(script, "utf8").replace(/^export const meta =/m, "const meta =");
  const url = `${script.href}?workflow-body=${workflowBodies.size + 1}`;
  workflowBodies.set(url, `export default async (${HOOK_BINDINGS.join(", ")}) => {${source}\n};\n`);
  return async (...hooks) => (await import(url)).default(...hooks);
}

export async function invokeWorkflow(script, args, respond) {
  const calls = [];
  const agent = async (prompt, opts = {}) => {
    calls.push({ prompt, opts });
    const output = await respond(opts.label || "", prompt, opts);
    if (!opts.identity) return output;
    if (output && output.identityEnvelope) return output.identityEnvelope;
    return { label: opts.label, phase: opts.phase, output, failure: output === null ? { message: "worker exhausted" } : null };
  };
  const parallel = async (thunks) => Promise.all(thunks.map((thunk) => thunk()));
  const pipeline = () => { throw new Error("workflow unexpectedly called pipeline"); };
  const result = await script(agent, parallel, pipeline, () => {}, () => {}, args);
  return { result, calls };
}
