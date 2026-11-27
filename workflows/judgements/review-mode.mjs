// Review mode: which review a head gets, where the repository's
// `review.mode` setting is `automatic`. The stage is asked once the push
// inspection has settled the range the review reads, and is given that
// range as a review is (its bounding commits, the project's guidance and
// the author's description). Its agent reads the change itself in the
// clone and chooses between the two modes (review-mode.mjs).

import { projectGuidanceFromInput, projectGuidanceSection } from "../orientation-guidance.mjs";
import { pullRequestFromInput, pullRequestSection } from "../pull-request-description.mjs";
import { repositoryDescriptionFromInput, repositoryDescriptionSection } from "../repository-description.mjs";
import { MODES } from "../review-mode.mjs";
import { REVIEWED_CHANGE_SLOTS } from "../review-prompts.mjs";

export const stage = {
  name: "review-mode",
  slots: [...REVIEWED_CHANGE_SLOTS],

  // {
  //   target, head            the commits bounding the range the review
  //                           will read: the earlier head for an
  //                           increment, the pull request's target for a
  //                           full review
  //   guidance, pullRequest,  as a review's input builder attaches them
  //   repositoryDescription
  // }
  inputProblem(input) {
    if (!input || typeof input !== "object" || Array.isArray(input)) return "the input must be an object";
    for (const name of ["target", "head"])
      if (typeof input[name] !== "string" || input[name] === "") return `${name} must be a commit`;
    if (!projectGuidanceFromInput(input)) return "the reviewed project's guidance is missing or malformed";
    return null;
  },

  promptValues(input) {
    return {
      target: input.target,
      head: input.head,
      "project-documents": projectGuidanceSection(projectGuidanceFromInput(input)),
      "repository-description": repositoryDescriptionSection(repositoryDescriptionFromInput(input)),
      "pull-request": pullRequestSection(pullRequestFromInput(input)),
    };
  },

  answerSchema() {
    return {
      type: "object",
      additionalProperties: false,
      required: ["mode", "reason"],
      properties: {
        mode: { type: "string", enum: [...MODES] },
        reason: { type: "string" },
      },
    };
  },

  answerProblem(answer) {
    return typeof answer.reason === "string" && answer.reason.trim() !== ""
      ? null
      : "the reason is blank, and it is the run report's record of why the head got the review it did";
  },
};
