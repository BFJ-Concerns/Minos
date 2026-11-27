// Repository description: the account Minos keeps of a repository's
// layout, its components and how they connect, written from the default
// branch at one commit. Reviewers are handed it as grounding beside the
// project's guidance, and the operator reads it on the web page.
//
// Unlike the other stages this one serves no pull request: it is asked
// when a repository's default branch has moved past the commit the kept
// description was made from, and its agent works in a clone of the
// repository checked out at the new commit. Its answer is the whole new
// description, which replaces the one kept. The stage is given that
// earlier description, where there is one, so that a small change to the
// repository costs a small revision and the text reviewers are shown does
// not shift between one reading of unchanged code and the next.

import {
  MAXIMUM_REPOSITORY_DESCRIPTION_CHARACTERS,
  descriptionProblem,
  isCommit,
} from "../repository-description.mjs";

export const stage = {
  name: "repository-description",
  role: "description",
  slots: ["commit", "earlier", "maximum-characters"],

  // {
  //   commit    the default-branch commit the clone is checked out at
  //   earlier   null, or { commit, description }: the description kept
  //             until now and the commit it was made from
  // }
  inputProblem(input) {
    if (!input || typeof input !== "object" || Array.isArray(input)) return "the input must be an object";
    if (!isCommit(input.commit)) return "commit must be a commit's full object name";
    if (input.earlier === null) return null;
    if (!input.earlier || typeof input.earlier !== "object" || Array.isArray(input.earlier))
      return "earlier must be null or the description kept until now";
    const problem = descriptionProblem(input.earlier.commit, input.earlier.description);
    return problem ? `earlier: ${problem}` : null;
  },

  // The earlier description is shown whole under the commit it was made
  // from, or as the bare `none`, so nothing reads as a description that
  // is not one.
  promptValues(input) {
    return {
      commit: input.commit,
      earlier: input.earlier === null
        ? "<earlier-description>none</earlier-description>"
        : `<earlier-description commit="${input.earlier.commit}">\n${input.earlier.description}\n</earlier-description>`,
      "maximum-characters": String(MAXIMUM_REPOSITORY_DESCRIPTION_CHARACTERS),
    };
  },

  answerSchema() {
    return {
      type: "object",
      additionalProperties: false,
      required: ["description"],
      properties: { description: { type: "string" } },
    };
  },

  answerProblem(answer, input) {
    return descriptionProblem(input.commit, answer.description);
  },
};
