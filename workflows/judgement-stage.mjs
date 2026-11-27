// What a judgement stage is, for everything that runs one. A judgement
// stage is a single agent asked one question whose answer decides a run's
// course or its published result: the run driver gathers the stage's input,
// the stage answers, and the driver carries the answer out. A stage is two
// files of one name under workflows/judgements: NAME.md, the prompt, and
// NAME.mjs, whose `stage` export is its definition:
//
//   name                          the stage's name, which is also the name
//                                 it is dispatched and recorded under
//   slots                         the run slots its prompt carries
//   inputProblem(input)           what is wrong with an input, or null
//   promptValues(input)           the value of each run slot
//   answerSchema(input)           the JSON schema of the answer
//   answerProblem(answer, input)  what is wrong with an answer that the
//                                 schema cannot say, or null
//
// A definition reads no forge and no clock. Everything a stage is given is
// its input, a plain JSON value, and the clone its agent works in. That is
// what lets a stage be run again on a recorded input
// (replay-judgement.mjs), and the record a run keeps of each judgement is
// exactly such an input with the answer beside it.
// workflows/judgements/README.md is the list of stages.

import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { fillFileSlots, fillPrompt, sharedSections, slotNames } from "./review-prompts.mjs";
import { routingFromEnvironment } from "./role-routing.mjs";

export const JUDGEMENTS_DIRECTORY = "workflows/judgements";
// The workflow every stage runs through, and the runner dispatch-stage
// launches it with, as names under the workflows directory.
export const JUDGEMENT_WORKFLOW = "judgement.js";
export const JUDGEMENT_RUNNER = "run-judgement";
export const RECORD_KIND = "minos-judgement-v1";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const STAGE_NAME = /^[a-z]+(-[a-z]+)*$/;

// The definition of the stage of this name.
export async function loadStage(name) {
  if (typeof name !== "string" || !STAGE_NAME.test(name)) throw new Error(`${JSON.stringify(name)} is not a judgement stage name`);
  let module;
  try {
    module = await import(`./judgements/${name}.mjs`);
  } catch (error) {
    if (error.code === "ERR_MODULE_NOT_FOUND") throw new Error(`no judgement stage is named ${name}`);
    throw error;
  }
  const stage = module.stage;
  if (
    !stage || stage.name !== name || !Array.isArray(stage.slots) ||
    ["inputProblem", "promptValues", "answerSchema", "answerProblem"].some((part) => typeof stage[part] !== "function")
  ) throw new Error(`workflows/judgements/${name}.mjs does not export a complete stage definition`);
  return stage;
}

// The stage's prompt file with its file slots filled: the sections it
// shares with the review prompts. A slot the stage never fills is refused
// here, before anything is dispatched.
function promptTemplate(stage) {
  const text = readFileSync(join(repositoryRoot, JUDGEMENTS_DIRECTORY, `${stage.name}.md`), "utf8").trim();
  const template = fillFileSlots(text, sharedSections(repositoryRoot));
  const unknown = slotNames(template).filter((name) => !stage.slots.includes(name));
  if (unknown.length > 0)
    throw new Error(`judgement prompt ${stage.name}.md carries slots its stage never fills: ${unknown.join(", ")}`);
  return template;
}

// The arguments the judgement workflow takes for one execution of a stage:
// the input checked, the prompt complete, the answer's schema and the
// routing. Run slots are filled in one pass, so a value is never itself
// read for slots. The input rides along untouched: the workflow ignores
// it, and the arguments then say on their own what the stage was asked.
export function judgementArgs(stage, input, { env = process.env } = {}) {
  const problem = stage.inputProblem(input);
  if (problem) throw new Error(`${stage.name} input: ${problem}`);
  return {
    stage: stage.name,
    input,
    prompt: fillPrompt(promptTemplate(stage), stage.promptValues(input)),
    schema: stage.answerSchema(input),
    routing: routingFromEnvironment(env).judgement,
  };
}

// The stage's answer out of what the runner printed. The runtime has
// already held the answer to its schema; what the schema cannot say is
// checked here. An unusable answer throws: a stage with no answer has
// judged nothing, and nothing downstream may act as though it had.
export function answerFrom(stage, input, result) {
  if (!result || result.stage !== stage.name || !result.answer || typeof result.answer !== "object")
    throw new Error(`stage ${stage.name} gave no answer`);
  const problem = stage.answerProblem(result.answer, input);
  if (problem) throw new Error(`stage ${stage.name} gave an unusable answer: ${problem}`);
  return result.answer;
}

// What a run keeps of one judgement: what the stage was given, what it
// answered and the routing it answered on. replay-judgement.mjs takes a
// record and puts its input to the stage again. `answer` may equally hold
// a choice made some other way on the same input, which a replay is then
// compared with.
export function judgementRecord(stage, input, answer, routing) {
  return { kind: RECORD_KIND, stage: stage.name, input, answer, routing };
}
