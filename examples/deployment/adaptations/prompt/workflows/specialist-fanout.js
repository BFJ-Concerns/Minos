export const meta = {
  name: "pump19-specialist-fanout",
  description: "Run authorised Pump-19 specialist reviewers over selected briefs"
};

const findingSchema = {
  type: "object",
  additionalProperties: false,
  required: ["findings", "coverage_notes"],
  properties: {
    findings: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: [
          "dedup_hint",
          "title",
          "explanation",
          "suggestion",
          "priority",
          "locations",
          "evidence_notes"
        ],
        properties: {
          dedup_hint: { type: "string" },
          title: { type: "string" },
          explanation: { type: "string" },
          suggestion: { type: "string" },
          priority: { type: "string", enum: ["P0", "P1", "P2", "P3"] },
          locations: {
            type: "array",
            items: {
              type: "object",
              additionalProperties: false,
              required: ["path", "line"],
              properties: {
                path: { type: "string" },
                line: { type: "integer" }
              }
            }
          },
          evidence_notes: { type: "array", items: { type: "string" } }
        }
      }
    },
    coverage_notes: { type: "string" }
  }
};

function optionsFor(target, label) {
  const options = {
    engine: target.engine,
    model: target.model,
    label,
    schema: findingSchema
  };
  if (args.agent_timeout_ms) options.timeoutMs = args.agent_timeout_ms;
  return options;
}

function specialistPrompt(brief) {
  return `You are a Pump-19 specialist reviewer.

Review this pull request only for the brief below. Return JSON matching the schema.

Hold a high confidence bar. Report a finding only when the issue is real,
material to correctness, safety, maintainability, contract behaviour, or tests,
and anchored to a changed line. Read the changed file in full and enough related
definitions or call sites to confirm the claim. Leave out linter-catchable
issues, speculative risks, and pre-existing problems on untouched lines.

For each finding, provide a stable dedup hint, a one-line title, the concrete
triggering behaviour, a drop-in suggestion when one exists or an empty string,
priority P0/P1/P2/P3, changed-line locations, and concise evidence notes.

<brief id="${brief.id}">
${brief.prompt}
</brief>`;
}

const calls = [];
for (const brief of args.briefs || []) {
  for (const reviewer of args.reviewers || []) {
    calls.push({ brief, reviewer });
  }
}

const outputs = await parallel(calls.map(({ brief, reviewer }) => () =>
  agent(specialistPrompt(brief), optionsFor(reviewer, `${reviewer.agent_id}:${brief.id}`))
));

if (outputs.some((output) => output === null)) {
  throw new Error("specialist output failed schema validation");
}

const reports = [];
for (let i = 0; i < calls.length; i += 1) {
  const { brief, reviewer } = calls[i];
  const output = outputs[i];
  reports.push({
    brief_id: brief.id,
    reviewer_agent_id: reviewer.agent_id,
    model_family: reviewer.model_family,
    findings: output.findings,
    coverage_notes: output.coverage_notes
  });
}

return { reports };
