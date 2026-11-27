// The summary comment's text: the one comment Minos keeps on a pull
// request, created at the first claim and edited in place ever after.
//
// The text is made of parts. A part is one element of the comment under a
// name, rendered from the facts a run holds about the head: PARTS maps
// each name to its renderer, `(facts) => Markdown`, which returns "" when
// it has nothing to say. The template
// (publication-templates/summary-comment.md) places each part by a
// `{{name}}` slot. A new element of the comment is therefore one renderer
// here and one slot in the template; turning an element off is naming it
// in `hidden`; and a repository's own layout is another template text.
//
// Every write composes the whole text from the facts given. Nothing is
// read back out of the comment as it stands, so a part shows only what
// its writer handed over.
//
// The record that binds the comment's state to a head is not composed
// here. `minos forge summary` writes it as the comment's first line, from
// the state and head the command is given (run-steps/forge-writes.mjs).

import { readFileSync } from "node:fs";

import { plural, short } from "./finding-presentation.mjs";
import { fillPrompt, slotNames } from "./review-prompts.mjs";
import { CHANGES_REQUESTED, CLEAN, INCOMPLETE } from "./run-endings.mjs";

// What the comment says of the head it names. The three a run can end on
// are the endings' own names (run-endings.mjs); these two are shown while
// a run is at work. `minos forge summary` takes the same five words.
export const INSPECTING = "inspecting";
export const REVIEWING = "reviewing";
export const STATES = [INSPECTING, REVIEWING, CLEAN, CHANGES_REQUESTED, INCOMPLETE];

// The kinds of section a review has.
export const CONCERN = "concern";
export const BRIEF = "brief";

const HEADLINE = {
  [INSPECTING]: "looking at what changed",
  [REVIEWING]: "review in progress",
  [CLEAN]: "no blocking findings",
  [CHANGES_REQUESTED]: "changes requested",
  [INCOMPLETE]: "review incomplete",
};

// The head under review and its state. `condition` is why a run stopped
// short, in words the author can read; it is shown only on an incomplete
// head.
function state({ state, head, target, condition }) {
  const lines = [`**${capitalised(HEADLINE[state])}.**`, "", `Head \`${short(head)}\` against target \`${short(target)}\`.`];
  if (state === INCOMPLETE) {
    lines.push("", condition
      ? `This head has not been judged: ${sentence(condition)}`
      : "This head has not been judged.");
  }
  return lines.join("\n");
}

// The review's sections, one row each: a specialist concern or a brief,
// with the count of findings it returned, that it found none, or why it
// was skipped. A skipped section is never shown as one that found none,
// and one that was skipped and still returned a finding, as a specialist
// may find something outside its concern, shows both.
// Absent or empty, as before a review has run and on a head that needed
// none, the part says nothing.
function sections({ sections = [] }) {
  if (sections.length === 0) return "";
  const rows = sections.map(({ kind, name, findings, skipped }) => {
    const label = kind === BRIEF ? `Brief \`${cell(name)}\`` : cell(name);
    const count = findings === undefined || findings === 0 ? undefined : String(findings);
    const result = skipped === undefined ? count ?? "none"
      : count === undefined ? `skipped: ${cell(skipped)}` : `${count} (skipped: ${cell(skipped)})`;
    return `| ${label} | ${result} |`;
  });
  return ["| Section | Findings |", "|---|---|", ...rows].join("\n");
}

// The gating findings of earlier reviews that still stand, as
// settle-earlier-findings settles them: where each sits and the head it
// was raised on.
function outstanding({ outstanding = [] }) {
  if (outstanding.length === 0) return "";
  return [
    `**${plural(outstanding.length, "earlier blocking finding")} still outstanding:**`, "",
    ...outstanding.map(({ path, line, head, title }) =>
      `- ${title ? codeSpan(title) + " at " : ""}${codeSpan(`${path}:${line}`)}, raised at \`${short(head)}\``),
  ].join("\n");
}

