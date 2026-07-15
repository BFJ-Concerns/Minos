---
name: review-panel
description: The standard code review — use this whenever the user asks for a code review, a security review of the code, a review of their code, changes, branch, or the current branch's PR, or a check before merging; those requests are this skill's job, not something to answer inline. Also use to review or audit the code against the project's `.review/` briefs, baseline a new brief, or run an occasion-specific (release, nightly) review. Reviews the current branch or its PR only — an arbitrary PR by number is /review's job. Not for reading, summarising, or addressing existing PR review comments (babysit-pr), not for authoring or refining the briefs (review-brief), and not for non-code security work such as threat modelling, architecture, or deployment-posture reviews. Review only — it never applies the fixes it suggests.
argument-hint: '[--full] [--base <ref>] [--occasion <name>] [--no-aspects] [--no-briefs] [--no-codex] [--no-verify] [--bar] [name …]'
allowed-tools:
- Bash(git rev-parse:*)
- Bash(git diff:*)
- Bash(gh pr view:*)
- Bash(python3 *plan_review.py*)
- Bash(python3 *run_codex_review.py*)
- Bash(python3 *merge_codex_review.py*)
- Bash(python3 *validate_quotes.py*)
- Bash(python3 *assemble_verify_input.py*)
- Bash(python3 *plan_remediation.py*)
- Bash(python3 *merge_remediation.py*)
- Bash(python3 *post_pr_comments.py*)
- Bash(node:*)
- Read
- Grep
- Glob
- Write(/tmp/claude/**)
- Edit(**/ISSUES.md)
---

# Review Panel

Convene a panel of review agents over a change: the bundled **standard
aspects** — the review dimensions any codebase needs — together with the
repository's own **`.review/` briefs** where it keeps them, and, on a diff
run, the **Codex leg** — the Codex CLI's built-in reviewer, bringing a second
model family's reading of the change. Each concern gets
its own clean-context reviewer, blind to the others, scoped to the relevant
files, split across several reviewers when its file set is large; the panel's
findings are collated and every one passes through the verification gate
before anything posts. One run, one posted review.

This is the standard code review. It finds real defects (the correctness and
security aspects), holds the change to the quality dimensions that rot
codebases when unreviewed (error handling, comments, tests, type design,
simplification), and checks the repo's own standing concerns — in a single
panel with a single report.

## The standard aspects

Seven aspect files ship with the skill (in `aspects/`, beside `scripts/`),
one per review dimension:

| Aspect | Concern |
|--------|---------|
| `correctness` | Actual bugs and violations of the project's written conventions, at a high confidence bar |
| `error-handling` | Failures that would be concealed: swallowed errors, over-broad catches, unjustified fallbacks |
| `comments` | Comments the change makes wrong, stale, or misleading; comments that add no value |
| `tests` | Behavioural coverage — the concrete regressions the current suite would let pass |
| `types` | Invariant strength and encapsulation of new or reshaped types |
| `simplification` | Behaviour-preserving clarity improvements (suggestions, never blockers) |
| `security` | Defensive robustness: untrusted input reaching sensitive operations, auth gaps, secrets, weak cryptography |

Aspects review the diff, repo-wide, and are planned exactly like briefs —
same sharding, same verification, same report. What is different is **relevance
gating**: each aspect's frontmatter declares the diffs it is relevant to, and
the plan step weighs that declaration against the actual changed set. The
default is to run; an aspect is skipped only when the diff *clearly* gives it
nothing to do (a docs-only change gives the types aspect nothing; a diff with
no comment or adjacent-code changes gives the comments aspect nothing). When
in doubt, run it — a wasted reviewer costs a little money, a skipped concern
costs a missed finding. `correctness` is never skipped. Every granted skip is
recorded with its reason and reported with the run's other skips: an unchecked
concern, not a passed one.

## The `.review/` convention

A `.review/` directory at the repo root holds one markdown **brief** per file —
the repository's own standing concerns, authored by `/review-brief`. The
directory is optional: a repo without one still gets the full aspect panel. A
brief's position determines what it reviews:

- A brief **directly in `.review/`** applies repo-wide.
- A brief **inside a subfolder whose full nested path matches a repo
  directory** is scoped to that subtree. `.review/src/api/limits.md` reviews
  only `src/api/` when that directory exists; the match is on the full path
  (`src/api`), not just the leaf.
- A brief inside a subfolder that **matches no repo directory** still runs, but
  repo-wide, and `plan_review.py` emits a warning so authors can fix a typo or
  stale folder name. Surface that warning to the user.

A brief is plain prose: what to check and what good looks like. The agent reads
the full file, so write it for a reviewer, not a parser. A brief may carry
optional frontmatter declaring its **extent**:

```yaml
---
extent: full   # or omit for the default, diff
---
```

Scope (which subtree), extent (how much of it), sweep (how it is covered), and
occasion (when it applies) are independent axes:

- **`extent: diff`** (default, or no frontmatter) — review only what the branch
  changed within the brief's scope. Most briefs are this: "is this changed code
  good?"
