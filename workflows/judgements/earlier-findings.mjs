// Earlier findings: whether each blocking finding of an earlier Minos
// review still stands at this head. The driver step has already set aside
// what is settled mechanically (a resolved thread), so each finding the
// stage is shown needs reading: a rejection from the author, or the defect
// gone from the code. Its agent reads the code in the clone.

import { REVIEW_BODY, THREAD } from "../earlier-minos-findings.mjs";

export const STANDS = "stands";
export const AUTHOR_REJECTED = "author-rejected";
export const DEFECT_GONE = "defect-gone";
export const OUTCOMES = [STANDS, AUTHOR_REJECTED, DEFECT_GONE];

function findingProblem(finding) {
  if (!finding || typeof finding !== "object" || Array.isArray(finding)) return "each finding must be an object";
  for (const field of ["key", "path", "head", "finding"])
    if (typeof finding[field] !== "string" || finding[field] === "") return `a finding's ${field} must be a non-empty string`;
  if (typeof finding.line !== "number") return `finding ${finding.key} needs its line`;
  if (finding.where === THREAD) {
    if (finding.comments !== undefined) return `thread finding ${finding.key} carries replies, not comments`;
    if (!Array.isArray(finding.replies) || finding.replies.some((reply) =>
      !reply || typeof reply.byAuthor !== "boolean" || typeof reply.body !== "string"))
      return `thread finding ${finding.key} needs its replies as { byAuthor, body }`;
    return null;
  }
  if (finding.where === REVIEW_BODY) {
    if (finding.replies !== undefined) return `review-body finding ${finding.key} carries comments, not replies`;
    if (!Array.isArray(finding.comments) || finding.comments.some((comment) => typeof comment !== "string"))
      return `review-body finding ${finding.key} needs the author's later comments as strings`;
    return null;
  }
  return `finding ${finding.key} must sit in a thread or a review body`;
}

export const stage = {
  name: "earlier-findings",
  slots: ["head", "findings"],

  // {
  //   head       the commit the pull request stands at
  //   findings   the blocking findings not settled mechanically, each
  //              { key, path, line, head, finding, where } with, on a
  //              thread finding, its replies as { byAuthor, body } and, on
  //              a review-body finding, the author's pull-request comments
  //              made after that review, as strings
  // }
  inputProblem(input) {
    if (!input || typeof input !== "object" || Array.isArray(input)) return "the input must be an object";
    if (typeof input.head !== "string" || input.head === "") return "head must be a commit";
    if (!Array.isArray(input.findings) || input.findings.length === 0) return "findings must be a non-empty list";
    const keys = new Set();
    for (const finding of input.findings) {
      const problem = findingProblem(finding);
      if (problem) return problem;
      if (keys.has(finding.key)) return `findings name ${finding.key} twice`;
      keys.add(finding.key);
    }
    return null;
  },

  promptValues(input) {
    return { head: input.head, findings: JSON.stringify(input.findings, null, 2) };
  },

  answerSchema(input) {
    return {
      type: "object",
      additionalProperties: false,
      required: ["findings"],
      properties: {
        findings: {
          type: "array",
          items: {
            type: "object",
            additionalProperties: false,
            required: ["key", "outcome", "reason"],
            properties: {
              key: { type: "string", enum: input.findings.map((finding) => finding.key) },
              outcome: { type: "string", enum: OUTCOMES },
              reason: { type: "string" },
            },
          },
        },
      },
    };
  },

  // One answer for every finding shown, each with its reason, since the
  // reason is the record of why a block was lifted or kept.
  answerProblem(answer, input) {
    const heads = new Map(input.findings.map(({ key, head }) => [key, head]));
    const answered = new Set();
    for (const entry of answer.findings) {
      if (answered.has(entry.key)) return `finding ${entry.key} is answered twice`;
      answered.add(entry.key);
      if (entry.outcome === DEFECT_GONE && heads.get(entry.key) === input.head)
        return `finding ${entry.key} was raised on this head and cannot have changed`;
      if (typeof entry.reason !== "string" || entry.reason.trim() === "") return `the reason for ${entry.key} is blank`;
    }
    const missing = input.findings.map((finding) => finding.key).filter((key) => !answered.has(key));
    return missing.length === 0 ? null : `no answer for ${missing.join(", ")}`;
  },
};
