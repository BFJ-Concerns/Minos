import { readFileSync } from "node:fs";

const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;

// The hook bindings a workflow body is handed, in the order invokeWorkflow
// passes them.
const HOOK_BINDINGS = ["agent", "parallel", "pipeline", "phase", "log", "args"];

// Loads a workflow script from this directory as the runtime runs it: the
// body of one async function over the hook bindings, with its `meta`
// export kept as an ordinary binding.
export function loadWorkflow(filename) {
  const source = readFileSync(new URL(filename, import.meta.url), "utf8");
  return new AsyncFunction(...HOOK_BINDINGS, source.replace(/^export const meta =/m, "const meta ="));
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
