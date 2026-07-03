export const meta = {
  name: "pump19-fix",
  description: "Run the authorised Pump-19 fixer"
};

const fixSchema = {
  type: "object",
  additionalProperties: false,
  required: ["kind", "summary"],
  properties: {
    kind: { type: "string", enum: ["description"] },
    summary: { type: "string" }
  }
};

const fixer = args.fixers[0];
const result = await agent(
  args.prompt,
  {
    engine: fixer.engine,
    model: fixer.model,
    label: fixer.agent_id,
    schema: fixSchema,
    timeoutMs: args.agent_timeout_ms || 300000
  }
);

if (result === null) {
  throw new Error("fixer output failed schema validation");
}

return result;
