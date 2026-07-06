export const meta = {
  name: "pump19-repair-output",
  description: "Re-emit invalid Pump-19 agent output against the supplied schema"
};

const repairer = args.repairer || (args.repairers || [])[0];
if (!repairer) {
  throw new Error("repair-output requires a repairer target");
}

const prompt = `Repair this Pump-19 agent output so it validates against the supplied schema.

Preserve the same substantive meaning. Change only structure, field names,
missing required fields, enum spellings, and JSON formatting needed to satisfy
the schema. If a required textual field has no source content, use an empty
string rather than inventing a claim.

For Pump-19 lead review payloads, verdict_proposal is a tagged union whose
verdict value must be exactly one of: converged, findings_posted,
standing_findings, bar_failed, bar_check_degraded, or partial_coverage.
Never emit synonyms such as approved, ready, clean, accept, or approve. If the
invalid output substantively says there are no findings and the PR is ready,
but lacks a schema-valid bar_check, preserve that meaning by emitting
{"verdict":"bar_check_degraded","attempts":0,"last_error":"lead proposed clean convergence without a schema-valid bar_check"}
so the review frame can launch its authorised bar-check retry.

<validation_errors>
${JSON.stringify(args.errors || [])}
</validation_errors>

<invalid_output>
${typeof args.invalid_output === "string" ? args.invalid_output : JSON.stringify(args.invalid_output)}
</invalid_output>`;

const options = {
  engine: repairer.engine,
  model: repairer.model,
  label: repairer.agent_id,
  schema: args.schema
};
if (args.agent_timeout_ms) options.timeoutMs = args.agent_timeout_ms;

const result = await agent(prompt, options);
if (result === null) {
  throw new Error("repair output failed schema validation");
}

return result;
