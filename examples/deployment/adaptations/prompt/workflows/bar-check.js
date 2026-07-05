export const meta = {
  name: "pump19-bar-check",
  description: "Judge an assembled Pump-19 review against the review quality bar"
};

const barCheckSchema = {
  type: "object",
  additionalProperties: false,
  required: ["passed", "rationale"],
  properties: {
    passed: { type: "boolean" },
    rationale: { type: "string" }
  }
};

const checker = args.checker || (args.checkers || [])[0];
if (!checker) {
  throw new Error("bar-check requires a checker target");
}

const prompt = `Judge this assembled Pump-19 review against the quality bar.
Return JSON matching the schema.

Pass only if the review is quiet, high-confidence, grounded in the diff and
coverage record, honours the selected briefs and subject guidance, and does not
claim convergence when coverage is incomplete. Fail reviews that contain
speculative findings, miss material changed-line issues apparent from the
provided evidence, include unverified claims, or treat partial coverage as clean.

<assembled_review>
${args.assembled_review || ""}
</assembled_review>

<coverage>
${JSON.stringify(args.coverage || {})}
</coverage>

<manifest_summary>
${args.manifest_summary || ""}
</manifest_summary>

<diff_path>
${args.diff_path || ""}
</diff_path>`;

const options = {
  engine: checker.engine,
  model: checker.model,
  label: checker.agent_id,
  schema: barCheckSchema
};
if (args.agent_timeout_ms) options.timeoutMs = args.agent_timeout_ms;

const result = await agent(prompt, options);
if (result === null) {
  throw new Error("bar check failed schema validation");
}

return result;