- **`extent: full`** — audit the brief's whole scope on every run, regardless of
  the diff. Two kinds of concern need this: per-file judgement that must cover
  the whole tree rather than just the diff (test quality, per-file conventions),
  and whole-tree invariants (global properties, cross-file consistency, "every X
  is registered in Y"). Heavier, so reserve it for concerns that genuinely need
  it.

A brief also declares, in the same frontmatter, **how it sweeps** — whether its
files can be reviewed independently:

- **`sweep: per-file`** (default, or no frontmatter) — each file is judged on
  its own. When the file set is large, the brief is split across several
  reviewers, each handed a slice, so no single agent is swamped. Most concerns
  are this: the verdict on one file doesn't depend on reading another.
- **`sweep: whole-tree`** — the concern needs the whole scope in one view
  (a cross-file invariant, a uniqueness or completeness property), so the brief
  is **never** split; it is always one reviewer. A whole-tree file set beyond
  what one reviewer can genuinely hold is refused loudly at planning (reported
  as not run, with the measured size) rather than skimmed — so keep whole-tree
  briefs scoped to a subtree.

A brief may declare the **occasions** it applies on — run-conditions, as
author-defined tokens (`occasion: merge` or `occasion: nightly, release`). A
run names its occasion with `--occasion <name>`; a brief that declares
occasions runs only when the run's occasion matches, and one that declares none
applies on every occasion. A brief skipped by occasion is reported with the
other skips — an unchecked concern, not a passed one.

`/review-brief` decides and sets a brief's extent, sweep, and occasion when
authoring; this skill reads them. (The same frontmatter keys work in aspect
files, plus `relevance:` — but aspects ship with the skill and are not edited
per repo.)

## The Codex leg

On a diff run the panel also convenes the Codex CLI's built-in reviewer —
`codex review`, run non-interactively against the same base — as one more
panel member, so the review carries a second model family's reading of the
change alongside the aspect and brief reviewers. `run_codex_review.py` runs
the CLI and maps its findings into the panel's finding shape (the priorities
already share the P0–P3 scale; the `code_quote` is filled mechanically with
the verbatim lines at each cited location, so the validator can confine the
finding to real, cited code), and `merge_codex_review.py` folds them into the
run before the validation gate. From there they are panel findings like any
other: quote-validated, independently checked, posted under the **Codex
Review** title, and de-duplicated against the other concerns' findings. The
leg's findings arrive classified change-introduced — the CLI reviews the
branch diff, so that is what its findings describe — and the checkers'
attribution dimension holds them to it: one that is really pre-existing comes
back attribution-indeterminate and still posts, the conservative direction.
Because their producer is Codex, the checker gate sends them to Claude
checkers — the family opposite the producer, mirroring how the panel's
Claude-produced findings go to Codex checkers.

The leg's coverage joins the account honestly as its own category: the CLI
returns only a verdict and its findings — never the files-read/commands-run
trail an agent reviewer's clean-review note carries — so `merge_codex_review.py`
records the leg as an `external-verdict` coverage entry (`method:
"external-cli"`, carrying the verdict and the mapped-findings count), never
reviewer-method `full`. The bar judges that named entry on verdict-honesty —
that it represents itself as an external verdict and nothing more, and that its
leg-attributed findings reconcile with the recorded mapped count — rather than
on an evidence trail the CLI structurally cannot emit. Extent and source
faithfulness are the run and merge scripts' mechanical guarantees, covered by
their tests, not the judge's to re-establish; every agent reviewer stays held
to the full coverage discipline.

The leg runs on every diff-mode run by default, and is selectable and
excludable like any concern: name it as `codex-review` to run it alone, or
pass `--no-codex` to leave it out. The planner skips it — recorded and
reported like every other skip — under `--full` (`codex review` reviews a
branch diff; it has no whole-scope audit mode), on an empty diff, when name
selection names other concerns without it, or when the machine has no `codex`
CLI. A leg that was planned but fails — the CLI errors or times out — is a
coverage gap, reported with the run's reviewer failures, never a silent
absence.

## Runs

| Invocation | What runs | Output |
|------------|-----------|--------|
| `/review-panel` | Every relevant aspect plus every applicable brief, each at its declared extent, vs the branch | Findings posted to the PR; pre-existing findings routed to the annexe; chat recap. Chat-only if no PR |
| `/review-panel <name>…` | Only the named aspect(s)/brief(s), at their declared extent | as above |
| `/review-panel --full [<name>…]` | The named concerns (or all), forced to full extent — a whole-scope audit | Chat summary grouped by review |
| `/review-panel --base <ref> [<name>…]` | Diff against `<ref>` instead of the auto-detected base | as the default run |
| `/review-panel --occasion <name> …` | As the run would otherwise, with occasion-declaring briefs selected by `<name>` | as the run would otherwise |
| `/review-panel --no-aspects …` | Briefs only — the repo's own concerns without the standard panel | as the run would otherwise |
| `/review-panel --no-briefs …` | Aspects only — the standard panel without the repo's briefs | as the run would otherwise |
| `/review-panel --no-codex …` | As the run would otherwise, without the Codex leg | as the run would otherwise |
| `/review-panel --no-verify …` | As the run would otherwise, skipping per-finding verification | as the run would otherwise |
| `/review-panel --bar …` | As the run would otherwise, plus the bar check on the assembled review | as the run would otherwise, plus the bar verdict |

A default run honours each concern's extent: diff-extent concerns see only
changed files; full-extent briefs audit their whole scope and classify each
finding so change-caused ones still reach the PR (see Report). **`--full`**
overrides every selected concern to a whole-scope audit and reports to chat —
use it to baseline a new brief against existing code
(`/review-panel --full <new-brief>`) without dragging every other concern
through a sweep, or to audit the whole repo against an aspect
(`/review-panel --full security`). **Name selection** — naming aspects or
briefs — restricts any run to those concerns.