// Literal titles and paths may themselves contain Markdown code spans.
function codeSpan(text) {
  const value = text.replace(/[\r\n]+/g, " ");
  const longest = Math.max(0, ...[...value.matchAll(/`+/g)].map(([run]) => run.length));
  const fence = "`".repeat(longest + 1);
  return longest === 0 ? `${fence}${value}${fence}` : `${fence} ${value} ${fence}`;
}

export const PARTS = { state, sections, outstanding };

// Text inside one table cell: a bar would end the cell and a line break
// the row.
function cell(text) {
  return String(text).replace(/\s*\n\s*/g, " ").replace(/\|/g, "\\|");
}

function capitalised(text) {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

function sentence(text) {
  const trimmed = String(text).trim();
  return /[.!?]$/.test(trimmed) ? trimmed : `${trimmed}.`;
}

// The shipped template's text.
export function shippedTemplate() {
  return readFileSync(new URL("./publication-templates/summary-comment.md", import.meta.url), "utf8");
}

function checkHead({ state, head, target } = {}) {
  if (!STATES.includes(state)) throw new Error(`the summary comment has no state ${JSON.stringify(state)}`);
  for (const [name, sha] of [["head", head], ["target", target]])
    if (typeof sha !== "string" || sha === "") throw new Error(`the summary comment needs the ${name} it speaks of`);
}

function checkFacts(facts) {
  checkHead(facts);
  const { sections = [], outstanding = [] } = facts;
  if (!Array.isArray(sections) || !Array.isArray(outstanding))
    throw new Error("the summary comment's sections and outstanding findings are lists");
  for (const section of sections) {
    const counted = Number.isInteger(section?.findings) && section.findings >= 0;
    const skipped = typeof section?.skipped === "string" && section.skipped.trim() !== "";
    const malformed = (section?.findings !== undefined && !counted) || (section?.skipped !== undefined && !skipped);
    if (![CONCERN, BRIEF].includes(section?.kind) || typeof section.name !== "string" || section.name === "" || malformed || !(counted || skipped))
      throw new Error(`a summary section names a concern or a brief and its count of findings, why it was skipped, or both: ${JSON.stringify(section)}`);
  }
  for (const finding of outstanding)
    if (typeof finding?.path !== "string" || !Number.isInteger(finding.line) || typeof finding.head !== "string")
      throw new Error(`an outstanding finding names its path, line and head: ${JSON.stringify(finding)}`);
}

// The comment's text for `facts`: `state` (one of STATES), `head` and
// `target`, and whatever else the parts read: `condition`, `sections`
// (each `{ kind, name }` with `findings` or `skipped`) and `outstanding`.
// `template` is the layout and `hidden` the names of parts to leave out.
// Facts a part cannot render and a template slot that names no part both
// throw: the caller decides what a comment that cannot be composed costs
// the run.
export function composeSummary(facts, { template = shippedTemplate(), hidden = [] } = {}) {
  checkFacts(facts);
  const unknown = [...slotNames(template), ...hidden].filter((name) => !Object.hasOwn(PARTS, name));
  if (unknown.length > 0) throw new Error(`the summary comment has no part named ${unknown.join(", ")}`);
  const rendered = Object.fromEntries(Object.entries(PARTS).map(([name, render]) =>
    [name, hidden.includes(name) ? "" : render(facts)]));
  return fillPrompt(template, rendered).replace(/\n{3,}/g, "\n\n").trim();
}

// The comment's text with no template: the state part alone, which says
// what the comment records of the head. A write whose text cannot be
// composed goes out with this, so a layout that fails never costs the
// record its verdict (run-steps/forge-writes.mjs).
export function plainSummary(facts) {
  checkHead(facts);
  const { state: name, head, target, condition } = facts;
  return state({ state: name, head, target, condition: typeof condition === "string" ? condition : undefined });
}
