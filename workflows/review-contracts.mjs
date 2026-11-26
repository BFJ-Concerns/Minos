// What each stage of the review workflows returns: the specialist, merge
// and verifier schemas both workflows share, what the merge and verifier
// legs are shown of a finding, and the verifier batch size. A workflow
// script imports nothing, so the input builders attach these as
// `args.contracts` and each workflow validates them before dispatching
// anything; neither keeps a copy.

export const VERIFIER_BATCH_SIZE = 6;

export const SEVERITIES = ["Critical", "High", "Medium", "Low"];

// One proposed finding, as a specialist, a repository-brief reviewer or a
// verifier writes it up. `dependsOn` names the fact outside the repository
// the finding stands or falls on, or is empty.
export const findingShape = {
  type: "object",
  additionalProperties: false,
  required: ["title", "severity", "confidence", "path", "line", "explanation", "dependsOn"],
  properties: {
    title: { type: "string" },
    severity: { type: "string", enum: SEVERITIES },
    confidence: { type: "integer", minimum: 0, maximum: 100 },
    path: { type: "string" },
    line: { type: "integer", minimum: 1 },
    explanation: { type: "string" },
    dependsOn: { type: "string" },
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

// What a proposing leg returns, on specialist.md or repository.md.
// `applicability` speaks for the leg's own concern alone. A problem of
// another concern that the change introduced comes back in
// `outsideConcernFindings`, whatever the applicability, and is a proposed
// finding like any in `findings`: merged and verified the same way. An
// out-of-scope observation is a defect the change did not introduce; it is
// recorded and never verified.
export const specialistSchema = {
  type: "object",
  additionalProperties: false,
  required: ["applicability", "findings", "outsideConcernFindings", "outOfScopeObservations"],
  properties: {
    applicability: applicabilityShape,
    findings: { type: "array", items: findingShape },
    outsideConcernFindings: { type: "array", items: findingShape },
    outOfScopeObservations: { type: "array", items: outOfScopeObservationShape },
  },
};

// What the merge leg is shown of each proposed finding, as merge.md's
// `{{findings}}`: no severity and no confidence, which grouping does not
// turn on. `reportedBy` is the proposing leg's concern and `outsideConcern`
// says whether the finding came back in `outsideConcernFindings`.
export const MERGE_FINDING_FIELDS = ["id", "reportedBy", "outsideConcern", "title", "path", "line", "explanation", "dependsOn"];

// What the merge leg returns, on merge.md. A schema cannot say the rest of
// the contract: the grouping is usable only when every finding id it was
// shown sits in exactly one group and each group's `primary` is one of that
// group's `findingIds`. An answer that breaks either rule is no usable
// answer, and the leg counts as one that did not complete.
export const mergeSchema = {
  type: "object",
  additionalProperties: false,
  required: ["groups"],
  properties: {
    groups: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["findingIds", "primary", "sharedCause"],
        properties: {
          findingIds: { type: "array", minItems: 1, items: { type: "string" } },
          primary: { type: "string" },
          sharedCause: { type: "string" },
        },
      },
    },
  },
};

// What a verifier is shown of each finding, as verifier.md's `{{findings}}`:
// the claim of the group's primary write-up, and never a proposer's
// severity or confidence, so the verifier's grade is its own. `dependsOn`
// joins what every write-up in the group named. `alsoReportedAs` holds the
// group's other write-ups as `{ title, explanation }` and is left out for a
// group of one. `wholeScope` is the directory a whole-scope repository
// brief audits; it is left out for a finding about the change.
export const VERIFIER_FINDING_FIELDS = ["id", "title", "path", "line", "explanation", "dependsOn", "alsoReportedAs", "wholeScope"];

// One verifier verdict. `severity` is the verifier's own grade and the one
// the finding carries from here on; it is `None` exactly when the finding
// is refuted. `outsideFact` names a fact the verdict needs and the
// repository does not show, and `wouldSettle` says what would decide an
// unsettled finding; both are empty when they do not apply.
// finding-disposition.mjs owns what a verdict makes of its finding.
export const verifierVerdictShape = {
  type: "object",
  additionalProperties: false,
  required: ["findingId", "verdict", "confidence", "severity", "reason", "outsideFact", "wouldSettle"],
  properties: {
    findingId: { type: "string" },
    verdict: { type: "string", enum: ["confirmed", "refuted", "unsettled"] },
    confidence: { type: "integer", minimum: 0, maximum: 100 },
    severity: { type: "string", enum: [...SEVERITIES, "None"] },
    reason: { type: "string" },
    outsideFact: { type: "string" },
    wouldSettle: { type: "string" },
  },
};

// What a verifier leg returns, on verifier.md. A `newFindings` entry is a
// different defect the verifier established in the changed code. It is a
// proposed finding whose proposer is that verifier leg, and it reaches the
// author only with a verdict of its own from a leg other than the one that
// raised it.
export const verifierSchema = {
  type: "object",
  additionalProperties: false,
  required: ["verdicts", "newFindings", "outOfScopeObservations"],
  properties: {
    verdicts: { type: "array", items: verifierVerdictShape },
    newFindings: { type: "array", items: findingShape },
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
    mergeFindingFields: MERGE_FINDING_FIELDS,
    mergeSchema,
    verifierFindingFields: VERIFIER_FINDING_FIELDS,
    verifierVerdictShape,
    verifierSchema,
  }));
}