Every finding is quote-validated in code and then verified by an independent
checker by default (see steps 3–4); **`--no-verify`** skips the checkers (the
quote validator always runs — it is near-free and catches the fabrications).
**`--bar`** opts in to the bar check interactively. A headless caller wanting
the bar fired by its computed trigger instead — the review would otherwise
converge clean, or a criterion was skipped, failed, or partially covered —
passes `bar_mode: "auto"` at step 4 rather than `--bar`. A bar that runs and
**fails** triggers one remediation round (step 5): the implicated reviews are
re-run fresh and re-verified rather than the run's work being discarded.

**Base resolution** (diff mode only) decides what the branch is compared against,
most explicit first: a PR's base when there's a PR, otherwise an auto-detected
default branch (`origin/HEAD`, then `main`/`master`). When neither is right — a
branch that forks off another branch, or a repo with an unusual default — pass
**`--base <ref>`** to name what to diff against. This is the only supported
non-PR shape: the panel reviews a PR or a branch against its base, nothing
else. Uncommitted working-tree changes are not part of that comparison — when
the user wants them reviewed, say so and suggest committing first rather than
reviewing a surface that silently excludes their edits.

**EMPTY DIFF is a distinct outcome, never a clean pass.** A diff run whose
changed set is empty returns `outcome: "empty-diff"`: every diff-extent concern
is skipped (nothing changed to review — the aspects all fall here), while
full-extent briefs still plan and dispatch — they audit their whole scope
regardless of the diff. Report the branch-review portion as exactly that — the
diff was empty, nothing on the branch was reviewed — never as a clean pass or
"no findings": with a *guessed* base (`base_source: "default"`) it as often
means the wrong base as a clean branch, so say the base was guessed and suggest
`--base <ref>`. A failed diff command is a planner `error`, not an empty diff.

A full-scope audit covers every in-scope tracked file. A **per-file** concern is
split across several reviewers when its scope is large, so one reviewer is never
handed a sweep it can only skim; on an enormous scope the planner caps the
fan-out and grows each slice instead, logging the split so the trade is visible. A **whole-tree** brief can't be split — it is always one
reviewer — and beyond the whole-view capacity (a file count `plan_review.py`
measures against its budget) the plan refuses to dispatch it at all, reporting it
as not run with the measured size, rather than let one reviewer skim a scope it
cannot hold. Keep whole-tree briefs scoped to a subtree.

## Workflow

The run's agent stages are bundled ensemble workflows — deterministic scripts
executed by the ensemble runtime that ships with the `ensemble-workflow` skill:
one fans reviewers out over the plan, one verifies the findings and judges the
bar. The dispatch (one agent per shard, every concern), the slice of files each
reviewer covers, the findings shape, the checker pairing, and the downgrade
rules are all fixed by code rather than left to the model; the agents' judgement
stays non-deterministic — that is the point — but what they are pointed at, and
the structure they return, are not. Invoking the bundled workflows from this
skill is the user's opt-in to multi-agent orchestration — do not ask again
before running them. The agents dispatch as clean-context background sessions,
so the runtime's live requirements apply; if the runtime or its primary engine
is unavailable, stop and report that rather than hand-rolling a substitute
fan-out. (The verification stage prefers the family opposite each finding's
producer and degrades to the other, recording the achieved pairing — that
degradation is the workflow's to make, not yours.)

<!-- foundry:engine-placeholders ensemble-runtime start -->

## Running a workflow

The runtime lives in the `ensemble-workflow` skill — a self-contained Node ≥24 launcher,
nothing to install. Run a workflow with flags **before** the script path; the result is
one line of JSON on stdout, logs on stderr:

```bash
node ~/.claude/skills/ensemble-workflow/scripts/ensemble.mjs --json-args '<json>' ~/.claude/skills/<skill>/workflows/<name>.js > <scratch>/<name>.json 2> <scratch>/<name>.log
```

`<skill>` is the skill this reference ships with — substitute its bundled workflow
script's installed path (a skill that documents a concrete command already spells it
out). Both paths are absolute so the command runs from any
cwd — launch from the directory the move itself calls for (a blind stage from an empty
scratch directory, a grounded one from the code repo), never from a directory chosen just
to make a relative path resolve.

The script **cannot touch the filesystem** (sandboxed): pass every input in `--json-args`,
and **you are the driver** — write the returned stdout to `<scratch>`, read it, and bring
the result back. Read each workflow script's header for the inputs it names.
Everything else — the flags (`--budget`, `--concurrency`, `--timeout`), their defaults and
when to set them, the live-engine requirement, and the fan-in limits — is owned by the
`ensemble-workflow` skill; read it before running or authoring a workflow.

<!-- foundry:engine-placeholders ensemble-runtime end -->

The plan decides the fan-out; run the bundled workflows as written. A concern
with a large file set is **deliberately split across several reviewers by the
plan** — that is the workflow doing what it is for, iterating a large group
across agents, and it is fixed in code (`plan_review.py` sizes each concern and
shards it). The groundedness guards are likewise fixed: each reviewer must quote
the code it flags (step 2), the quote validator checks those quotes in code
(step 3), and the verification workflow pairs each finding with an independent
checker (step 4). What is **not** allowed is improvising on top of that at
runtime: do not rewrite the scripts, re-derive or re-balance the shards by hand,
or stand up your own extra confirmation pass alongside the scripted ones. The
plan handles its own refusals — a whole-tree brief beyond the whole-view
capacity is marked not-run with the measured size while the rest of the run
proceeds; relay that refusal (the fix is usually to scope the brief to a
subtree). Stop the whole run only on a plan-level `error`, and report what
blocked it rather than engineering around it silently.

