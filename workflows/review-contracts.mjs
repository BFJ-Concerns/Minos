// What each stage of the review workflows returns: the exploration plan,
// the specialist and general reviewers' answers, the merge and verifier
// schemas every workflow shares, what the merge and verifier legs are shown
// of a finding, and the verifier batch size. The input
// builders attach these as `args.contracts` and each workflow validates
// them before dispatching anything; neither keeps a copy.

export const VERIFIER_BATCH_SIZE = 6;

// How many rounds of verification a workflow runs. The first checks the
// proposed findings; each later one checks the findings verifiers raised in
// the round before, merged first. A finding raised in the last round has no
// verdict, so a run that reaches the limit with one is incomplete.
export const VERIFICATION_ROUNDS = 3;

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

// What the exploration leg returns, on exploration.md: the changed-file
// inventory and the plan, one unit per specialist dispatch, each naming one
// of the shipped concerns.
export function explorationSchema(concernNames) {
  return {
    type: "object",
    additionalProperties: false,
    required: ["files", "plan", "applicability"],
    properties: {
      applicability: {
        type: "object",
        additionalProperties: false,
        required: ["reason"],
        properties: { reason: { type: "string" } },
      },
      files: {
        type: "array",
        items: {
          type: "object",
          additionalProperties: false,
          required: ["path", "added", "deleted"],
          properties: {
            path: { type: "string" },
            added: { type: "integer", minimum: 0 },
            deleted: { type: "integer", minimum: 0 },
          },
        },
      },
      plan: {
        type: "array",
        items: {
          type: "object",
          additionalProperties: false,
          required: ["id", "concern", "scope", "specialistType"],
          properties: {
            id: { type: "string" },
            concern: { type: "string" },
            scope: { type: "array", minItems: 1, items: { type: "string" } },
            specialistType: { type: "string", enum: [...concernNames] },
          },
        },
      },
    },
  };
}

// What a proposing leg returns, on specialist.md or repository.md.
// `applicability` speaks for the leg's own concern alone. A problem of
// another concern that the change introduced comes back in
// `outsideConcernFindings`, whatever the applicability, and is a proposed
// finding like any in `findings`: merged and verified the same way. An
// out-of-scope observation is a defect the change did not introduce; it is
// recorded and never verified. A whole-scope repository brief's own
// finding in code the change never touched is not one: it stays in
// `findings`.
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

// One share of a change divided among general reviewers, as
// general-assignment/whole-change.md asks for it: the changed `paths` its
// reviewer answers for and, in `focus`, a sentence saying what that part of
// the change does. A share is what its reviewer must get through and never
// a bound on what it reads or reports.
export const shareShape = {
  type: "object",
  additionalProperties: false,
  required: ["id", "focus", "paths"],
  properties: {
    id: { type: "string" },
    focus: { type: "string" },
    paths: { type: "array", minItems: 1, items: { type: "string" } },
  },
};

// What a general reviewer returns, on general.md, for the share of a
// divided change it was given. A general reviewer has no concern, so it
// answers for no applicability and has no finding outside one: every
// problem the change introduced is in `findings`, each a proposed finding
// merged and verified like a specialist's.
export const generalSchema = {
  type: "object",
  additionalProperties: false,
  required: ["findings", "outOfScopeObservations"],
  properties: {
    findings: { type: "array", items: findingShape },
    outOfScopeObservations: { type: "array", items: outOfScopeObservationShape },
  },
};

// What the general reviewer given the whole change returns: a general
// reviewer's answer and, in `shares`, the division it made of a change it
// judged too large for one reader. Two or more shares are a division, and
// a reviewer is then sent for each on generalSchema, which has no `shares`,
// so a change is divided once. Fewer than two are none: the answer's
// `findings` are then the review. A dividing answer's `findings`, where it
// has any, are proposed all the same.
export const generalWholeChangeSchema = {
  ...generalSchema,
  required: ["shares", ...generalSchema.required],
  properties: { shares: { type: "array", items: shareShape }, ...generalSchema.properties },
};

