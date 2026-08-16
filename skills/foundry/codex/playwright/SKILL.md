---
name: playwright
description: Browser automation and testing with Playwright — explore and debug web apps and Electron apps in a persistent browser session driven from the shell with playwright-cli, run functional and visual QA, write and heal E2E tests, and repair missing browsers or tooling. Use when testing web pages, reproducing UI bugs, verifying frontend changes, capturing screenshots, automating browser interactions, debugging an Electron app's UI, or working on a Playwright test suite. Keeps transient automation artefacts out of the repository. Drives isolated automation browsers only — not for the user's own browser or its open tabs, which browser-extension tooling owns.
---

# Playwright

Browser work runs through three mechanisms. Pick by the job, not by habit:

1. **`playwright-cli`** — the default for interactive work: exploring a page,
   reproducing a bug, QA passes, debugging a failing test. A user-level daemon
   holds named browser sessions open across shell calls, so state persists
   between commands without any harness configuration; that is what makes it
   the portable choice on any machine with Node.
2. **Playwright MCP browser tools** (`browser_navigate`, `browser_snapshot`,
   `browser_click`, …) — fine for short interactive exploration when the
   session already lists them. Each call returns a page snapshot into context
   whether needed or not, so long flows cost far more than the CLI; and the
   server's availability is per-machine configuration you cannot repair from
   inside the session. When the tools are absent, use `playwright-cli` —
   absence is normal, not an error.
3. **Playwright scripts and `npx playwright test`** — for anything repeatable:
   test suites, CI checks, Electron apps, batch scraping. If a check will run
   again, it belongs in a script or a committed test, not a transcript.

## Setup and Repair

Broken environments get fixed, not worked around. Triage in this order:

```bash
playwright-cli --version || npx --no-install playwright-cli --version
```

- **Command missing** — install it pinned: `npm install -g
  @playwright/cli@<version>`, choosing the newest release at least a week old
  (`npm view @playwright/cli time` lists release dates; this is the standing
  dependency policy, and an unpinned install defeats it). Without permission
  for a global install, `npx @playwright/cli@<version>` runs the same pinned
  version from the package cache.
- **Browser missing** — `open` fails with an executable-not-found error when
  the machine has neither a system Chrome nor a Playwright browser cache. Run
  `playwright-cli install-browser` to fetch Chrome for Testing into the user
  cache (no elevated permissions needed). If launch then fails on missing
  system libraries and you can use sudo, add them with
  `playwright-cli install-browser --with-deps`; without sudo, report exactly
  which libraries are missing instead of downgrading to a weaker mechanism.
- **Running via `npx`** — when the CLI is reachable only through `npx` rather
  than a global install, substitute `npx --no-install playwright-cli` for
  `playwright-cli` in every command in this skill; the daemon and sessions
  behave identically.
- **Dev server not reachable** — `net::ERR_CONNECTION_REFUSED` on `goto`
  means the app under test is not listening, not that the browser is broken.
  Start the dev server as a persistent background process, confirm the port
  answers, and prefer `127.0.0.1` over `localhost` (IPv6/IPv4 resolution of
  `localhost` differs between machines).

## Driving the Browser

```bash
playwright-cli open http://127.0.0.1:3000   # or a named session: -s=qa open …
playwright-cli snapshot                     # accessibility tree with element refs
playwright-cli click e15                    # act on a ref from the snapshot
playwright-cli fill e5 "user@example.com" --submit
playwright-cli console                      # page console messages
playwright-cli requests                     # network activity since load
playwright-cli screenshot                   # only when visuals matter; snapshot is cheaper
playwright-cli close                        # this session; before finishing, close-all (see Artefact Hygiene)
```

The working loop is snapshot → act → snapshot: the accessibility snapshot
shows structure, roles, labels, and state, which is what interaction decisions
need; screenshots are for judging rendered appearance, not for finding
elements. Targets accept snapshot refs (`e15`), CSS selectors, or Playwright
locators (`getByRole('button', { name: 'Submit' })`). Wait for conditions, not
durations — the page reaching a state is evidence, elapsed time is not.

Two commands lift the ceiling when the verb set runs out:

- `playwright-cli eval "el => el.getAttribute('data-testid')" e5` — run
  JavaScript against the page or an element.
- `playwright-cli run-code "async page => { … }"` — run arbitrary Playwright
  API code against the live session: permissions, geolocation, media
  emulation, iframes, downloads, conditional network mocking.

Every interaction command also prints the equivalent Playwright TypeScript,
which is what turns an interactive session into a permanent test — see
`references/test-workflows.md`.

The full command manual ships with the tool itself: `playwright-cli --help`
prints the path of its bundled agent skill, whose `references/` cover request
mocking, storage state, tracing, video recording, and attaching to running
browsers. Read those on demand rather than guessing flags.

## Artefact Hygiene

Browser automation sheds files — screenshots, page snapshots, traces, videos,
storage-state dumps. Transient captures never belong in the repository, where
they accumulate as untracked litter or, worse, get committed by accident; the
one exception is deliberate test fixtures, covered below.

<!-- foundry:variant scratch-workspace start -->

On this target, `<scratch>` is a session directory you create under `/tmp/codex/` —
`mkdir -p /tmp/codex` then `mktemp -d /tmp/codex/pw-XXXXXX`, and use the result. The parent
is shared by every session on the machine, so working directly in it would mix concurrent
sessions' captures and make cleanup unsafe; a private subdirectory keeps both safe.

