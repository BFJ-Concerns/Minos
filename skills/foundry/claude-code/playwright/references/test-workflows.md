# Test Workflows

Writing, generating, running, and fixing Playwright test files. The common
mechanic throughout: run a test paused in debug mode in the background, attach
`playwright-cli` to it, and drive the paused page interactively — every
interaction prints the Playwright TypeScript that belongs in the test file.

## Running Tests

```bash
PLAYWRIGHT_HTML_OPEN=never npx --no-install playwright test    # whole suite
PLAYWRIGHT_HTML_OPEN=never npx --no-install playwright test tests/foo.spec.ts:12
```

`PLAYWRIGHT_HTML_OPEN=never` stops the HTML report from opening a browser and
blocking the run. Prefer the repo's own test script when one exists.
`--no-install` keeps `npx` from resolving a fresh package on the fly: if it
reports Playwright is missing, the project has no Playwright, and adding it is
an explicit, version-pinned dependency decision (see Setup and Repair in the
skill body), not a side effect of running tests.

## The Debug–Attach Loop

```bash
# 1. From the repo, start the failing test in debug mode as a background job,
#    capturing its output (use the shell tool's own background facility if it
#    has one; otherwise redirect and detach):
PLAYWRIGHT_HTML_OPEN=never npx --no-install playwright test tests/foo.spec.ts:12 --debug=cli \
  > <scratch>/debug-run.log 2>&1 &
# 2. Poll the log until "Debugging Instructions" appears, and read the session name:
grep -o 'tw-[A-Za-z0-9-]*' <scratch>/debug-run.log
# 3. Attach and diagnose — run these from <scratch>, like every playwright-cli
#    command (they write .playwright-cli/ into their cwd):
playwright-cli attach tw-XXXXXX
playwright-cli pause-at <file:line>   # or step-over / resume
playwright-cli snapshot               # did the element move, rename, change role?
playwright-cli console                # app-side errors?
playwright-cli requests               # failed call? wrong payload?
```

Two working directories are in play: the `npx playwright test` run belongs to
the repo, while the `playwright-cli` commands belong to `<scratch>` — the
daemon doesn't care where you stand, but its auto-snapshots land in the cwd.

Go through the test, not around it: attaching to the paused test inherits its
fixtures, auth, and setup, where opening the URL directly would not. Rehearse
the corrected interaction in the attached session — the generated code in the
output is what you paste back into the test file. When done: stop the
background test run, close the CLI session, rerun the test to confirm green.

## Diagnosing Failures

Fix one test at a time; rerun after each fix before moving on. Batched fixes
hide which change worked.

| Symptom | Likely cause | Fix |
|---|---|---|
| Element not found | Selector drift, new wrapper, label/ARIA rename | Update the locator (snapshot shows the current tree) |
| Timeout waiting | Element never appears, or feature moved/removed | Check the feature still exists; wait on the right condition |
| Wrong text/value | App behaviour changed | Update the assertion — if the change looks unintended, see below |
| Passes sometimes | Race condition | Wait for a condition; remove timing assumptions |
| Click does nothing | Element obscured, disabled, or not ready | Wait for visibility/enabled state; check overlays |
| Network error in test | API changed or environment missing | Update the mock or the test environment |

Fix discipline — these hold regardless of pressure to get the suite green:

- Wait for specific conditions (`waitFor`, `waitForResponse`, `expect`
  polling); never `waitForTimeout`, never `networkidle`.
- Prefer role/label locators over CSS classes; test behaviour, not
  implementation details.
- Never delete a failing test without understanding why it fails, never
  loosen an assertion just to pass, never skip silently.

## When the Test Is Right and the App Is Wrong

If investigation shows the test is correct and the application is broken, the
test has found a real defect — do not quietly silence it. Report the bug to
the user first, with the evidence gathered. Only with explicit approval to
unblock the suite, mark the test `test.fixme()` with a comment stating what
the test expects, what actually happens, and why it appears to be an
application bug — that surfaces the defect while preserving the test's intent.

## Planning Test Coverage (spec-driven)

For substantial coverage work, write the plan down before generating anything:
a spec file (`specs/<feature>.plan.md`) enumerating scenarios, each with
user-level steps and `- expect:` bullets for observable outcomes. Place the
spec by its fate: one that generates committed tests is a test fixture and
lives in the repository beside them (the generated tests point back at it with
a `// spec:` header); exploratory planning that never wires into committed
tests belongs in the scratch workspace from the skill body's Artefact Hygiene
section, not the code repo. Explore the
app first through a **seed test** — a minimal test (ideally a fixture) that
lands the page in the state every scenario starts from — using the debug–attach
loop above, and map: primary journeys, interactive surfaces, edge cases (empty
states, validation, boundary values), persistence across reload, and
navigation behaviour. Scenarios must be independent, each starting fresh from
the seed; cover happy paths, edge cases, validation, and negative flows.

## Generating Tests from a Live Session

For each planned scenario, one at a time:

1. Start the seed test with `--debug=cli` in the background; attach; `resume`
   so the seed completes.
2. Walk the scenario's steps with `playwright-cli`, snapshotting to find refs.
   The spec is the plan; the live app is the source of truth — when a step is
   vague or stale, update the spec to match reality and continue.
3. Collect the printed TypeScript into the test file. Default layout, when the
   project shows no convention of its own: one test per file, named from the
   spec — a kebab-case, filesystem-friendly scenario name as the filename
   (`should-add-single-todo.spec.ts`), the `describe` block matching the
   spec's group name, the test name matching the scenario. A project that
   already groups related scenarios into shared spec files keeps its own
   convention. Either way: a `// N. <step>` comment before each step's
   actions, and imports from the project's fixtures file when it has one.
4. Assertions are yours to add — generation captures actions, not
   expectations. For each `- expect:` bullet write an explicit assertion;
   `playwright-cli --raw generate-locator e5` gives a stable locator and
   `--raw eval "el => el.textContent" e5` the expected value. Prefer
   `toBeVisible`/`toHaveText`/`toMatchAriaSnapshot`; when the locator itself
   is text-based, assert visibility rather than repeating the text.
5. Stop the background run and close the session before the next scenario, so
   every test starts clean. Then run the new test once, unattended.

## Healing a Suite

Run the suite, list the failures, and take them one at a time through the
debug–attach loop and the diagnosis table. After each fix, reconcile the spec
if one exists: a purely technical fix (locator drift) leaves the spec alone; a
fix that changed user-visible steps or outcomes updates it. When you cannot
tell whether the app change was intentional (stale spec) or a regression
(real bug), stop and ask the user, quoting the scenario, the stale spec lines,
and the observed behaviour — do not pick a side silently.
