// Reads the shipped review prompts for the input builders. Each prompt is
// its Markdown file under workflows/review-briefs with every file slot
// filled: the shared sections, and for a specialist its concern's body. The
// run slots stay for the workflow to fill in one pass, once the values it
// alone knows exist, so that a value is never itself read for slots.
// workflows/review-briefs/README.md is the slot contract this module and
// the workflows keep.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

export const PROMPT_DIRECTORY = "workflows/review-briefs";

const SHARED_SECTIONS = ["reviewed-change", "writing-a-finding", "severity-scale"];
const REVIEWED_CHANGE_SLOTS = ["target", "head", "project-documents", "pull-request"];

// The run slots each stage's prompt may carry, after its file slots are
// filled. The workflows fill exactly these.
export const STAGE_RUN_SLOTS = {
  exploration: [...REVIEWED_CHANGE_SLOTS, "concerns"],
  specialist: [...REVIEWED_CHANGE_SLOTS, "concern-name", "other-concerns", "planned-focus", "same-concern-units"],
  repository: [...REVIEWED_CHANGE_SLOTS, "brief-path", "brief", "assignment"],
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

function checkedPrompt(stage, label, text) {
  const unknown = slotNames(text).filter((name) => !STAGE_RUN_SLOTS[stage].includes(name));
  if (unknown.length > 0)
    throw new Error(`review prompt ${label} carries slots its stage never fills: ${unknown.join(", ")}`);
  return text;
}

// The concerns, in name order, and every stage's prompt with its file slots
// filled. Any slot a stage does not fill is refused here, before a run
// receives the prompt.
export function reviewPrompts(repositoryRoot) {
  const directory = join(repositoryRoot, PROMPT_DIRECTORY);
  const read = (name) => readFileSync(join(directory, name), "utf8").trim();
  const shared = Object.fromEntries(SHARED_SECTIONS.map((name) => [name, read(`${name}.md`)]));
  const concerns = readdirSync(join(directory, "concerns"))
    .filter((entry) => entry.endsWith(".md"))
    .sort()
    .map((entry) => parseConcern(entry.slice(0, -".md".length), read(`concerns/${entry}`)));
  if (concerns.length === 0) throw new Error("no review concerns are shipped");
  const prompt = (stage, file, extra = {}) =>
    checkedPrompt(stage, file, fillFileSlots(read(file), { ...shared, ...extra }));
  return {
    concerns: concerns.map(({ name, summary }) => ({ name, summary })),
    prompts: {
      exploration: prompt("exploration", "exploration.md"),
      specialist: Object.fromEntries(concerns.map((concern) =>
        [concern.name, prompt("specialist", "specialist.md", { concern: concern.body })])),
      repository: prompt("repository", "repository.md"),
      merge: prompt("merge", "merge.md"),
      verifier: prompt("verifier", "verifier.md"),
    },
  };
}