// What the merge leg is shown of each proposed finding, as merge.md's
// `{{findings}}`: no severity and no confidence, which grouping does not
// turn on. `reportedBy` names what the proposing leg read for: a
// specialist's concern, a brief's title, `general` for a quick review's
// general reviewer, or `verification` for a finding a verifier raised.
// `outsideConcern` says whether the finding came back in
// `outsideConcernFindings`, and is false for a reviewer with no concern.
// Nor is it shown `wholeScope` or `brief`: a group carries the scope and
// brief of any whole-scope write-up in it, whichever is primary, so
// grouping need not weigh them.
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
// group of one. `brief` is the path in the reviewed repository of the
// `.review/` brief the finding was made under, whatever the brief's
// extent, which the verifier reads to judge the finding under the brief's
// rule; it is left out for a finding of the main review. `wholeScope` is
// the directory a whole-scope brief audits, which tells the verifier that
// untouched code there is within the review where it is a fault under that
// brief; it is left out for a finding about the change.
export const VERIFIER_FINDING_FIELDS = ["id", "title", "path", "line", "explanation", "dependsOn", "alsoReportedAs", "brief", "wholeScope"];

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

// One finding a verifier raises, as verifier.md's `newFindings` takes it: a
// proposed finding and, in `foundWhileChecking`, the id of the finding in
// its batch that the verifier was checking when it established the defect.
// The name is how a raised finding comes by a whole scope, which a verifier
// never states itself: the finding is whole-scope exactly when the one it
// names carries `wholeScope` and its own `path` is under that directory,
// and it then carries that directory into the next round. A name that
// matches no finding of the batch gives no scope, and the finding is judged
// as a claim about the change. The name is provenance and no part of the
// write-up the author reads.
export const raisedFindingShape = {
  ...findingShape,
  required: [...findingShape.required, "foundWhileChecking"],
  properties: { ...findingShape.properties, foundWhileChecking: { type: "string" } },
};

// What a verifier leg returns, on verifier.md. A `newFindings` entry is a
// different defect the verifier established in the changed code, or under
// the directory of a whole-scope finding it was checking. It is a proposed
// finding whose proposer is that verifier leg, and it reaches the author
// only with a verdict of its own from a leg other than the one that raised
// it.
export const verifierSchema = {
  type: "object",
  additionalProperties: false,
  required: ["verdicts", "newFindings", "outOfScopeObservations"],
  properties: {
    verdicts: { type: "array", items: verifierVerdictShape },
    newFindings: { type: "array", items: raisedFindingShape },
    outOfScopeObservations: { type: "array", items: outOfScopeObservationShape },
  },
};

// The `args.contracts` value, as the builders emit it: plain JSON. The
// exploration schema's concerns are the shipped concern names.
export function reviewContracts(concernNames) {
  if (!Array.isArray(concernNames) || concernNames.length === 0)
    throw new Error("the review contracts need the shipped concern names");
  return JSON.parse(JSON.stringify({
    verifierBatchSize: VERIFIER_BATCH_SIZE,
    verificationRounds: VERIFICATION_ROUNDS,
    explorationSchema: explorationSchema(concernNames),
    findingShape,
    raisedFindingShape,
    outOfScopeObservationShape,
    applicabilityShape,
    specialistSchema,
    shareShape,
    generalSchema,
    generalWholeChangeSchema,
    mergeFindingFields: MERGE_FINDING_FIELDS,
    mergeSchema,
    verifierFindingFields: VERIFIER_FINDING_FIELDS,
    verifierVerdictShape,
    verifierSchema,
  }));
}

// A workflow's check of the `contracts` its input builder attached from
// reviewContracts: the set, or null when it is missing or malformed, which
// fails the workflow before dispatch.
export function contractsFromInput(input) {
  const contracts = input && input.contracts;
  if (!contracts || typeof contracts !== "object" || Array.isArray(contracts)) return null;
  if (!Number.isInteger(contracts.verifierBatchSize) || contracts.verifierBatchSize < 1) return null;
  if (!Number.isInteger(contracts.verificationRounds) || contracts.verificationRounds < 1) return null;
  for (const name of ["mergeFindingFields", "verifierFindingFields"])
    if (!Array.isArray(contracts[name]) || !contracts[name].includes("id")) return null;
  for (const name of [
    "explorationSchema",
    "findingShape",
    "raisedFindingShape",
    "outOfScopeObservationShape",
    "applicabilityShape",
    "specialistSchema",
    "shareShape",
    "generalSchema",
    "generalWholeChangeSchema",
    "mergeSchema",
    "verifierVerdictShape",
    "verifierSchema",
  ]) {
    const shape = contracts[name];
    if (
      !shape || typeof shape !== "object" || Array.isArray(shape) ||
      shape.type !== "object" ||
      !Array.isArray(shape.required) ||
      !shape.properties || typeof shape.properties !== "object" || Array.isArray(shape.properties)
    ) return null;
  }
  return contracts;
}
