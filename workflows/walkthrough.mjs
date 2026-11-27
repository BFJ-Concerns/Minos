// The walkthrough: one generated overview of a pull request's change,
// grouped by what changed with the files each group touched, with sequence
// diagrams inside it where the repository enables them. It is an extra: it
// costs model time, gates nothing and decides nothing, and a run is never
// failed on it.
//
// Two stages write it (workflows/judgements): `walkthrough` reads the
// change, writes the overview and the groups and names the flows worth
// drawing, and `walkthrough-diagrams` traces those flows through the code.
// The closing duty run-steps/write-walkthrough.mjs asks them once the head
// is finished, so neither the verdict nor the head's markers wait for
// them, and puts the result into the summary comment as its `walkthrough`
// part (summary-comment.mjs).
//
// What the two stages returned is kept as one record:
//
//   {
//     "kind": "minos-walkthrough-v1",
//     "head": "", "target": "",   the change described: target...head
//     "reviewBase": "",           where the review of that head read from:
//                                 the target for a review of the whole
//                                 change, an earlier head for an increment
//     "overview": "",
//     "groups": [{ "title": "", "summary": "", "files": [""] }],
//     "diagrams": [{ "title": "", "participants": [""],
//                    "steps": [{ "from": "", "to": "", "message": "", "reply": false }] }],
//     "models": { "walkthrough": "", "diagrams": "" }
//   }
//
// A walkthrough always describes the whole pull request, whatever range
// the review read, and says which range that was. The service keeps the
// last record published for each pull request beside the runs and hands a
// run its path (RECORD_VARIABLE). Every write of the summary comment shows
// the kept walkthrough under the head it was made from, until the run has
// published one of its own: a head still being reviewed, a head that
// needed no review and a head whose run stopped short all show the last
// one made, and a head that is reviewed gets a new one, revised from the
// kept one. The record is a cache: losing it costs the display of the
// earlier walkthrough until the next reviewed head, and nothing else.
//
// A diagram is data, never Mermaid a model wrote: participants and the
// steps between them, which renderDiagram turns into a Mermaid sequence
// diagram in a fenced block, the one form both GitHub and Forgejo draw in
// a comment and one that still reads as text where it is not drawn. A
// diagram's source therefore cannot fail to parse on what a model wrote.
// One the checks here refuse is left out and the walkthrough goes out
// without it.

