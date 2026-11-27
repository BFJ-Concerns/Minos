export const meta = {
  name: "minos-judgement",
  description: "One judgement stage of a run: one agent, one answer bound to a schema",
  phases: [
    { title: "Judge", detail: "one agent reads what the stage was given and answers" },
  ],
};

// Every judgement stage runs through this one script. The stage's input
// builder (judgement-stage.mjs) attaches its complete prompt, the schema of
// its answer and the routing it resolved, so nothing here knows any stage.
const input = args && typeof args === "object" && !Array.isArray(args) ? args : null;
const stage = input && typeof input.stage === "string" && input.stage !== "" ? input.stage : null;
if (!stage) throw new Error("judgement workflow needs args {stage, prompt, schema, routing}");
if (typeof input.prompt !== "string" || input.prompt.trim() === "")
  throw new Error("deterministic input omitted the stage's prompt");
if (!input.schema || typeof input.schema !== "object" || Array.isArray(input.schema))
  throw new Error("deterministic input omitted the schema of the stage's answer");
const routing = input.routing;
if (
  !routing ||
  typeof routing.engine !== "string" || routing.engine === "" ||
  typeof routing.model !== "string" || routing.model === "" ||
  typeof routing.effort !== "string" || routing.effort === ""
) throw new Error("deterministic input omitted the stage's routing");

phase("Judge");
const settlement = await agent(input.prompt, {
  engine: routing.engine,
  schema: input.schema,
  model: routing.model,
  effort: routing.effort,
  strip: ["skills", "agents"],
  identity: true,
  label: stage,
  phase: "Judge",
});

// A leg that gave no answer is returned as none. The runner fails the stage
// on it: no default stands in for a judgement nobody made. `answeredBy` is
// the model that answered, or null where the runtime names none. A failed
// leg carries the runtime's reason for the runner to report.
const answered = settlement && settlement.failure === null ? settlement : null;
return {
  stage,
  answer: (answered && answered.output) || null,
  answeredBy: answered?.answeredBy?.model ?? null,
  failure: settlement?.failure?.message ?? null,
};