1. **Plan.** Run the discovery script from the target repo, translating the
   slash arguments: `--full` → `--full`, `--base <ref>` → `--base <ref>`,
   `--occasion <name>` → `--occasion <name>`,
   `--no-aspects`/`--no-briefs`/`--no-codex` → the same, and any bare words →
   aspect/brief names (or `codex-review`) to restrict to.
   (`--no-verify` and `--bar` are step-4 inputs, not planner flags.)

   ```bash
   python3 ~/.claude/skills/review-panel/scripts/plan_review.py [--full] [--base <ref>] [--occasion <name>] [--no-aspects] [--no-briefs] [--no-codex] [name …] > /tmp/claude/review-panel-plan.json
   ```

   Then read the file to inspect the plan. When the user's request names an
   occasion in words ("run the release review") rather than as a flag, translate
   it to `--occasion <token>` using the tokens the repo's briefs declare.

   **Relevance gating** (diff mode only — a full audit has no diff to be
   irrelevant to, and the planner refuses `--skip-aspect` there). The first
   plan runs every aspect. Now weigh each aspect's `relevance` line (each plan
   entry carries it) against what actually changed:
   `git diff --name-only <base_ref>...HEAD` lists the changed set, and
   `git diff` shows the content where names alone don't settle it. Skip an
   aspect only when the diff **clearly** gives it nothing to do; when in doubt,
   it runs. Two skips the planner refuses in code: `correctness` always runs,
   and an aspect the user selected by name always runs — explicit selection is
   authoritative. If any skips are warranted, re-run the planner once with the
   same arguments plus a `--skip-aspect name="reason"` per skip, and use that
   second plan. The reasons appear in the plan's skip records and must reach
   the run report — a skipped aspect is an unchecked concern, and the user
   sees which and why.

   **If the plan's `outcome` is `"empty-diff"`**, every diff-extent concern has
   been skipped, the Codex leg among them. If no concern remains active, stop here and report the EMPTY
   DIFF outcome as described under Runs — no phrasing of that report may read as
   a clean pass. If full-extent briefs remain active, continue: they audit
   regardless of the diff, and the branch-review portion is still reported as
   EMPTY DIFF alongside their results.

   Each concern in the plan carries its `kind` (`aspect` or `brief`), its
   effective `extent` (`diff` or `full`), `sweep` (`per-file` or `whole-tree`),
   and `occasions` alongside its `scope`, the size of its file set as
   `file_set_size`, and a `shards` list — one entry per reviewer. A small
   concern, or any whole-tree brief, has a single shard; a large per-file
   concern has several, each describing a slice of the scope. An unsharded diff
   concern's shard carries the explicit changed `files`; every other shard
   carries a 1-based line range the reviewer turns into its slice by enumerating
   the scope itself, so no large file list is ever relayed through the workflow
   input (see the fan-out note in step 2). A skipped concern carries
   `skip_reason` and `skip_kind` (`relevance`, `empty`, `occasion`, or
   `capacity`) instead of shards.

   It returns JSON: the concerns (under `briefs`), the Codex leg's entry
   (under `codex_review` — planned, or skipped with its reason), the detected
   `pr`, the `base_ref`, the `base_source`, `warnings`, and the three static
   method-file paths (`template_path` for reviewers, `checker_method_path` for
   the per-finding checkers, `bar_method_path` for the bar judge). If it
   returns an `error` (no git repo, a broken skill install, nothing to plan, a
   missing method file, or an unresolvable `--base`), report it and stop.
   Surface any `warnings` to the user — they usually mean a brief is
   mis-scoped or a name collides.

   Tell the user what was diffed: the `base_ref` and how it was chosen
   (`base_source` is `pr`, `explicit`, or `default`). When it's `default` the
   base was *guessed* — say so, and if the run found little, treat that as
   possibly the wrong base and suggest `--base <ref>`. When a concern's `shards`
   list has more than one entry, mention the split (the workflow logs it too) so
   the user sees a large sweep was spread across reviewers. A brief skipped at
   `capacity` was refused, not passed — relay its reason, which names the
   measured size and the fixes (scope to a subtree, or declare
   `sweep: per-file`).

