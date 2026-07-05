export const meta = {
  name: "pump19-assemble-review",
  description: "Compose the single posted Pump-19 review from verified findings"
};

const assembledReviewSchema = {
  type: "object",
  additionalProperties: false,
  required: ["body", "finding_order"],
  properties: {
    body: { type: "string" },
    finding_order: { type: "array", items: { type: "string" } }
  }
};

const assembler = args.assembler || (args.assemblers || [])[0];
if (!assembler) {
  throw new Error("assemble-review requires an assembler target");
}

const prompt = `Compose the single Pump-19 review body from the verified findings below.
Return JSON matching the schema.

Write concise, professional review prose. Lead with the highest-priority
findings, keep each finding actionable, and preserve the supplied anchors and
evidence. Do not invent new findings and do not include rejected or unverified
claims. The Rust frame enforces anchor, changed-line, and deduplication gates;
your job is readable review prose over the verified set.

<verified_findings>
${JSON.stringify(args.verified_findings || [])}
</verified_findings>

<manifest_summary>
${args.manifest_summary || ""}
</manifest_summary>`;

const options = {
  engine: assembler.engine,
  model: assembler.model,
  label: assembler.agent_id,
  schema: assembledReviewSchema
};
if (args.agent_timeout_ms) options.timeoutMs = args.agent_timeout_ms;

const result = await agent(prompt, options);
if (result === null) {
  throw new Error("assembled review failed schema validation");
}

return result;
