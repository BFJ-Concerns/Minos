export const meta = {
  name: "pump19-review",
  description: "Run authorised Pump-19 reviewers over judgement briefs"
};

const reviewSchema = {
  type: "object",
  additionalProperties: false,
  required: ["status", "stdout", "stderr"],
  properties: {
    status: { type: "string", enum: ["passed", "failed"] },
    stdout: { type: "string" },
    stderr: { type: "string" }
  }
};

function optionsFor(target, label) {
  return {
    engine: target.engine,
    model: target.model,
    label,
    schema: reviewSchema,
    timeoutMs: args.agent_timeout_ms || 300000
  };
}

const calls = [];
for (const brief of args.briefs) {
  for (const reviewer of args.reviewers) {
    calls.push({ brief, reviewer });
  }
}

const outputs = await parallel(calls.map(({ brief, reviewer }) => () =>
  agent(brief.prompt, optionsFor(reviewer, `${reviewer.agent_id}:${brief.id}`))
));

if (outputs.some((output) => output === null)) {
  throw new Error("reviewer output failed schema validation");
}

const briefResults = [];
for (const brief of args.briefs) {
  const reviews = [];
  for (let i = 0; i < calls.length; i += 1) {
    if (calls[i].brief.id !== brief.id) continue;
    const reviewer = calls[i].reviewer;
    const output = outputs[i];
    reviews.push({
      agent_id: reviewer.agent_id,
      model_family: reviewer.model_family,
      status: output.status,
      stdout: output.stdout,
      stderr: output.stderr
    });
  }
  briefResults.push({
    brief_id: brief.id,
    status: reviews.some((review) => review.status === "failed") ? "failed" : "passed",
    reviews
  });
}

return {
  status: briefResults.some((brief) => brief.status === "failed") ? "failed" : "passed",
  briefs: briefResults,
  model_families: [...new Set(args.reviewers.map((reviewer) => reviewer.model_family))].sort()
};