2. **Fan out via the ensemble runtime — with the Codex leg running beside
   it.** First start the Codex leg as a background task — run this command
   through the shell tool's background mode (not `&`), so it works while the
   panel reviews and you are told when it exits. It takes minutes on the CLI's
   own model; when the plan skipped the leg it exits immediately, emitting the
   skip for the merge to record. Running it in the foreground instead is
   merely slower, never wrong:

   ```bash
   python3 ~/.claude/skills/review-panel/scripts/run_codex_review.py /tmp/claude/review-panel-plan.json > /tmp/claude/codex-leg.json
   ```

   Then run the bundled workflow through the runtime, wrapping the plan file
   from step 1 as the script's JSON input:

   ```bash
   node ~/.claude/skills/ensemble-workflow/scripts/ensemble.mjs \
     --json-args "{\"plan\": $(cat /tmp/claude/review-panel-plan.json)}" \
     ~/.claude/skills/review-panel/scripts/review_workflow.js \
     > /tmp/claude/review-panel-result.json 2> /tmp/claude/review-panel-run.log
   ```

   When **both** have exited — wait for the leg's background task to finish,
   never merge a half-written file — fold the leg into the workflow result.
   Run the merge unconditionally, so a skipped or failed leg still lands in
   the run's coverage account rather than vanishing (the leg script reports
   its own failures inside `codex-leg.json`; if the merge command itself
   errors on an unreadable leg file, treat the leg as failed, report it with
   the run's failures, and continue with the unmerged result):

   ```bash
   python3 ~/.claude/skills/review-panel/scripts/merge_codex_review.py /tmp/claude/review-panel-result.json /tmp/claude/codex-leg.json > /tmp/claude/review-panel-combined.json
   ```

   The runtime prints the workflow's return value as one line of JSON on stdout —
   read `/tmp/claude/review-panel-result.json` for the result; the stderr log
   carries the split notices. Leave the runtime's tuning flags (budget,
   concurrency, timeout) unset.

   Do **not** relay the reviewer prompt through the input. It is static
   (`prompts/reviewer-method.md`), the plan carries its path (`template_path`),
   and each reviewer reads it itself — relaying that multi-kilobyte,
   backtick-heavy prose through the workflow input causes JSON parse errors: a
   single garbled character kills the run before any reviewer starts. Keep the
   input to just the compact plan.

   The workflow launches one schema-validated agent per shard, in parallel: one
   agent for a small or whole-tree concern, several for a large per-file
   concern, each covering a slice of the scope. Each agent reads the shared
   reviewer method and its own aspect or brief file, and reviews only its
   assigned files — reviewers stay blind to one another. A **diff-extent**
   concern inspects the branch diff for its changed files and flags what the
   change introduces or touches; a **full-extent** concern enumerates its scope
   (with `git ls-files`) and audits it, classifying each finding as
   change-caused (`preexisting: false`) or independent (`preexisting: true`). A
   `--full` run forces every concern to a plain whole-scope audit instead. It
   returns
   `{ findings, reviews, coverage, skipped, failures, sweep_advisories, mode, pr }`:
   every finding is already tagged with its `brief`, `review_title`, `scope`, and
   `producer` and conforms to the structured schema (no parsing needed);
   `reviews` carries each returning reviewer's notes (its evidence trail) and
   its declared coverage; `coverage` is the run's coverage account — per
   concern, the shards dispatched against the shards that returned, combined
   with each reviewer's own coverage declaration, with `status` `full`,
   `partial`, `none`, or `not-run`. The combination is downgrade-only: a
   reviewer declaring `partial` marks its concern partial (the declared gaps
   ride in `declared_gaps`), while nothing a reviewer returns can claim more
   than the dispatch record shows, and prose notes never adjust the account
   in either direction. `skipped` lists concerns that did not run (not
   relevant to the diff, no files, wrong occasion, or refused at capacity),
   `failures` lists any reviewer that returned nothing (a coverage gap — see
   Report), and `sweep_advisories` lists full-extent briefs that were split
   without a declared `sweep` (also see Report).

3. **Validate quotes.** Run the zero-model validator over the combined result —
   mechanical code, no judgement, before any checker spends a call:

   ```bash
   python3 ~/.claude/skills/review-panel/scripts/validate_quotes.py --base <base_ref> /tmp/claude/review-panel-combined.json > /tmp/claude/review-panel-validated.json
   ```

   (Omit `--base` on a `--full` run, which has none.) The validator distrusts
   findings wholesale: a `file` that is absolute, escapes the repository,
   is untracked, or sits outside the finding's own scope is suppressed
   unopened; a `code_quote` not found in the cited file (content-exact per
   line, indentation-insensitive) is suppressed — a quote that isn't in the
   file means the finding did not come from reading it; a real quote with an
   off-by-N `line` is corrected in place. The output carries the surviving
   `findings`, the `suppressed_by_validator` list, and `quote_validation`
   counts. Do **not** re-read the files yourself, re-derive the fan-out, or
   rebuild the workflow to "confirm" findings — independent verification is the
   next step's job.

