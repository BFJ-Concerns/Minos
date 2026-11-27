import "./isolate-from-live-run.mjs";
import assert from "node:assert/strict";
import { chmodSync, existsSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";

import { operationRoot } from "./temporary-directory-fixture.mjs";
import {
  MAXIMUM_DIAGRAM_CHARACTERS, MAXIMUM_WALKTHROUGH_CHARACTERS, RECORD_KIND, RECORD_VARIABLE, SETTINGS_VARIABLE,
  diagramProblem, fitted, keepWalkthrough, keptWalkthrough, renderDiagram, renderWalkthrough, walkthroughProblem,
  walkthroughSettings,
} from "./walkthrough.mjs";

const target = "1".repeat(40);
const head = "2".repeat(40);
const earlier = "3".repeat(40);

const diagram = {
  title: "A request is parsed",
  participants: ["Client", "Parser"],
  steps: [
    { from: "Client", to: "Parser", message: "parse(text)", reply: false },
    { from: "Parser", to: "Client", message: "tokens", reply: true },
  ],
};

function record(overrides = {}) {
  return {
    kind: RECORD_KIND,
    head,
    target,
    reviewBase: target,
    overview: "The parser learns to read empty tokens.",
    groups: [{ title: "Parsing", summary: "Empty tokens are kept.", files: ["parser.go", "parser_test.go"] }],
    diagrams: [],
    models: { walkthrough: "claude-opus-5-5", diagrams: "gpt-6.1-sol" },
    ...overrides,
  };
}

const enabled = (extra = {}) => ({ [SETTINGS_VARIABLE]: '{"enabled":true,"diagrams":false}', ...extra });

test("the settings are both off where the service exports none, and diagrams are off wherever the walkthrough is", () => {
  assert.deepEqual(walkthroughSettings({}), { enabled: false, diagrams: false });
  assert.deepEqual(walkthroughSettings({ [SETTINGS_VARIABLE]: "" }), { enabled: false, diagrams: false });
  assert.deepEqual(walkthroughSettings({ [SETTINGS_VARIABLE]: '{"enabled":false,"diagrams":true}' }), { enabled: false, diagrams: false });
  assert.deepEqual(walkthroughSettings({ [SETTINGS_VARIABLE]: '{"enabled":true,"diagrams":true}' }), { enabled: true, diagrams: true });
});

test("settings that are not the two booleans are refused", () => {
  assert.throws(() => walkthroughSettings({ [SETTINGS_VARIABLE]: "{" }), /not valid JSON/);
  assert.throws(() => walkthroughSettings({ [SETTINGS_VARIABLE]: '{"enabled":"true","diagrams":false}' }), /must hold the booleans/);
  assert.throws(() => walkthroughSettings({ [SETTINGS_VARIABLE]: '{"enabled":true}' }), /must hold the booleans/);
});

test("a diagram needs distinct participants and steps between them that say what passes", () => {
  assert.equal(diagramProblem(diagram), null);
  assert.match(diagramProblem({ ...diagram, title: " " }), /needs a title/);
  assert.match(diagramProblem({ ...diagram, participants: ["Client"] }), /at least two named participants/);
  assert.match(diagramProblem({ ...diagram, participants: ["Client", "Client"] }), /distinct names/);
  assert.match(diagramProblem({ ...diagram, steps: [] }), /at least one step/);
  assert.match(diagramProblem({ ...diagram, steps: [{ from: "Client", to: "Store", message: "save", reply: false }] }), /between two of the diagram's participants/);
  assert.match(diagramProblem({ ...diagram, steps: [{ from: "Client", to: "Parser", message: "", reply: false }] }), /says what passes/);
  assert.match(diagramProblem({ ...diagram, steps: [{ from: "Client", to: "Parser", message: "parse" }] }), /whether it is a reply/);
});

test("a diagram whose source would be too long for a forge to draw is refused", () => {
  const long = { from: "Client", to: "Parser", message: "x".repeat(200), reply: false };
  const steps = Array.from({ length: Math.ceil(MAXIMUM_DIAGRAM_CHARACTERS / 200) + 1 }, () => long);
  assert.match(diagramProblem({ ...diagram, steps }), /longer than 4000 characters/);
});

test("a diagram renders as a fenced Mermaid sequence with generated identifiers and no model text read as syntax", () => {
  const rendered = renderDiagram({
    title: "Escaping",
    participants: ["participant A; B", "C <html>"],
    steps: [{ from: "participant A; B", to: "C <html>", message: "send #1\nthen `tick` ->> x", reply: true }],
  });
  assert.equal(rendered, [
    "**Escaping**",
    "",
    "```mermaid",
    "sequenceDiagram",
    "    participant p1 as participant A#59; B",
    "    participant p2 as C #60;html#62;",
    "    p1-->>p2: send #35;1 then 'tick' -#62;#62; x",
    "```",
  ].join("\n"));
});

test("a record is whole only with full commit names, groups, the models that wrote it and a length the comment carries", () => {
  assert.equal(walkthroughProblem(record()), null);
  assert.match(walkthroughProblem({ ...record(), kind: "other" }), /not a minos-walkthrough-v1 record/);
  assert.match(walkthroughProblem(record({ head: "abc1234" })), /head must be a commit's full object name/);
  assert.match(walkthroughProblem(record({ reviewBase: null })), /reviewBase must be/);
  assert.match(walkthroughProblem(record({ groups: [] })), /at least one group/);
  assert.match(walkthroughProblem(record({ groups: [{ title: "Parsing", summary: "Kept.", files: [] }] })), /names no files/);
  assert.match(walkthroughProblem(record({ models: { walkthrough: "" } })), /names the model that wrote it/);
  assert.match(walkthroughProblem(record({ diagrams: [diagram], models: { walkthrough: "claude-opus-5-5" } })), /names the model that traced them/);
  assert.match(walkthroughProblem(record({ overview: "x".repeat(MAXIMUM_WALKTHROUGH_CHARACTERS) })), /longer than 30000 characters as rendered/);
});

test("the walkthrough says which range the review read and names the models", () => {
  const whole = renderWalkthrough(record(), head);
  assert.match(whole, /^### Walkthrough\n\nThe parser learns to read empty tokens\.\n\n\*\*Parsing\*\*\n\nEmpty tokens are kept\.\n\n- `parser\.go`\n- `parser_test\.go`/);
  assert.match(whole, /The review read the whole change against `1111111`\. Written by `claude-opus-5-5`\.$/);

  const increment = renderWalkthrough(record({ reviewBase: earlier, diagrams: [diagram] }), head);
  assert.match(increment, /The review read what changed since `3333333`; this walkthrough covers the whole pull request\./);
  assert.match(increment, /Written by `claude-opus-5-5`; diagrams traced by `gpt-6\.1-sol`\.$/);
  assert.match(increment, /```mermaid\nsequenceDiagram\n/);
});

test("a walkthrough shown under a later head names the head it describes", () => {
  assert.match(renderWalkthrough(record(), "4".repeat(40)), /^### Walkthrough of `2222222`\n/);
});

test("a group's files are listed where they are few and folded under their count where they are many", () => {
  const files = Array.from({ length: 9 }, (_, index) => `file${index}.go`);
  const rendered = renderWalkthrough(record({ groups: [{ title: "Many", summary: "Many files.", files }] }), head);
  assert.match(rendered, /<details>\n<summary>9 files<\/summary>\n\n- `file0\.go`/);
  assert.doesNotMatch(renderWalkthrough(record(), head), /<details>/);
});

test("changed paths use a single code span even when they contain backticks or a newline", () => {
  const files = ["`[link](https://example.test) @reader``.go", "first\n- @reader", " name with spaces "];
  const rendered = renderWalkthrough(record({ groups: [{ title: "Paths", summary: "Changed files.", files }] }), head);
  assert.ok(rendered.includes("- ``` `[link](https://example.test) @reader``.go ```\n- `first - @reader`\n- `  name with spaces  `"));
});

test("fitting leaves out a diagram its check refuses and says so", () => {
  const broken = { ...diagram, title: "Broken", participants: ["Only"] };
  const { record: kept, omitted, problem } = fitted(record({ diagrams: [diagram, broken] }));
  assert.equal(problem, null);
  assert.deepEqual(kept.diagrams, [diagram]);
  assert.deepEqual(omitted, ['The diagram "Broken" was left out: a diagram needs at least two named participants.']);
});

test("fitting leaves out every diagram where the walkthrough is too long with them, and keeps the rest", () => {
  const overview = "x".repeat(MAXIMUM_WALKTHROUGH_CHARACTERS - 400);
  const { record: kept, omitted, problem } = fitted(record({ overview, diagrams: [diagram, { ...diagram, title: "Again" }] }));
  assert.equal(problem, null);
  assert.deepEqual(kept.diagrams, []);
  assert.equal(omitted.length, 1);
  assert.match(omitted[0], /^The walkthrough's diagrams were left out: the walkthrough is longer than 30000 characters as rendered\.$/);
});

test("fitting returns the problem of a walkthrough that is unfit without its diagrams", () => {
  const { problem } = fitted(record({ overview: " ", diagrams: [diagram] }));
  assert.equal(problem, "the overview is blank");
});

test("a kept walkthrough is read back whole, and is none where the setting is off", (t) => {
  const path = join(operationRoot(t, "minos-walkthrough-"), "records", "pr.json");
  keepWalkthrough({ [RECORD_VARIABLE]: path }, record());
  assert.equal(existsSync(`${path}.partial`), false);
  assert.deepEqual(keptWalkthrough(enabled({ [RECORD_VARIABLE]: path })), record());
  assert.equal(keptWalkthrough({ [RECORD_VARIABLE]: path }), null);
  assert.equal(keptWalkthrough(enabled()), null);
});

test("the walkthrough directory and record are private, including after a partial write", (t) => {
  const directory = join(operationRoot(t, "minos-walkthrough-"), "records");
  const path = join(directory, "pr.json");
  const env = { [RECORD_VARIABLE]: path };

  keepWalkthrough(env, record());
  assert.equal(statSync(directory).mode & 0o777, 0o700);
  assert.equal(statSync(path).mode & 0o777, 0o600);

  chmodSync(directory, 0o755);
  writeFileSync(`${path}.partial`, "interrupted write");
  chmodSync(`${path}.partial`, 0o644);
  keepWalkthrough(env, record());
  assert.equal(statSync(directory).mode & 0o777, 0o700);
  assert.equal(statSync(path).mode & 0o777, 0o600);
  assert.equal(existsSync(`${path}.partial`), false);
});

test("a kept walkthrough comes without its diagrams where the repository has diagrams off", (t) => {
  const path = join(operationRoot(t, "minos-walkthrough-"), "pr.json");
  keepWalkthrough({ [RECORD_VARIABLE]: path }, record({ diagrams: [diagram] }));
  const off = keptWalkthrough(enabled({ [RECORD_VARIABLE]: path }));
  assert.deepEqual(off.diagrams, []);
  assert.doesNotMatch(renderWalkthrough(off, "4".repeat(40)), /mermaid|diagrams traced/);
  const on = keptWalkthrough({ [SETTINGS_VARIABLE]: '{"enabled":true,"diagrams":true}', [RECORD_VARIABLE]: path });
  assert.deepEqual(on.diagrams, [diagram]);
});

test("a kept walkthrough that cannot be read or is not whole is none, and reading it never throws", (t) => {
  const root = operationRoot(t, "minos-walkthrough-");
  const path = join(root, "pr.json");
  assert.equal(keptWalkthrough(enabled({ [RECORD_VARIABLE]: path })), null);
  writeFileSync(path, "not json");
  assert.equal(keptWalkthrough(enabled({ [RECORD_VARIABLE]: path })), null);
  writeFileSync(path, JSON.stringify(record({ head: "short" })));
  assert.equal(keptWalkthrough(enabled({ [RECORD_VARIABLE]: path })), null);
  assert.equal(keptWalkthrough({ [SETTINGS_VARIABLE]: "{", [RECORD_VARIABLE]: path }), null);
});

test("keeping a walkthrough where the service names no path keeps nothing and does not fail", () => {
  assert.equal(keepWalkthrough({}, record()), undefined);
});
