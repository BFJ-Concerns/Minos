import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import test from "node:test";

import { BRIEF, CONCERN, INSPECTING, PARTS, REVIEWING, STATES, composeSummary, plainSummary, shippedTemplate } from "./summary-comment.mjs";
import { slotNames } from "./review-prompts.mjs";
import { CHANGES_REQUESTED, CLEAN, INCOMPLETE } from "./run-endings.mjs";

const head = "a".repeat(40);
const target = "b".repeat(40);
const raisedOn = "c".repeat(40);

test("the shipped template places every part once and nothing else", () => {
  assert.deepEqual(slotNames(shippedTemplate()).sort(), Object.keys(PARTS).sort());
});

test("every state names the head it speaks of", () => {
  for (const state of STATES) {
    const text = composeSummary({ state, head, target });
    assert.match(text, /^\*\*[A-Z][a-z ]+\.\*\*\n\nHead `aaaaaaa` against target `bbbbbbb`\./, state);
    assert.doesNotMatch(text, /Minos/, state);
  }
  assert.notEqual(composeSummary({ state: INSPECTING, head, target }), composeSummary({ state: REVIEWING, head, target }));
});

test("a judged head shows its sections and the earlier findings that stand", () => {
  const text = composeSummary({
    state: CHANGES_REQUESTED, head, target,
    sections: [
      { kind: CONCERN, name: "correctness", findings: 2 },
      { kind: CONCERN, name: "security", findings: 0 },
      { kind: BRIEF, name: ".review/api.md", findings: 1 },
      { kind: BRIEF, name: ".review/docs.md", skipped: "no changed path\nis in its scope | none" },
      { kind: CONCERN, name: "failure", skipped: "nothing fails here", findings: 1 },
    ],
    outstanding: [{ path: "lexer.go", line: 5, head: raisedOn }],
  });
  assert.equal(text, [
    "**Changes requested.**", "",
    "Head `aaaaaaa` against target `bbbbbbb`.", "",
    "| Section | Findings |", "|---|---|",
    "| correctness | 2 |",
    "| security | none |",
    "| Brief `.review/api.md` | 1 |",
    "| Brief `.review/docs.md` | skipped: no changed path is in its scope \\| none |",
    "| failure | 1 (skipped: nothing fails here) |", "",
    "**1 earlier blocking finding still outstanding:**", "",
    "- `lexer.go:5`, raised at `ccccccc`",
  ].join("\n"));
});

test("a part with nothing to say leaves no gap", () => {
  assert.equal(composeSummary({ state: CLEAN, head, target }),
    "**No blocking findings.**\n\nHead `aaaaaaa` against target `bbbbbbb`.");
});

test("an incomplete head shows why it was not judged", () => {
  assert.match(composeSummary({ state: INCOMPLETE, head, target, condition: "a verifier did not finish" }),
    /\n\nThis head has not been judged: a verifier did not finish\.$/);
  assert.match(composeSummary({ state: INCOMPLETE, head, target }), /\n\nThis head has not been judged\.$/);
  assert.doesNotMatch(composeSummary({ state: CLEAN, head, target, condition: "left over" }), /left over/);
});

test("a template lays the parts out and a hidden part is left out", () => {
  const facts = { state: CLEAN, head, target, sections: [{ kind: CONCERN, name: "correctness", findings: 0 }] };
  const text = composeSummary(facts, { template: "{{sections}}\n\nOur house note.\n\n{{state}}\n" });
  assert.match(text, /^\| Section \| Findings \|\n\|---\|---\|\n\| correctness \| none \|\n\nOur house note\.\n\n\*\*No blocking findings\.\*\*/);
  assert.doesNotMatch(composeSummary(facts, { hidden: ["sections"] }), /Section/);
});

test("a part's text is never read for slots", () => {
  const text = composeSummary({ state: CLEAN, head, target, sections: [{ kind: BRIEF, name: "{{state}}", findings: 0 }] });
  assert.match(text, /\| Brief `\{\{state\}\}` \| none \|/);
});

test("facts the parts cannot render and slots that name no part are refused", () => {
  assert.throws(() => composeSummary({ state: "approved", head, target }), /no state "approved"/);
  assert.throws(() => composeSummary({ state: CLEAN, head: "", target }), /needs the head/);
  assert.throws(() => composeSummary({ state: CLEAN, head, target, sections: [{ kind: CONCERN, name: "x" }] }), /its count of findings, why it was skipped, or both/);
  assert.throws(() => composeSummary({ state: CLEAN, head, target, sections: [{ kind: CONCERN, name: "x", findings: -1, skipped: "why" }] }), /its count of findings/);
  assert.throws(() => composeSummary({ state: CLEAN, head, target, sections: [{ kind: CONCERN, name: "x", findings: 1, skipped: " " }] }), /its count of findings/);
  assert.throws(() => composeSummary({ state: CLEAN, head, target, sections: [{ kind: "stage", name: "x", findings: 1 }] }), /names a concern or a brief/);
  assert.throws(() => composeSummary({ state: CLEAN, head, target, outstanding: [{ path: "a.go" }] }), /names its path, line and head/);
  assert.throws(() => composeSummary({ state: CLEAN, head, target }, { template: "{{state}}\n\n{{walkthrough}}" }), /no part named walkthrough/);
  assert.throws(() => composeSummary({ state: CLEAN, head, target }, { hidden: ["walkthrough"] }), /no part named walkthrough/);
});

test("the plain text is the headline alone and needs no template", () => {
  assert.equal(plainSummary({ state: CHANGES_REQUESTED, head, target, sections: "unreadable" }),
    "**Changes requested.**\n\nHead `aaaaaaa` against target `bbbbbbb`.");
  assert.match(plainSummary({ state: INCOMPLETE, head, target, condition: "a verifier did not finish" }),
    /This head has not been judged: a verifier did not finish\.$/);
  assert.throws(() => plainSummary({ state: "approved", head, target }), /no state "approved"/);
});
