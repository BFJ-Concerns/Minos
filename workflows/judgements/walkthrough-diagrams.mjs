// Walkthrough diagrams: the sequences a walkthrough's diagrams show. The
// `walkthrough` stage names the flows worth drawing, and this stage's agent
// traces each through the code at the head and returns it as participants
// and the steps between them. It returns data and never Mermaid:
// ../walkthrough.mjs checks each diagram and renders its source, so one
// diagram that cannot be drawn is left out without costing the others or
// the walkthrough.
//
// It runs on a role of its own, so that a deployment can route the reading
// a diagram takes apart from the writing of the walkthrough.

import { repositoryDescriptionFromInput, repositoryDescriptionSection } from "../repository-description.mjs";
import { MAXIMUM_DIAGRAM_CHARACTERS } from "../walkthrough.mjs";

function blank(value) {
  return typeof value !== "string" || value.trim() === "";
}

const text = { type: "string" };

export const stage = {
  name: "walkthrough-diagrams",
  role: "walkthrough-diagrams",
  slots: ["target", "head", "repository-description", "flows", "maximum-characters"],

  // {
  //   target, head            the commits bounding the pull request
  //   repositoryDescription   as a review's input builder attaches it
  //   flows                   [{ title, trace }], as the walkthrough stage
  //                           returned them: at least one, titles distinct
  // }
  inputProblem(input) {
    if (!input || typeof input !== "object" || Array.isArray(input)) return "the input must be an object";
    for (const name of ["target", "head"])
      if (blank(input[name])) return `${name} must be a commit`;
    const { flows } = input;
    if (!Array.isArray(flows) || flows.length === 0 || flows.some((flow) => !flow || blank(flow.title) || blank(flow.trace)))
      return "flows must list at least one flow, each with a title and a trace";
    if (new Set(flows.map((flow) => flow.title)).size !== flows.length) return "two flows share a title";
    return null;
  },

  promptValues(input) {
    return {
      target: input.target,
      head: input.head,
      "repository-description": repositoryDescriptionSection(repositoryDescriptionFromInput(input)),
      flows: JSON.stringify(input.flows.map(({ title, trace }) => ({ title, trace })), null, 2),
      "maximum-characters": String(MAXIMUM_DIAGRAM_CHARACTERS),
    };
  },

  // One entry for each flow. `undrawn` is empty on a flow that was drawn
  // and says why on one that was not, whose participants and steps are
  // then empty.
  answerSchema() {
    return {
      type: "object",
      additionalProperties: false,
      required: ["diagrams"],
      properties: {
        diagrams: {
          type: "array",
          items: {
            type: "object",
            additionalProperties: false,
            required: ["flow", "participants", "steps", "undrawn"],
            properties: {
              flow: text,
              participants: { type: "array", items: text },
              steps: {
                type: "array",
                items: {
                  type: "object",
                  additionalProperties: false,
                  required: ["from", "to", "message", "reply"],
                  properties: { from: text, to: text, message: text, reply: { type: "boolean" } },
                },
              },
              undrawn: text,
            },
          },
        },
      },
    };
  },

  // Every flow is answered once and nothing else is. Whether a drawn
  // diagram can be rendered is not judged here: that check is made on each
  // diagram alone, where one refusal leaves the rest standing.
  answerProblem(answer, input) {
    const asked = input.flows.map((flow) => flow.title);
    const answered = answer.diagrams.map((diagram) => diagram.flow);
    const unknown = answered.find((title) => !asked.includes(title));
    if (unknown !== undefined) return `the answer draws ${JSON.stringify(unknown)}, which is not one of the flows`;
    if (new Set(answered).size !== answered.length) return "a flow is answered twice";
    const missing = asked.find((title) => !answered.includes(title));
    if (missing !== undefined) return `the flow ${JSON.stringify(missing)} has no entry`;
    return null;
  },
};

// The diagrams an answer drew, as a walkthrough record holds them: each
// under its flow's title, in the order the flows were named. A flow the
// stage left undrawn has none.
export function drawnDiagrams(answer, flows) {
  return flows.flatMap(({ title }) => {
    const entry = answer.diagrams.find((diagram) => diagram.flow === title);
    return blank(entry.undrawn) ? [{ title, participants: entry.participants, steps: entry.steps }] : [];
  });
}