import { chmodSync, mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";

import { codeSpan, short } from "./finding-presentation.mjs";
import { isCommit } from "./repository-description.mjs";

export const RECORD_KIND = "minos-walkthrough-v1";

// The repository's two settings, as the service exports them to a run: one
// JSON value, `{ "enabled": false, "diagrams": false }`, from
// `walkthrough.enabled` and `walkthrough.diagrams`. Both are off unless
// enabled. The same two are the named toggles the output settings list for
// these elements: an element that costs model time has one switch, which
// says both whether it is made and whether it is posted.
export const SETTINGS_VARIABLE = "MINOS_WALKTHROUGH";
// The path of the record the service keeps for this pull request. Unset,
// nothing is kept and each walkthrough is written from the change alone.
export const RECORD_VARIABLE = "MINOS_WALKTHROUGH_RECORD";

// The longest walkthrough the summary comment carries, as rendered,
// diagrams included. GitHub refuses a comment of more than 65,536
// characters, the smaller of the two forges' limits, and the walkthrough
// shares the comment with the head's state, the review's sections and the
// earlier findings that stand, none of which may be crowded out by it.
export const MAXIMUM_WALKTHROUGH_CHARACTERS = 30_000;
// The longest Mermaid source one diagram may come to. A forge draws a
// diagram only up to a source length of its own (Forgejo's
// `[markup] MERMAID_MAX_SOURCE_CHARACTERS`), and a sequence too long to
// take in explains nothing.
export const MAXIMUM_DIAGRAM_CHARACTERS = 4_000;

// The repository's settings. An absent variable is both off, as when a run
// is started by hand. Diagrams sit within the walkthrough, so they are off
// wherever it is. A value that is not the two booleans throws.
export function walkthroughSettings(env = process.env) {
  const text = env[SETTINGS_VARIABLE];
  if (text === undefined || text === "") return { enabled: false, diagrams: false };
  let settings;
  try {
    settings = JSON.parse(text);
  } catch (error) {
    throw new Error(`${SETTINGS_VARIABLE} is not valid JSON (${error.message})`);
  }
  if (!settings || typeof settings.enabled !== "boolean" || typeof settings.diagrams !== "boolean")
    throw new Error(`${SETTINGS_VARIABLE} must hold the booleans enabled and diagrams`);
  return { enabled: settings.enabled, diagrams: settings.enabled && settings.diagrams };
}

function blank(value) {
  return typeof value !== "string" || value.trim() === "";
}

// What is wrong with an overview and its groups, or null. The
// `walkthrough` stage's answer and a record are both held to this.
export function groupsProblem(overview, groups) {
  if (blank(overview)) return "the overview is blank";
  if (!Array.isArray(groups) || groups.length === 0) return "a walkthrough has at least one group";
  for (const group of groups) {
    if (!group || blank(group.title) || blank(group.summary)) return "a group needs a title and a summary";
    if (!Array.isArray(group.files) || group.files.length === 0 || group.files.some((file) => typeof file !== "string" || file === ""))
      return `the group ${JSON.stringify(group.title)} names no files, or an empty path`;
  }
  return null;
}

// What is wrong with a diagram, or null: a title, at least two
// participants under distinct names, and at least one step, each between
// two of those participants and saying what passes. `reply` marks a step
// that answers an earlier one, which is drawn dashed.
export function diagramProblem(diagram) {
  if (!diagram || blank(diagram.title)) return "a diagram needs a title";
  const { participants, steps } = diagram;
  if (!Array.isArray(participants) || participants.length < 2 || participants.some(blank))
    return "a diagram needs at least two named participants";
  if (new Set(participants).size !== participants.length) return "a diagram's participants need distinct names";
  if (!Array.isArray(steps) || steps.length === 0) return "a diagram needs at least one step";
  for (const step of steps) {
    if (!step || !participants.includes(step.from) || !participants.includes(step.to))
      return "a step runs between two of the diagram's participants";
    if (blank(step.message)) return "a step says what passes";
    if (typeof step.reply !== "boolean") return "a step says whether it is a reply";
  }
  if (diagramSource(diagram).length > MAXIMUM_DIAGRAM_CHARACTERS)
    return `the diagram's source is longer than ${MAXIMUM_DIAGRAM_CHARACTERS} characters`;
  return null;
}

// What is wrong with a record, or null. Only a record this passes is
// published, kept or shown.
export function walkthroughProblem(record) {
  if (!record || typeof record !== "object" || Array.isArray(record) || record.kind !== RECORD_KIND)
    return `not a ${RECORD_KIND} record`;
  for (const name of ["head", "target", "reviewBase"])
    if (!isCommit(record[name])) return `${name} must be a commit's full object name`;
  const problem = groupsProblem(record.overview, record.groups);
  if (problem) return problem;
  if (!Array.isArray(record.diagrams)) return "diagrams must be a list";
  for (const diagram of record.diagrams) {
    const diagramIssue = diagramProblem(diagram);
    if (diagramIssue) return diagramIssue;
  }
  if (!record.models || blank(record.models.walkthrough)) return "a walkthrough names the model that wrote it";
  if (record.diagrams.length > 0 && blank(record.models.diagrams)) return "a walkthrough with diagrams names the model that traced them";
  if (renderWalkthrough(record, record.head).length > MAXIMUM_WALKTHROUGH_CHARACTERS)
    return `the walkthrough is longer than ${MAXIMUM_WALKTHROUGH_CHARACTERS} characters as rendered`;
  return null;
}

// Text on one line of a Mermaid sequence diagram: a participant's label
// or a step's message. A line break would end the statement, as `;`
// would; `#` opens an entity code; `<` and `>` would be read as markup;
// and a backtick could close the fence the diagram sits in. The four
// characters go in as entity codes, which Mermaid draws as the characters.
function diagramText(text) {
  return text.trim().replace(/\s+/g, " ").replace(/`/g, "'").replace(/[#;<>]/g, (character) => `#${character.charCodeAt(0)};`);
}

// The diagram's Mermaid source. Participants take generated identifiers,
// so no name a model chose is ever read as Mermaid syntax.
function diagramSource({ participants, steps }) {
  const id = new Map(participants.map((name, index) => [name, `p${index + 1}`]));
  return [
    "sequenceDiagram",
    ...participants.map((name) => `    participant ${id.get(name)} as ${diagramText(name)}`),
    ...steps.map(({ from, to, message, reply }) =>
      `    ${id.get(from)}${reply ? "-->>" : "->>"}${id.get(to)}: ${diagramText(message)}`),
  ].join("\n");
}

// A group's files as the comment carries them: listed where they are few,
// and folded under their count where a list would bury the groups after
// it. Both forges draw `<details>` in a comment.
const LISTED_FILES = 8;

function fileList(files) {
  const list = files.map((file) => `- ${codeSpan(file)}`).join("\n");
  return files.length <= LISTED_FILES ? list : `<details>\n<summary>${files.length} files</summary>\n\n${list}\n\n</details>`;
}

// A diagram as the comment carries it: its title, then its source in a
// `mermaid` fence.
export function renderDiagram(diagram) {
  return [`**${diagram.title.trim()}**`, "", "```mermaid", diagramSource(diagram), "```"].join("\n");
}

// The walkthrough as the summary comment's part, for a comment that
// speaks of `head`. One shown under a later head than its own says which
// head it describes. The closing lines say what the review of its head
// read, which may be less than the walkthrough covers, and name the
// models, as every model-authored comment does.
export function renderWalkthrough(record, head) {
  const { overview, groups, diagrams, models } = record;
  const whole = record.reviewBase === record.target;
  const read = whole
    ? `The review read the whole change against \`${short(record.target)}\`.`
    : `The review read what changed since \`${short(record.reviewBase)}\`; this walkthrough covers the whole pull request.`;
  const written = diagrams.length > 0
    ? `Written by \`${models.walkthrough}\`; diagrams traced by \`${models.diagrams}\`.`
    : `Written by \`${models.walkthrough}\`.`;
  return [
    record.head === head ? "### Walkthrough" : `### Walkthrough of \`${short(record.head)}\``,
    overview.trim(),
    ...groups.map(({ title, summary, files }) =>
      [`**${title.trim()}**`, summary.trim(), fileList(files)].join("\n\n")),
    ...diagrams.map(renderDiagram),
    `${read} ${written}`,
  ].join("\n\n");
}

// The record without what does not fit, and what was left out, for the
// duty to report: a diagram its check refuses, and then every diagram
// where the walkthrough is too long with them. A record that is still
// refused is returned as `problem` and is not to be published.
export function fitted(record) {
  const omitted = [];
  let diagrams = record.diagrams.filter((diagram) => {
    const problem = diagramProblem(diagram);
    if (problem) omitted.push(`The diagram ${JSON.stringify(diagram?.title ?? "")} was left out: ${problem}.`);
    return !problem;
  });
  let problem = walkthroughProblem({ ...record, diagrams });
  if (problem && diagrams.length > 0 && !walkthroughProblem({ ...record, diagrams: [] })) {
    omitted.push(`The walkthrough's diagrams were left out: ${problem}.`);
    diagrams = [];
    problem = null;
  }
  return { record: { ...record, diagrams }, omitted, problem };
}

// The walkthrough kept for this pull request, or null: where the
// repository has it off, where the service keeps none, and where the
// record cannot be read or is not whole, since a cache that cannot be read
// is a cache lost. Where the repository has diagrams off, it comes without
// the diagrams it was drawn with. This never throws. Every write of the summary comment
// calls it, the write that records a verdict among them, and what a
// comment shows beside the verdict must never cost the verdict's write.
export function keptWalkthrough(env = process.env) {
  try {
    const settings = walkthroughSettings(env);
    if (!settings.enabled || !env[RECORD_VARIABLE]) return null;
    const record = JSON.parse(readFileSync(env[RECORD_VARIABLE], "utf8"));
    if (walkthroughProblem(record)) return null;
    return settings.diagrams ? record : { ...record, diagrams: [] };
  } catch {
    return null;
  }
}

// Keeps a published record as the pull request's walkthrough, whole or
// not at all. Where the service names no path, nothing is kept.
export function keepWalkthrough(env, record) {
  const path = env[RECORD_VARIABLE];
  if (!path) return;
  const directory = dirname(path);
  const partial = `${path}.partial`;
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  chmodSync(directory, 0o700);
  writeFileSync(partial, `${JSON.stringify(record, null, 2)}\n`, { mode: 0o600 });
  chmodSync(partial, 0o600);
  renameSync(partial, path);
}
