export const meta = {
  name: "pump19-judge",
  description: "Run the authorised Pump-19 significance judge"
};

const judgeSchema = {
  type: "object",
  additionalProperties: false,
  required: ["decisions"],
  properties: {
    decisions: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["finding_id", "verdict", "rationale"],
        properties: {
          finding_id: { type: "string" },
          verdict: { type: "string", enum: ["material", "minor"] },
          rationale: { type: "string" }
        }
      }
    }
  }
};

const judge = args.judges[0];
const result = await agent(
  args.prompt,
  {
    engine: judge.engine,
    model: judge.model,
    label: judge.agent_id,
    schema: judgeSchema,
    timeoutMs: args.agent_timeout_ms || 300000
  }
);

if (result === null) {
  throw new Error("judge output failed schema validation");
}

if ((args.current_findings || args.findings || []).length > 0 && result.decisions.length === 0) {
  throw new Error("judge returned no decisions for standing findings");
}

return result.decisions;
