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
