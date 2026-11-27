// Push inspection: how much review a head needs, decided before any review
// runs. The stage is given the pull request as a review is (its bounding
// commits, the project's guidance and the author's description), what is
// known of the earlier head Minos finished on (`minos forge earlier`), and
// the outcomes this run may choose among, which the driver narrowed by
// what is mechanical before asking. Its agent reads the change itself in
// the clone.

import { projectGuidanceFromInput, projectGuidanceSection } from "../orientation-guidance.mjs";
import { pullRequestFromInput, pullRequestSection } from "../pull-request-description.mjs";
import { REVIEWED_CHANGE_SLOTS } from "../review-prompts.mjs";

// The three outcomes, in the order a prompt lists them.
export const NO_REVIEW = "no-review";
export const INCREMENT = "increment";
export const FULL = "full";
export const OUTCOMES = [NO_REVIEW, INCREMENT, FULL];

const EARLIER_OUTCOMES = ["clean", "attention"];

function earlierProblem(earlier) {
  if (!earlier || typeof earlier !== "object" || Array.isArray(earlier)) return "earlier must be an object";
  if (earlier.outcome === "none") return null;
  if (!EARLIER_OUTCOMES.includes(earlier.outcome)) return "earlier.outcome must be clean, attention or none";
  if (typeof earlier.head !== "string" || earlier.head === "") return "an earlier head needs its commit";
  if (typeof earlier.ancestor !== "boolean") return "earlier.ancestor must be a boolean";
  if (
    earlier.changedPaths !== null &&
    !(Array.isArray(earlier.changedPaths) && earlier.changedPaths.every((path) => typeof path === "string" && path !== ""))
  ) return "earlier.changedPaths must be a list of paths, or null where the heads could not be compared";
  return null;
}

// The earlier head as the prompt shows it: the bare `none` where there is
// no earlier head, so nothing reads as a fact about a head that is not one.
function earlierForPrompt(earlier) {
  if (earlier.outcome === "none") return { outcome: "none" };
  const { head, outcome, ancestor, changedPaths } = earlier;
  return { head, outcome, ancestor, changedPaths };
}

export const stage = {
  name: "push-inspection",
  slots: [...REVIEWED_CHANGE_SLOTS, "earlier", "outcomes"],

  // {
  //   target, head            the commits bounding the pull request
  //   guidance, pullRequest   as a review's input builder attaches them
  //   earlier                 { outcome: "none" }, or { head, outcome,
  //                           ancestor, changedPaths } with the outcome
  //                           clean or attention
  //   outcomes                the outcomes this run may choose among
  // }
  inputProblem(input) {
    if (!input || typeof input !== "object" || Array.isArray(input)) return "the input must be an object";
    for (const name of ["target", "head"])
      if (typeof input[name] !== "string" || input[name] === "") return `${name} must be a commit`;
    if (!projectGuidanceFromInput(input)) return "the reviewed project's guidance is missing or malformed";
    const earlier = earlierProblem(input.earlier);
    if (earlier) return earlier;
    const outcomes = input.outcomes;
    if (
      !Array.isArray(outcomes) || outcomes.length === 0 ||
      outcomes.some((outcome) => !OUTCOMES.includes(outcome)) ||
      new Set(outcomes).size !== outcomes.length
    ) return `outcomes must be a non-empty list drawn once each from ${OUTCOMES.join(", ")}`;
    // An increment is reviewed from the earlier head, so it can be offered
    // only where that head can be the base of a range ending at this one.
    if (outcomes.includes(INCREMENT) && !(input.earlier.outcome !== "none" && input.earlier.ancestor))
      return "increment may be offered only with an earlier head that is an ancestor of this one";
    return null;
  },

  promptValues(input) {
    return {
      target: input.target,
      head: input.head,
      "project-documents": projectGuidanceSection(projectGuidanceFromInput(input)),
      "pull-request": pullRequestSection(pullRequestFromInput(input)),
      earlier: JSON.stringify(earlierForPrompt(input.earlier), null, 2),
      outcomes: OUTCOMES.filter((outcome) => input.outcomes.includes(outcome)).map((outcome) => `- \`${outcome}\``).join("\n"),
    };
  },

  // The answer can name only an outcome this run may choose.
  answerSchema(input) {
    return {
      type: "object",
      additionalProperties: false,
      required: ["outcome", "reason"],
      properties: {
        outcome: { type: "string", enum: OUTCOMES.filter((outcome) => input.outcomes.includes(outcome)) },
        reason: { type: "string" },
      },
    };
  },

  answerProblem(answer) {
    return typeof answer.reason === "string" && answer.reason.trim() !== ""
      ? null
      : "the reason is blank, and it is the record of why the head got the review it did";
  },
};
