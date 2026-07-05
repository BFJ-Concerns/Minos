export const meta = {
  name: "pump19-verify-findings",
  description: "Verify candidate Pump-19 findings with deterministic family pairing"
};

const verificationSchema = {
  type: "object",
  additionalProperties: false,
  required: ["verdict", "evidence", "rationale"],
  properties: {
    verdict: { type: "string", enum: ["verified", "rejected"] },
    evidence: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["path", "line", "quote", "note"],
        properties: {
          path: { type: "string" },
          line: { type: "integer" },
          quote: { type: "string" },
          note: { type: "string" }
        }
      }
    },
    rationale: { type: "string" }
  }
};

function verifierFor(finding, verifiers) {
  const producerFamily = finding.producing_model_family || finding.model_family || "";
  const producerAgentId =
    finding.producing_agent_id ||
    finding.producer_agent_id ||
    finding.reviewer_agent_id ||
    finding.agent_id ||
    finding.provenance?.agent_id ||
    "";
  const eligibleVerifiers = verifiers.filter((verifier) =>
    !producerAgentId || verifier.agent_id !== producerAgentId
  );
  if (eligibleVerifiers.length === 0) {
    return {
      target: null,
      family_split: "unknown",
      unavailable_reason: "no non-producer verifier target is available"
    };
  }
  const crossFamily = eligibleVerifiers.find((verifier) =>
    verifier.model_family && producerFamily && verifier.model_family !== producerFamily
  );
  if (crossFamily) {
    return { target: crossFamily, family_split: "cross_family" };
  }
  const sameFamily = eligibleVerifiers.find((verifier) => verifier.model_family === producerFamily);
  if (sameFamily) {
    return { target: sameFamily, family_split: "same_family" };
  }
  return { target: eligibleVerifiers[0], family_split: "unknown" };
}

function optionsFor(target, label) {
  const options = {
    engine: target.engine,
    model: target.model,
    label,
    schema: verificationSchema
  };
  if (args.agent_timeout_ms) options.timeoutMs = args.agent_timeout_ms;
  return options;
}

function verificationPrompt(finding) {
  return `Independently verify this Pump-19 review finding. Return JSON matching the schema.

Your job is to look for why the claim is wrong before accepting it. Verify only
when the issue is real, anchored to a changed line, supported by cited evidence,
and worth a review reader's time. Reject speculative claims, linter-catchable
issues, pre-existing problems on untouched lines, or findings whose evidence
does not support the stated behaviour.

<finding>
${JSON.stringify(finding)}
</finding>`;
}

const findings = args.findings || [];
const verifiers = args.verifiers || [];
if (findings.length > 0 && verifiers.length === 0) {
  throw new Error("verify-findings requires at least one verifier target");
}

const calls = findings.map((finding) => {
  const selection = verifierFor(finding, verifiers);
  return {
    finding,
    verifier: selection.target,
    family_split: selection.family_split,
    unavailable_reason: selection.unavailable_reason || ""
  };
});

const runnableCalls = calls.filter((call) => call.verifier !== null);
const outputs = await parallel(runnableCalls.map(({ finding, verifier }) => () =>
  agent(verificationPrompt(finding), optionsFor(verifier, `${verifier.agent_id}:${finding.dedup_hint || finding.id}`))
));

if (outputs.some((output) => output === null)) {
  throw new Error("verification output failed schema validation");
}

return {
  verifications: calls.map((call, index) => {
    if (call.verifier === null) {
      return {
        finding_ref: call.finding.dedup_hint || call.finding.id || String(index),
        verifier_agent_id: "",
        family_split: call.family_split,
        verdict: "rejected",
        evidence: [],
        rationale: call.unavailable_reason
      };
    }
    const output = outputs.shift();
    return {
      finding_ref: call.finding.dedup_hint || call.finding.id || String(index),
      verifier_agent_id: call.verifier.agent_id,
      family_split: call.family_split,
      verdict: output.verdict,
      evidence: output.evidence,
      rationale: output.rationale
    };
  })
};