4. **Verify.** Run the verification workflow — an independent checker per
   finding, then the bar check when forced or triggered. Run it on every
   review that dispatched, even with zero surviving findings or `--no-verify`:
   it is what computes and records the bar trigger, and with the checkers off
   and the bar off it makes no agent calls and simply assembles the final
   report. Build its input with the bundled helper, then run it the same way
   as step 2:

   ```bash
   python3 ~/.claude/skills/review-panel/scripts/assemble_verify_input.py \
     /tmp/claude/review-panel-plan.json /tmp/claude/review-panel-validated.json \
     [--no-verify] [--bar off|on|auto] \
     [--coverage-result <path>] [--inspection-record <path>] \
     > /tmp/claude/review-panel-verify-input.json
   node ~/.claude/skills/ensemble-workflow/scripts/ensemble.mjs \
     --json-args "$(cat /tmp/claude/review-panel-verify-input.json)" \
     ~/.claude/skills/review-panel/scripts/verify_workflow.js \
     > /tmp/claude/review-panel-verify.json 2>> /tmp/claude/review-panel-run.log
   ```

   Pass `--no-verify` only when the run was invoked with it; pass `--bar on`
   under `--bar`, `--bar auto` for a headless caller that wants the
   trigger-fired mode, and nothing otherwise. The two coverage flags are for a
   caller — typically a service — that keeps an inspection record and accounts it
   mechanically, and they are unconflated on purpose: `--coverage-result` passes
   the mechanical accounting (a `status` + `omissions`), which hard-gates
   convergence (`coverage_gate` below); `--inspection-record` passes the
   lead-owned record it accounted (changed files, per-hunk read/omitted accounts,
   named definition/call-site references), which reaches the bar as its coverage
   *depth* evidence. An ordinary interactive run keeps no such record and omits
   both.

   Each checker is a fresh clean-context agent that is never the finding's
   producer, preferring the family opposite the producer — Codex checkers for
   the panel's Claude-produced findings, Claude checkers for the Codex leg's
   (recorded per finding as `checked_by`; a family that does not return
   degrades to the other, recorded rather than refused). Checkers return
   per-dimension verdicts and the workflow
   applies them in code: a finding failing **evidence**, **applicability**, or
   **genuine-issue** validity is suppressed as rejected; a **priority**
   disagreement preserves both classes and uses the more severe one, never
   suppressing the finding or silently weakening it; a failed **attribution**
   call marks the finding indeterminate and routes it to the PR (the
   conservative direction). A finding no checker reaches on either engine is
   withheld from posting and recorded as **check-failed** — an infrastructure
   outcome, distinct from rejection; the remediation round re-offers those to
   the checkers when it runs (step 5), and on a run without one, offer to
   re-run them. The workflow's
   output is the run's single, complete report: the surviving `findings` plus
   every record the report step needs (`suppressed_by_checkers`,
   `suppressed_by_validator`, `quote_validation`, `reclassified`,
   `attribution_indeterminate`, `verification`, `bar`, `coverage`,
   `coverage_result`, `inspection_record`, `coverage_gate`, `skipped`,
   `failures`, `reviews`, `sweep_advisories`, `mode`, `pr`).
   `coverage_gate.blocks_convergence` is the mechanical completeness gate: it
   clears only when both coverage artefacts are supplied and the accounting
   result's status is `complete`. A non-`complete` result blocks (partial
   coverage never converges), and so does asymmetric supply — a `--coverage-result`
   without its `--inspection-record` leaves depth unjudged, which cannot support a
   convergence-bearing pass. Either way the review still publishes its verified
   findings, but a caller must not treat it as converged whatever the bar verdict
   says. The gate is inactive (`result_status: null`) only when neither artefact
   was supplied. `inspection_record` rides through to the bar as its depth
   evidence; its content never gates, but its absence beside a result does.

   A service that owns finding policy uses the same workflow in two explicit
   phases. `entry_point: "verification-only"` with `bar_mode: "off"` returns
   an exhaustive `verification_result`: every admitted candidate has a stable
   producer identity and ordinal, `agent-judgement` assurance, proposed and
   verifier priority, the conservative verified priority, and exactly one of
   `verified`, `suppressed`, or `verification-unresolved`. The service then
   constructs and validates its complete disposition manifest. It calls
   `entry_point: "bar-only"` with `verify: false`, `bar_mode: "on"`, that exact
   verification result, the complete manifest, and the manifest's lower-case
   SHA-256 digest. The bar does not rerun finding verification or recompute
   policy; it returns `bar_attestation` bound to the supplied digest. A later
   manifest snapshot, including one changed by confirmed durable delivery,
   requires a fresh bar-only call and fresh attestation.

5. **Remediate — once, when the bar fails.** When step 4's `bar.outcome` is
   `"fail"`, run one remediation round so the run's work is repaired rather
   than discarded: fresh clean-context reviewers re-review what the judge
   implicated — each handed only the complaints the verdict attached to its
   brief — and the whole result is re-verified, ending in a fresh bar
   judgement that is not handed the first verdict. The round replaces an
   implicated brief's *account* — its reviewer notes and coverage entry,
   which are what the judge condemned — while its round-one findings that
   already passed the independent checkers are kept alongside the round's
   new ones: a brief-level complaint about a review's honesty is not
   finding-level evidence against independently confirmed work, and a
   re-discovery at the same site collapses at the poster. The round also
   folds in the run's other recoverable waste:
   reviewer shards that returned nothing are retried, and findings recorded
   as check-failed are re-offered to the checkers. An implicated **Codex
   leg** is the one exception: its external reviewer takes no re-briefing,
   so its round-one result carries forward and the complaint is reported
   rather than remediated (the plan emits the warning); the leg re-runs only
   when it *failed*, which may be transient. On any other bar outcome
   (`pass`, `check-failed`, or the bar not running), skip this step.

   Derive the round's plan mechanically — never hand-pick what to re-run:

   ```bash
   python3 ~/.claude/skills/review-panel/scripts/plan_remediation.py \
     /tmp/claude/review-panel-plan.json /tmp/claude/review-panel-verify.json \
     > /tmp/claude/review-panel-remediation-plan.json
   ```

   If it exits with an `error` — the judge implicated nothing that maps to a
   dispatched brief and no shard failed — there is nothing to mechanically
   re-run: skip to the report and present the bar verdict as it stands.
   Otherwise repeat steps 2–4 on the remediation plan, with these
   differences:

   - **Fan-out**: same ensemble command as step 2, with the remediation plan
     as input (write its result to a distinct file, e.g.
     `review-panel-remediation-result.json`). Run the Codex leg script only
     if the remediation plan's `codex_review` is planned rather than skipped
     — the plan carries the leg forward as skipped when its round-one result
     stands — and merge it exactly as in step 2 when it ran.
   - **Validate**: run the quote validator over the round's result as in
     step 3, to its own output file.
   - **Merge**: fold the round back into the round-one record before
     re-verifying:

     ```bash
     python3 ~/.claude/skills/review-panel/scripts/merge_remediation.py \
       /tmp/claude/review-panel-verify.json /tmp/claude/review-panel-remediation-validated.json \
       > /tmp/claude/review-panel-merged.json
     ```

   - **Re-verify**: assemble and run the verification workflow as in step 4,
     on the original plan and the merged file, passing `--bar on` — the
     round exists because the bar failed, so the merged review is always
     re-judged — and carrying the run's own `--no-verify` election through
     unchanged, so the round never re-enables checkers the caller disabled.
     Round-one survivors carry their recorded checker verdicts through
     without a fresh check (`verification.carried_forward`); only the
     round's new findings spend checkers.

   One round, then stop. If the second bar verdict is also `fail`, do not
   loop again: report both verdicts honestly and let the user decide — a
   review ground through repeated re-judging until a judge relents is worth
   less than an honestly failed one.