<!-- foundry:variant scratch-workspace end -->

- **Run `playwright-cli` from `<scratch>`, not from the repository.** The CLI
  writes an auto-snapshot into `.playwright-cli/` under the *current working
  directory* on every page-state-changing command — the path is hardcoded, so
  the only control is where you stand. The daemon is cwd-independent: a
  session opened from `<scratch>` is fully usable while you edit code in the
  repo from other shells.
- Point deliberate captures at explicit paths under `<scratch>`:
  `playwright-cli screenshot --filename=<scratch>/…png`.
- **Keeping evidence for later comparison** — a before/after baseline, a
  screenshot a future session will diff against — is the exception that needs
  a durable home: put it in the project's annexe and commit it there, so it
  survives on purpose rather than by neglect. If the project has no annexe,
  ask the user where (or whether) to keep it. Visual-regression baselines that
  a committed Playwright test suite reads (`toHaveScreenshot` snapshots) are
  test fixtures, not debris — they live in the repository with their tests.
- **Before finishing: leave nothing behind.** Close browser sessions
  (`playwright-cli close-all`; `kill-all` if a zombie daemon survives) — the
  daemon and its browsers outlive the conversation, and an orphaned headed
  browser on someone's desktop is this skill's least charming failure mode.
  Delete `<scratch>` captures that served their purpose, and check the repo's
  `git status` for strays if any command ran from it.

## Temporary or Permanent?

Decide before starting, because the artefact differs:

- **Temporary exploration** — verifying something works, chasing a UI bug,
  answering "what happens if". Drive the session, get the answer, clean up;
  no files survive.
- **Permanent tests** — behaviour that should be verified repeatedly:
  a feature worth protecting from regression, a bug that must not return, a
  flow teammates or CI need to re-run. Write or extend real test files —
  `references/test-workflows.md` covers planning scenarios, generating tests
  from a live session, and healing failures.

Repeating the same temporary check is the signal to promote it to a permanent
test: the third manual verification costs more than writing the test would.

## QA Signoff

Claiming UI work is done is a testing claim, and it needs more than "the page
loads". When signing off implemented UI — yours or under review — run the
doctrine in `references/qa-signoff.md`: build a QA inventory from the
requirements, the implemented behaviour, and the claims you intend to make;
exercise the functional path with real user input; run a *separate* visual
pass over screenshots (functional passing does not prove visual claims); and
check viewport fit numerically as well as visually. Scale the pass to the
claim surface: a new feature or a "the UI works" signoff gets the full
doctrine; a one-line copy or style fix gets the checks that cover what that
fix claims, not the whole inventory. The invariant is the short form: every
claim in your final report maps to a check you actually ran, in the state
where the claim matters.

## Electron

`playwright-cli` drives web pages; it does not launch Electron apps. For
Electron, write a short script using Playwright's
`_electron.launch({ args: ['.'] })`, drive `firstWindow()` with the same page
API, and always `electronApp.close()` at the end — an Electron process
outlives the session silently if the script does not close it. Run the script
with `node` **from the app's root directory**: that is what makes `'.'`
resolve to the app and lets the script import the project's own `playwright`
and `electron` dev-dependencies. The script file and every capture it writes
still point at `<scratch>` by absolute path — only the working directory
belongs to the app. After a renderer-only change, `window.reload()` suffices;
after a main-process, preload, or startup change, relaunch the app — and
relaunch rather than guess when process ownership is unclear.

## Troubleshooting

| Symptom | Likely cause and fix |
|---|---|
| `playwright-cli: command not found` | Not installed — see Setup and Repair; try `npx --no-install playwright-cli` first |
| `open` fails: executable/browser not found | No system Chrome and no cache — `install-browser`, `--with-deps` if sudo allows |
| `net::ERR_CONNECTION_REFUSED` | Dev server not listening — start it, verify the port, use `127.0.0.1` |
| Commands hang or a session misbehaves | Stale daemon — `playwright-cli kill-all`, then reopen |
| Ref (`e15`) no longer resolves | Page changed since the snapshot — re-run `snapshot`, use fresh refs |
| MCP `browser_*` tools absent | Normal — use `playwright-cli`; do not treat as a blocker |
| `.playwright-cli/` appeared in the repo | A command ran from the repo — delete the directory, rerun from `<scratch>` |
| Daemon dies instantly: `EROFS`/read-only cache writes, or launch/network fails immediately | A sandboxed execution path is blocking it — see the sandbox note below |

<!-- foundry:variant sandbox-note start -->

**Sandbox note.** Codex's default sandbox blocks the daemon — the cache
directory is read-only (`EROFS` writing under `~/.cache/ms-playwright/daemon/`)
and network is restricted. When a playwright-cli command fails with that
signature, request escalated approval for it, or have the session run with
`--sandbox danger-full-access`. Escalate for this failure signature only; it is
not a standing way to run.

<!-- foundry:variant sandbox-note end -->

## References

- `references/test-workflows.md` — read when writing, generating, running, or
  fixing Playwright test files: the plan → generate → heal workflow, the
  `--debug=cli` attach loop, failure diagnosis, and the fix-discipline gates.
- `references/qa-signoff.md` — read before signing off implemented UI: the QA
  inventory, functional and visual pass requirements, viewport-fit checks, and
  the signoff gates.
