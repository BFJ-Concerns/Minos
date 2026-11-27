// The shipped review prompts and their slots, for both sides of a run. An
// input builder reads each prompt through reviewPrompts: its Markdown file
// under workflows/review-briefs with every file slot filled, from the shared
// sections, the passages the repository's settings choose
// (prompt-settings.mjs) and for a specialist its concern's body. A workflow
// checks what it was handed with promptFits and fills the run slots with
// fillPrompt, in one pass, once the values it alone knows exist, so that a
// value is never itself read for slots. workflows/review-briefs/README.md
// is the slot contract, and STAGE_RUN_SLOTS here is its one list.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { settingSections } from "./prompt-settings.mjs";

export const PROMPT_DIRECTORY = "workflows/review-briefs";

const SHARED_SECTIONS = ["reviewed-change", "writing-a-finding", "severity-scale"];
const REVIEWED_CHANGE_SLOTS = ["target", "head", "project-documents", "pull-request"];

// The run slots each stage's prompt may carry, after its file slots are
// filled. The workflows fill exactly these.
export const STAGE_RUN_SLOTS = {
  exploration: [...REVIEWED_CHANGE_SLOTS, "concerns"],
  specialist: [...REVIEWED_CHANGE_SLOTS, "concern-name", "other-concerns", "planned-focus", "same-concern-units"],
  repository: [...REVIEWED_CHANGE_SLOTS, "brief-path", "brief", "assignment"],
  "brief-relevance": [...REVIEWED_CHANGE_SLOTS, "changed-paths", "relevance-conditions"],
  "brief-partition": [...REVIEWED_CHANGE_SLOTS, "brief-path", "brief", "file-inventory"],
  assignment: ["target", "head", "brief-directory", "unit-concern", "unit-files"],
  scope: [...REVIEWED_CHANGE_SLOTS, "concerns", "changed-files"],
  merge: ["target", "head", "findings"],
  verifier: [...REVIEWED_CHANGE_SLOTS, "findings"],
};

const SLOT = /\{\{([a-z-]+)\}\}/g;

export function slotNames(text) {
  return [...new Set([...text.matchAll(SLOT)].map((match) => match[1]))];
}

// A concern file opens with front matter carrying only `summary`.
export function parseConcern(name, text) {
  const match = text.match(/^---\n([\s\S]*?)\n---\n/);
  if (!match) throw new Error(`concern ${name} has no front matter`);
  const fields = new Map(match[1].split("\n").map((line) => {
    const separator = line.indexOf(":");
    if (separator === -1) throw new Error(`concern ${name} front matter line is not a key: ${line}`);
    return [line.slice(0, separator).trim(), line.slice(separator + 1).trim()];
  }));
  const summary = fields.get("summary");
  if (fields.size !== 1 || !summary) throw new Error(`concern ${name} front matter must carry exactly a non-empty summary`);
  const body = text.slice(match[0].length).trim();
  if (body === "") throw new Error(`concern ${name} has no body`);
  return { name, summary, body };
}

function fillFileSlots(text, files) {
  return text.replace(SLOT, (whole, name) => (Object.hasOwn(files, name) ? files[name] : whole));
}

// Whether a workflow may send this text for a stage: a non-empty prompt
// carrying no slot but the stage's run slots.
export function promptFits(text, stage) {
  return typeof text === "string" && text.trim() !== "" &&
    slotNames(text).every((name) => STAGE_RUN_SLOTS[stage].includes(name));
}

// The prompt with every run slot filled from `values`. A slot with no
// value throws, so a prompt is never sent with a slot still in it.
export function fillPrompt(template, values) {
  return template.replace(SLOT, (whole, name) => {
    if (!Object.hasOwn(values, name)) throw new Error(`prompt slot ${whole} has no value`);
    return values[name];
  });
}

function checkedPrompt(stage, label, text) {
  const unknown = slotNames(text).filter((name) => !STAGE_RUN_SLOTS[stage].includes(name));
  if (unknown.length > 0)
    throw new Error(`review prompt ${label} carries slots its stage never fills: ${unknown.join(", ")}`);
  return text;
}

// The concerns, in name order, and every stage's prompt with its file slots
// filled, as they read under `settings`, the run's value for every prompt
// setting. Any slot a stage does not fill is refused here, before a run
// receives the prompt.
export function reviewPrompts(repositoryRoot, settings) {
  const directory = join(repositoryRoot, PROMPT_DIRECTORY);
  const read = (name) => readFileSync(join(directory, name), "utf8").trim();
  const shared = Object.fromEntries(SHARED_SECTIONS.map((name) => [name, read(`${name}.md`)]));
  const chosen = settingSections(read, settings);
  const concerns = readdirSync(join(directory, "concerns"))
    .filter((entry) => entry.endsWith(".md"))
    .sort()
    .map((entry) => parseConcern(entry.slice(0, -".md".length), read(`concerns/${entry}`)));
  if (concerns.length === 0) throw new Error("no review concerns are shipped");
  const prompt = (stage, file, extra = {}) =>
    checkedPrompt(stage, file, fillFileSlots(read(file), { ...shared, ...chosen, ...extra }));
  return {
    concerns: concerns.map(({ name, summary }) => ({ name, summary })),
    prompts: {
      exploration: prompt("exploration", "exploration.md"),
      specialist: Object.fromEntries(concerns.map((concern) =>
        [concern.name, prompt("specialist", "specialist.md", { concern: concern.body })])),
      repository: prompt("repository", "repository.md"),
      briefRelevance: prompt("brief-relevance", "brief-relevance.md"),
      briefPartition: prompt("brief-partition", "brief-partition.md"),
      assignment: {
        changedPart: prompt("assignment", "assignment/changed-part.md"),
        wholePart: prompt("assignment", "assignment/whole-part.md"),
        unit: prompt("assignment", "assignment/unit.md"),
        files: prompt("assignment", "assignment/files.md"),
      },
      scope: prompt("scope", "scope.md"),
      merge: prompt("merge", "merge.md"),
      verifier: prompt("verifier", "verifier.md"),
    },
  };
}

// A workflow's check of the concerns its input builder attached from
// reviewPrompts: each with a distinct name and a summary, or null.
export function concernsFromInput(input) {
  const concerns = input && input.concerns;
  if (!Array.isArray(concerns) || concerns.length === 0) return null;
  const names = new Set();
  for (const concern of concerns) {
    if (
      !concern || typeof concern.name !== "string" || concern.name === "" ||
      typeof concern.summary !== "string" || concern.summary === "" ||
      names.has(concern.name)
    ) return null;
    names.add(concern.name);
  }
  return concerns;
}

// A concern as the planning and scope prompts list it: its name and the
// summary that completes "reads the change for …".
export function concernLine(concern) {
  return `- \`${concern.name}\`: ${concern.summary}`;
}