6. **Report.** Everything below reads from the final verification output —
   step 4's file, or the re-verify's when step 5 ran. On a remediated run,
   also read the round-one verification file for the gate story: report both
   bar verdicts (the failed first, with its reasons and implicated briefs,
   then the second), and fold both rounds' `suppressed_by_checkers`,
   `reclassified`, and `attribution_indeterminate` records into the
   verification summary — the merged file carries the surviving findings,
   but the first round's gate records live only in its own file. The findings
   that surface are its `findings`. Under `--no-verify`, first apply the manual
   spot check the checkers would otherwise subsume: read each surviving finding
   against its `code_quote` and drop any where the quote does not actually
   support the claim or is generic filler — the validator proved the quote
   exists, not that it means what the finding says. Judge a Codex-leg finding
   (`brief: "codex-review"`) by reading the code at its cited location instead:
   its quote is a mechanical extract of those lines, not evidence the reviewer
   chose, so "does the quote support the claim" is the wrong test for it.

   **Full mode, or diff mode with no PR** — nothing is posted to the forge.
   Present a summary grouped by review title, then by priority (P0 → P3); lead
   with the count and any scope warnings. (In full mode every finding is just a
   finding — `preexisting` is a diff-mode concept and doesn't apply.) In diff
   mode without a PR, `preexisting: true` findings still exist and still route
   to the annexe exactly as below — only the forge posting is absent, not the
   routing.

   **Diff mode with a PR** — publish via the poster. Invoking this skill is the
   authorisation to publish, so do **not** pause to confirm. Assemble one document
   with **every** finding — change-introduced *and* `preexisting: true` — and hand
   it to the poster; it posts the change-introduced findings to the PR and returns
   the pre-existing count for you to route (see below). Write the document to a
   file under `/tmp/claude/` and pass its path — a command beginning with
   `python3`, so it posts without a permission prompt and the JSON never has to
   be shell-quoted:

   ```bash
   python3 ~/.claude/skills/review-panel/scripts/post_pr_comments.py /tmp/claude/review-panel-findings.json
   ```

   The document is `{"pr": <number>, "head_sha": "<sha>", "title": "Review Panel",
   "summary": "<optional one-liner>", "findings": [...]}`, where `head_sha` is
   `git rev-parse HEAD`. Pass the findings through unchanged: each already carries
   `file`, `line`, `side`, `priority`, `title`, `message`, `code_quote`,
   `review_title`, `brief`, `extent`, and `preexisting` from the workflow. `title`
   is the review heading (the default is deliberately generic, as the same poster
   also backs combined runs); `review_title` is the readable name each finding
   groups under.

   The poster collapses exact duplicates first — several concerns validly
   raising the same defect at the same site become one posted finding credited
   to each concern — and a clean run still posts one review ("no issues found",
   pinned to the reviewed commit), so a PR is never left without a review that
   could be mistaken for a run that never happened. It detects the forge from
   the origin remote (github.com → the `gh` CLI; anything else → the Forgejo
   API) and prints a JSON result — act on its `posting` field:
   - **`"gh"` or `"forgejo"`** — the poster posted the PR review itself: one
     review (`event: COMMENT`) with an inline comment per change-introduced
     finding (falling back to body-only if a finding sits off the diff). Report
     from the result: `review` and `stats`.
   - **`"gh_unavailable"` or `"render"`** — `gh` can't write here (the Claude Code
     Web routine, where writes go through the GitHub connector). The poster wrote
     nothing and returned a `render` payload for you to post with your GitHub
     connector tools, **verbatim**: post `render.comment_markdown` as **one**
     comment — prefer a single pull-request review with that text as the body
     if your connector can create reviews, else one conversation comment on
     the PR. One comment, never one per finding. Then report `render.stats`.
   - **`"forgejo_unavailable"`** — the poster could not write to the Forgejo
     instance and there is no connector to hand the payload to. Nothing was
     posted. Report the result's `reason` — usually a missing or unusable entry
     in `~/.config/forgejo/instances.toml`, which the reason spells out how to
     fix — and present the findings as the chat summary instead. Do not try to
     publish the render payload by other means.

   **Never write PR text yourself** — the wording that reaches the forge comes
   from the poster, so internal process vocabulary (the fan-out, file slices,
   reviewer indices, kebab-case names) cannot leak onto it. Posted findings are
   labelled by each concern's readable `review_title`.

   **Pre-existing findings route to the annexe, never the forge.** A
   `preexisting: true` finding describes something the change did not introduce,
   so it does not belong on the PR. When the reviewed project has an annexe
   (the sibling planning repo the project's machine-local guidance points at),
   append the pre-existing findings to its `ISSUES.md` as ingest entries: one
   short entry per finding — what and where (`file:line`), the quoted evidence,
   and the review it came from — or one grouped entry for a full-extent brief's
   whole backlog, so a first audit does not flood the log. Mention in the run
   summary what was routed there. When the project has no annexe — or the
   annexe's `ISSUES.md` cannot be written — the chat report carries them
   instead, as their own section after the change-introduced summary, saying
   why they were not routed. Never silently drop them.

   **Operational detail goes to the run summary only**, never into the PR:
   - **The coverage account**: state coverage from the `coverage` record —
     which concerns were fully covered, which partially, and which not run —
     never from prose in a reviewer's notes. The record already folds in each
     reviewer's structured coverage declaration, downgrade-only; when an entry
     carries `declared_gaps`, report what went unreviewed in the reviewers'
     own words. Any coverage claim in the summary names its source: the
     account, or a command and its output.
   - **Skipped concerns**: report the `skipped` list (name and reason) so the
     user knows which concerns were *not* checked this round — an aspect the
     diff made irrelevant, a subtree with no changes, an occasion that didn't
     match, or a whole-view scope refused at capacity. A skipped concern is an
     unchecked concern, not a passed one.
   - **Failed reviewers**: if the workflow returns any `failures`, report them —
     each is part of a review whose files went **unreviewed** this run, so that
     concern is only partly checked. Name the affected review and offer to re-run.
     A failed Codex leg appears here too — its `coverage` entry carries the
     CLI error that caused it.
   - **The Codex leg's verdict**: when the leg ran, its `reviews` entry's
     notes carry Codex's overall verdict on the patch — include that verdict
     in the run summary alongside the panel's own results. Its coverage entry
     reads `status: external-verdict` (not `full`): report it as the external
     tool's verdict, not as a fully-evidenced reviewer pass.
   - **Verification**: report the `verification` record — findings checked, the
     achieved engine pairing, any degradation — plus what the gates removed:
     `suppressed_by_validator` and `suppressed_by_checkers` (count and one line
     each, distinguishing `rejected` from `check-failed` — a check-failed
     finding was withheld because no checker reached it, so offer to re-run
     those), `reclassified` priorities (old → new), and any
     `attribution_indeterminate` findings (routed to the PR, flagged here as
     indeterminate).
   - **The bar**: report `bar` — whether its trigger fired and why, and, when it
     ran, the verdict with its reasons. `check-failed` is a degraded outcome to
     surface, not a pass. When the trigger fired but the bar did not run
     (`bar_mode: "off"`), mention that `--bar` would have judged this review.
   - **Sweep advisories**: if the workflow returns any `sweep_advisories`,
     surface them. Each is a full-extent brief with no declared `sweep` that was
     split under the per-file default — fine for a per-file concern, wrong for a
     cross-file invariant. Suggest adding `sweep: whole-tree` to the brief if
     its concern spans files. (`/review-brief` sets `sweep` on new briefs; this
     catches older ones.)

## Notes

- Reviewers are deliberately narrow and sceptical of their own findings — a
  concern with zero findings against a small diff is normal and correct. Do not
  pad the report to look thorough.
- The Codex leg needs the `codex` CLI installed and signed in on the reviewing
  machine. Without it the panel still runs in full: the planner records the leg
  as skipped ("codex CLI not found on PATH") and the run reports it with the
  other skips.
- The forge is detected from the origin remote, in both the planner and the
  poster: a github.com remote goes through the `gh` CLI, any other remote is
  treated as a Forgejo (or Gitea) instance and goes through its API. Neither
  path needs you to name the forge or pass credentials — the scripts resolve
  both.
- **GitHub**: `gh` handles reads (PR detection) and, where it can write,
  posting. Where `gh` is read-only (a Claude Code Web routine), the poster
  hands back ready-to-post content for the GitHub connector instead, while
  reads still go through `gh`. With no `gh` at all, diff mode still works
  against the default branch and reports to chat.
- **Forgejo**: the scripts authenticate with the per-host credentials file
  `~/.config/forgejo/instances.toml` — one section per remote host or SSH alias,
  carrying the instance `url` and an API `token`. A token is needed for reads as
  well as writes on instances that require sign-in, so a missing entry surfaces
  as a plan warning ("Cannot check for a PR: …") and the run degrades to a
  default-branch diff with chat output. When that happens, relay the warning's
  fix — add the section it names — rather than treating the run as PR-less by
  design.

## Probation

**Temporary — sunset 2026-08-15:** this skill was substantially revised on 2026-07-15,
so treat friction with it as signal rather than noise. A defect in the skill itself — an
instruction that is wrong or ambiguous, a bundled script, prompt, or binary that is broken,
anything it tells you to do that you end up working around — is a report this window exists
to collect: file it with the foundry-request skill once the task allows, or carry it in your
session report where that skill is not available. A failure of the environment, the task,
or other tooling is not this skill's defect; handle those as you normally would. If today
is after 2026-08-15, this note has outstayed its window: tell the user to remove it.
