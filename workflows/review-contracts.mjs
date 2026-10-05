// The finding, observation and verifier contracts both review workflows
// share, and the verifier batch size. A workflow script imports nothing, so
// the input builders attach these as `args.contracts` and each workflow
// validates them before dispatching anything; neither keeps a copy.

export const VERIFIER_BATCH_SIZE = 6;

export const findingShape = {
  type: "object",
  additionalProperties: false,
  required: ["title", "severity", "confidence", "path", "line", "explanation"],
  properties: {
    title: { type: "string" },
    severity: { type: "string", enum: ["Critical", "High", "Medium", "Low"] },
    confidence: { type: "integer", minimum: 0, maximum: 100 },
    path: { type: "string" },
    line: { type: "integer", minimum: 1 },
    explanation: { type: "string" },
  },
};

export const outOfScopeObservationShape = {
  type: "object",
  additionalProperties: false,
  required: ["title", "path", "line", "explanation"],
  properties: {
    title: { type: "string" },
    path: { type: "string" },
    line: { type: "integer", minimum: 1 },
    explanation: { type: "string" },
  },
};

export const applicabilityShape = {
  type: "object",
  additionalProperties: false,
  required: ["status", "reason"],
  properties: {
    status: { type: "string", enum: ["applicable", "inapplicable"] },
    reason: { type: "string" },
  },
};

export const specialistSchema = {
  type: "object",
  additionalProperties: false,
  required: ["applicability", "findings"],
  properties: {
    applicability: applicabilityShape,
    findings: { type: "array", items: findingShape },
    outOfScopeObservations: { type: "array", items: outOfScopeObservationShape },
  },
};

export const verifierVerdictShape = {
  type: "object",
  additionalProperties: false,
  required: ["findingId", "verdict", "confidence", "reason"],
  properties: {
    findingId: { type: "string" },
    verdict: { type: "string", enum: ["upheld", "refuted"] },
    confidence: { type: "integer", minimum: 0, maximum: 100 },
    reason: { type: "string" },
  },
};

export const verifierSchema = {
  type: "object",
  additionalProperties: false,
  required: ["verdicts"],
  properties: {
    verdicts: { type: "array", items: verifierVerdictShape },
    outOfScopeObservations: { type: "array", items: outOfScopeObservationShape },
  },
};

// The `args.contracts` value, as the builders emit it: plain JSON.
export function reviewContracts() {
  return JSON.parse(JSON.stringify({
    verifierBatchSize: VERIFIER_BATCH_SIZE,
    findingShape,
    outOfScopeObservationShape,
    applicabilityShape,
    specialistSchema,
    verifierVerdictShape,
    verifierSchema,
  }));
}
