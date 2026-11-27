// Walkthrough: the overview of a pull request's change that the summary
// comment carries where the repository enables it (../walkthrough.mjs). The
// stage's agent reads the whole change in the clone, target to head,
// whatever range the review of this head read, and returns the overview and
// the groups the change falls into, each with the files it touched. Where
// the repository also enables diagrams it names the flows worth drawing,
// which the `walkthrough-diagrams` stage then traces.
//
// The stage is given nothing a review made: no plan, no sections and no
// findings. A review in either mode can therefore be followed by one, and a
// walkthrough never repeats or leaks a verdict. It is given the walkthrough
// kept from an earlier head of the pull request, where there is one, so
// that a push costs a revision and the account of what the push left alone
// keeps its wording.

import { pullRequestFromInput, pullRequestSection } from "../pull-request-description.mjs";
import { repositoryDescriptionFromInput, repositoryDescriptionSection } from "../repository-description.mjs";
import { MAXIMUM_WALKTHROUGH_CHARACTERS, groupsProblem } from "../walkthrough.mjs";

function blank(value) {
  return typeof value !== "string" || value.trim() === "";
}

const text = { type: "string" };

export const stage = {
  name: "walkthrough",
  role: "walkthrough",
  slots: ["target", "head", "changed-files", "repository-description", "pull-request", "earlier", "diagrams", "maximum-characters"],

  // {
  //   target, head            the commits bounding the pull request
  //   files                   every path the change touches, a rename under
  //                           both its paths (changed-paths.mjs)
  //   pullRequest,            as a review's input builder attaches them
  //   repositoryDescription
  //   earlier                 null, or { head, overview, groups }: the
  //                           walkthrough kept from an earlier head
  //   diagrams                whether the repository enables diagrams
  // }
  inputProblem(input) {
    if (!input || typeof input !== "object" || Array.isArray(input)) return "the input must be an object";
    for (const name of ["target", "head"])
      if (blank(input[name])) return `${name} must be a commit`;
    if (!Array.isArray(input.files) || input.files.length === 0 || input.files.some(blank))
      return "files must list the paths the change touches";
    if (typeof input.diagrams !== "boolean") return "diagrams must be a boolean";
    if (input.earlier === null) return null;
    if (!input.earlier || blank(input.earlier.head)) return "earlier must be null or the walkthrough kept from an earlier head";
    const problem = groupsProblem(input.earlier.overview, input.earlier.groups);
    return problem ? `earlier: ${problem}` : null;
  },

  // The earlier walkthrough is shown whole under the head it was made
  // from, or as the bare `none`, so nothing reads as a walkthrough that is
  // not one.
  promptValues(input) {
    const { earlier } = input;
    return {
      target: input.target,
      head: input.head,
      "changed-files": JSON.stringify(input.files),
      "repository-description": repositoryDescriptionSection(repositoryDescriptionFromInput(input)),
      "pull-request": pullRequestSection(pullRequestFromInput(input)),
      earlier: earlier === null
        ? "<earlier-walkthrough>none</earlier-walkthrough>"
        : `<earlier-walkthrough head="${earlier.head}">\n${JSON.stringify({ overview: earlier.overview, groups: earlier.groups }, null, 2)}\n</earlier-walkthrough>`,
      diagrams: input.diagrams ? "enabled" : "not enabled",
      "maximum-characters": String(MAXIMUM_WALKTHROUGH_CHARACTERS),
    };
  },

  // The answer has `flows` only where the repository enables diagrams.
  answerSchema(input) {
    return {
      type: "object",
      additionalProperties: false,
      required: ["overview", "groups", ...(input.diagrams ? ["flows"] : [])],
      properties: {
        overview: text,
        groups: {
          type: "array",
          items: {
            type: "object",
            additionalProperties: false,
            required: ["title", "summary", "files"],
            properties: { title: text, summary: text, files: { type: "array", items: text } },
          },
        },
        ...(input.diagrams ? {
          flows: {
            type: "array",
            items: {
              type: "object",
              additionalProperties: false,
              required: ["title", "trace"],
              properties: { title: text, trace: text },
            },
          },
        } : {}),
      },
    };
  },

  // The groups account for the change's files exactly: each names only
  // paths the change touches, and every such path is in at least one. A
  // flow is known to the diagrams stage by its title, so titles differ.
  answerProblem(answer, input) {
    const problem = groupsProblem(answer.overview, answer.groups);
    if (problem) return problem;
    const named = new Set(answer.groups.flatMap((group) => group.files));
    const stray = [...named].find((file) => !input.files.includes(file));
    if (stray !== undefined) return `a group names ${JSON.stringify(stray)}, which the change does not touch`;
    const missing = input.files.find((file) => !named.has(file));
    if (missing !== undefined) return `no group names ${JSON.stringify(missing)}, which the change touches`;
    const flows = answer.flows ?? [];
    if (flows.some((flow) => blank(flow.title) || blank(flow.trace))) return "a flow needs a title and a trace";
    if (new Set(flows.map((flow) => flow.title)).size !== flows.length) return "two flows share a title";
    return null;
  },
};
