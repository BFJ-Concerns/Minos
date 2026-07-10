---
name: agent-review
description: Convene a panel of review agents from a repo's `.review/` briefs — a clean-context agent per brief, splitting a large per-file brief across several reviewers, each scoped to a subtree when its folder matches a repo directory; every finding is quote-checked in code and verified by an independent checker before it posts, and a review-bar judge can assess the assembled review. Use when asked to run an agent review, review the branch/PR against the project's review briefs, audit the codebase against `.review/`, baseline a new brief, or run an occasion-specific (release, nightly) brief review. Diff mode (default) reviews branch changes and posts review comments on the PR, raising any pre-existing issues separately; full mode audits all in-scope code and reports to chat. Not for general bug-hunting on a diff (use code-review), security-only review (use security-review), or authoring and refining the briefs themselves (use review-brief).
argument-hint: '[--full] [--base <ref>] [--occasion <name>] [--no-verify] [--bar] [brief …]'
allowed-tools:
- Bash(git rev-parse:*)
- Bash(gh pr view:*)
- Bash(python3 *plan_review.py*)
- Bash(python3 *validate_quotes.py*)
- Bash(python3 *assemble_verify_input.py*)
- Bash(python3 *post_pr_comments.py*)
- Bash(node:*)
- Read
- Grep
- Glob
- Write(/tmp/claude/**)
---

# Agent Review

Convene a panel of review agents over a repository's `.review/` briefs. Each
brief is a standing instruction for one reviewer — a concern that needs
judgement rather than a deterministic linter ("keep error copy neutral", "new
endpoints should rate-limit", "watch for accessibility regressions"). The skill
fans out clean-context agents over the briefs — blind to one another, scoped to
the relevant subtree, a large per-file brief split across several reviewers —
collates their findings, and then puts every finding through the verification
gate before anything posts.

This is the rule-driven, fan-out-per-concern counterpart to `code-review`. It
does **not** hunt for arbitrary bugs — it checks the code against the repo's own
briefs. For general correctness review use `/code-review`; for security use
`/security-review`.

## The `.review/` convention

A `.review/` directory at the repo root holds one markdown **brief** per file.
A brief's position determines what it reviews:

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
authoring; this skill reads them.

## Runs

| Invocation | What runs | Output |
|------------|-----------|--------|
| `/agent-review` | Every applicable brief (occasion-restricted ones excepted), each at its declared extent, vs the branch | Findings posted to the PR; pre-existing issues raised separately; chat recap. Chat-only if no PR |
| `/agent-review <brief>…` | Only the named brief(s), at their declared extent | as above |
| `/agent-review --full [<brief>…]` | The named briefs (or all), forced to full extent | Chat summary grouped by review |
| `/agent-review --base <ref> [<brief>…]` | Diff against `<ref>` instead of the auto-detected base | as the default run |
| `/agent-review --occasion <name> …` | As the run would otherwise, with occasion-declaring briefs selected by `<name>` | as the run would otherwise |
| `/agent-review --no-verify …` | As the run would otherwise, skipping per-finding verification | as the run would otherwise |
| `/agent-review --bar …` | As the run would otherwise, plus the bar check on the assembled review | as the run would otherwise, plus the bar verdict |

A default run honours each brief's extent: diff-extent briefs see only changed
files; full-extent briefs audit their whole scope and classify each finding so
change-caused ones still reach the PR (see Report). **`--full`** overrides every
selected brief to a whole-scope audit and reports to chat — use it to baseline a
new brief against existing code (`/agent-review --full <new-brief>`) without
dragging every other brief through a sweep. **Brief selection** — naming briefs —
restricts any run to those briefs.

Every finding is quote-validated in code and then verified by an independent
checker by default (see steps 3–4); **`--no-verify`** skips the checkers (the
quote validator always runs — it is near-free and catches the fabrications).
**`--bar`** opts in to the bar check interactively. A headless caller wanting
the bar fired by its computed trigger instead — the review would otherwise
converge clean, or a criterion was skipped, failed, or partially covered —
passes `bar_mode: "auto"` at step 4 rather than `--bar`.

**Base resolution** (diff mode only) decides what the branch is compared against,
most explicit first: a PR's base when there's a PR, otherwise an auto-detected
default branch (`origin/HEAD`, then `main`/`master`). When neither is right — a
branch that forks off another branch, or a repo with an unusual default — pass
**`--base <ref>`** to name what to diff against. This is the only supported
non-PR shape: agent-review reviews a PR or a branch against its base, nothing
else.

**EMPTY DIFF is a distinct outcome, never a clean pass.** A diff run whose
changed set is empty returns `outcome: "empty-diff"`: every diff-extent brief is
skipped (nothing changed to review), while full-extent briefs still plan and
dispatch — they audit their whole scope regardless of the diff. Report the
branch-review portion as exactly that — the diff was empty, nothing on the
branch was reviewed — never as a clean pass or "no findings": with a *guessed*
base (`base_source: "default"`) it as often means the wrong base as a clean
branch, so say the base was guessed and suggest `--base <ref>`. A failed diff
command is a planner `error`, not an empty diff.

A full-scope audit covers every in-scope tracked file. A **per-file** brief is
split across several reviewers when its scope is large, so the load stays bounded
however big the tree. A **whole-tree** brief can't be split — it is always one
reviewer — and beyond the whole-view capacity (a file count `plan_review.py`
measures against its budget) the plan refuses to dispatch it at all, reporting it
as not run with the measured size, rather than let one reviewer skim a scope it
cannot hold. Keep whole-tree briefs scoped to a subtree.

## Workflow

The run's agent stages are bundled ensemble workflows — deterministic scripts
executed by the ensemble runtime that ships with the `ensemble-workflow` skill:
one fans reviewers out over the plan, one verifies the findings and judges the
bar. The dispatch (one agent per shard, every brief), the slice of files each
reviewer covers, the findings shape, the checker pairing, and the downgrade
rules are all fixed by code rather than left to the model; the agents' judgement
stays non-deterministic — that is the point — but what they are pointed at, and
the structure they return, are not. Invoking the bundled workflows from this
skill is the user's opt-in to multi-agent orchestration — do not ask again
before running them. The agents dispatch as clean-context background sessions,
so the runtime's live requirements apply; if the runtime or its primary engine
is unavailable, stop and report that rather than hand-rolling a substitute
fan-out. (The verification stage prefers the opposite model family and degrades
to the reviewers' own, recording the achieved pairing — that degradation is the
workflow's to make, not yours.)

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

The plan decides the fan-out; run the bundled workflows as written. A brief with a
large file set is **deliberately split across several reviewers by the plan** —
that is the workflow doing what it is for, iterating a large group across agents,
and it is fixed in code (`plan_review.py` sizes each brief and shards it). The
groundedness guards are likewise fixed: each reviewer must quote the code it flags
(step 2), the quote validator checks those quotes in code (step 3), and the
verification workflow pairs each finding with an independent checker (step 4).
What is **not** allowed is improvising on top of that at runtime: do not rewrite
the scripts, re-derive or re-balance the shards by hand, or stand up your own
extra confirmation pass alongside the scripted ones. The plan handles its own
refusals — a whole-tree brief beyond the whole-view capacity is marked not-run
with the measured size while the rest of the run proceeds; relay that refusal
(the fix is usually to scope the brief to a subtree). Stop the whole run only on
a plan-level `error`, and report what blocked it rather than engineering around
it silently.

1. **Plan.** Run the discovery script from the target repo, translating the
   slash arguments: `--full` → `--full`, `--base <ref>` → `--base <ref>`,
   `--occasion <name>` → `--occasion <name>`, and any bare words → brief names
   to restrict to. (`--no-verify` and `--bar` are step-4 inputs, not planner
   flags.)

   ```bash
   python3 ~/.claude/skills/agent-review/scripts/plan_review.py [--full] [--base <ref>] [--occasion <name>] [brief-name …] > /tmp/claude/agent-review-plan.json
   ```

   Then read the file to inspect the plan. When the user's request names an
   occasion in words ("run the release review") rather than as a flag, translate
   it to `--occasion <token>` using the tokens the repo's briefs declare.

   **If the plan's `outcome` is `"empty-diff"`**, every diff-extent brief has
   been skipped. If no brief remains active, stop here and report the EMPTY DIFF
   outcome as described under Runs — no phrasing of that report may read as a
   clean pass. If full-extent briefs remain active, continue: they audit
   regardless of the diff, and the branch-review portion is still reported as
   EMPTY DIFF alongside their results.

   Each brief in the plan carries its effective `extent` (`diff` or `full`),
   `sweep` (`per-file` or `whole-tree`), and `occasions` alongside its `scope`,
   the size of its file set as `file_set_size`, and a `shards` list — one entry
   per reviewer. A small brief, or any whole-tree brief, has a single shard; a
   large per-file brief has several, each describing a slice of the scope. An
   unsharded diff brief's shard carries the explicit changed `files`; every other
   shard carries a 1-based line range the reviewer turns into its slice by
   enumerating the scope itself, so no large file list is ever relayed through
   the workflow input (see the fan-out note in step 2). A skipped brief carries
   `skip_reason` and `skip_kind` (`empty`, `occasion`, or `capacity`) instead of
   shards.

   It returns JSON: the briefs, the detected `pr`, the `base_ref`, the
   `base_source`, `warnings`, and the three static method-file paths
   (`template_path` for reviewers, `checker_method_path` for the per-finding
   checkers, `bar_method_path` for the bar judge). If it returns an `error` (no
   git repo, no `.review/`, no briefs, a missing method file, or an unresolvable
   `--base`), report it and stop. Surface any `warnings` to the user — they
   usually mean a brief is mis-scoped.

   Tell the user what was diffed: the `base_ref` and how it was chosen
   (`base_source` is `pr`, `explicit`, or `default`). When it's `default` the
   base was *guessed* — say so, and if the run found little, treat that as
   possibly the wrong base and suggest `--base <ref>`. When a brief's `shards`
   list has more than one entry, mention the split (the workflow logs it too) so
   the user sees a large sweep was spread across reviewers. A brief skipped at
   `capacity` was refused, not passed — relay its reason, which names the
   measured size and the fixes (scope to a subtree, or declare
   `sweep: per-file`).

2. **Fan out via the ensemble runtime.** Run the bundled workflow through the
   runtime, wrapping the plan file from step 1 as the script's JSON input:

   ```bash
   node ~/.claude/skills/ensemble-workflow/scripts/ensemble.mjs \
     --json-args "{\"plan\": $(cat /tmp/claude/agent-review-plan.json)}" \
     ~/.claude/skills/agent-review/scripts/review_workflow.js \
     > /tmp/claude/agent-review-result.json 2> /tmp/claude/agent-review-run.log
   ```

   The runtime prints the workflow's return value as one line of JSON on stdout —
   read `/tmp/claude/agent-review-result.json` for the result; the stderr log
   carries the split notices. Leave the runtime's tuning flags (budget,
   concurrency, timeout) unset.

   Do **not** relay the reviewer prompt through the input. It is static
   (`prompts/reviewer-method.md`), the plan carries its path (`template_path`),
   and each reviewer reads it itself — relaying that multi-kilobyte,
   backtick-heavy prose through the workflow input causes JSON parse errors: a
   single garbled character kills the run before any reviewer starts. Keep the
   input to just the compact plan.

   The workflow launches one schema-validated agent per shard, in parallel: one
   agent for a small or whole-tree brief, several for a large per-file brief, each
   covering a slice of the scope. Each agent reads the shared reviewer method and
   its own brief file, and reviews only its assigned files — reviewers stay blind
   to one another. A **diff-extent** brief inspects the branch diff for its changed
   files and flags what the change introduces or touches; a **full-extent** brief
   enumerates its scope (with `git ls-files`) and audits it, classifying each
   finding as change-caused (`preexisting: false`) or independent
   (`preexisting: true`). A `--full` run forces every brief to a plain whole-scope
   audit instead. It returns
   `{ findings, reviews, coverage, skipped, failures, sweep_advisories, mode, pr }`:
   every finding is already tagged with its `brief`, `review_title`, `scope`, and
   `producer` and conforms to the structured schema (no parsing needed);
   `reviews` carries each returning reviewer's notes (its evidence trail);
   `coverage` is the dispatch-derived coverage account — per brief, the shards
   dispatched against the shards that returned, with `status` `full`, `partial`,
   `none`, or `not-run`. That account is the run's coverage claim; a reviewer's
   own notes never override it. `skipped` lists briefs that did not run (no
   files, wrong occasion, or refused at capacity), `failures` lists any reviewer
   that returned nothing (a coverage gap — see Report), and `sweep_advisories`
   lists full-extent briefs that were split without a declared `sweep` (also see
   Report).

3. **Validate quotes.** Run the zero-model validator over the workflow result —
   mechanical code, no judgement, before any checker spends a call:

   ```bash
   python3 ~/.claude/skills/agent-review/scripts/validate_quotes.py --base <base_ref> /tmp/claude/agent-review-result.json > /tmp/claude/agent-review-validated.json
   ```

   (Omit `--base` on a `--full` run, which has none.) The validator distrusts
   findings wholesale: a `file` that is absolute, escapes the repository,
   is untracked, or sits outside the finding's own brief scope is suppressed
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
   python3 ~/.claude/skills/agent-review/scripts/assemble_verify_input.py \
     /tmp/claude/agent-review-plan.json /tmp/claude/agent-review-validated.json \
     [--no-verify] [--bar off|on|auto] > /tmp/claude/agent-review-verify-input.json
   node ~/.claude/skills/ensemble-workflow/scripts/ensemble.mjs \
     --json-args "$(cat /tmp/claude/agent-review-verify-input.json)" \
     ~/.claude/skills/agent-review/scripts/verify_workflow.js \
     > /tmp/claude/agent-review-verify.json 2>> /tmp/claude/agent-review-run.log
   ```

   Pass `--no-verify` only when the run was invoked with it; pass `--bar on`
   under `--bar`, `--bar auto` for a headless caller that wants the
   trigger-fired mode, and nothing otherwise.

   Each checker is a fresh clean-context agent that is never the finding's
   producer, preferring the opposite model family (recorded per finding as
   `checked_by`; a family that does not return degrades to the other, recorded
   rather than refused). Checkers return per-dimension verdicts and the workflow
   applies them in code: a finding failing **evidence**, **applicability**, or
   **genuine-issue** validity is suppressed as rejected; a dishonest
   **priority** is reclassified, never suppressed; a failed **attribution**
   call marks the finding indeterminate and routes it to the PR (the
   conservative direction). A finding no checker reaches on either engine is
   withheld from posting and recorded as **check-failed** — an infrastructure
   outcome, distinct from rejection; offer to re-run those. The workflow's
   output is the run's single, complete report: the surviving `findings` plus
   every record the report step needs (`suppressed_by_checkers`,
   `suppressed_by_validator`, `quote_validation`, `reclassified`,
   `attribution_indeterminate`, `verification`, `bar`, `coverage`, `skipped`,
   `failures`, `reviews`, `sweep_advisories`, `mode`, `pr`).

5. **Report.** Everything below reads from step 4's output file. The findings
   that surface are its `findings`. Under `--no-verify`, first apply the manual
   spot check the checkers would otherwise subsume: read each surviving finding
   against its `code_quote` and drop any where the quote does not actually
   support the claim or is generic filler — the validator proved the quote
   exists, not that it means what the finding says.

   **Full mode, or diff mode with no PR** — chat only. Present a summary grouped
   by review title, then by priority (P0 → P3); lead with the count and any scope
   warnings. Nothing is posted to the forge. (In full mode every finding is just a
   finding — `preexisting` is a diff-mode concept and doesn't apply.)

   **Diff mode with a PR** — publish via the poster. Invoking this skill is the
   authorisation to publish, so do **not** pause to confirm. Assemble one document
   with **every** finding — change-introduced *and* `preexisting: true` — and hand
   it to the poster; it routes them (change-introduced → the PR, pre-existing →
   their own issues on the forge) and de-duplicates the issues against open ones.
   Write the document to a file under `/tmp/claude/` and pass its path — a command
   beginning with `python3`, so it posts without a permission prompt and the JSON
   never has to be shell-quoted:

   ```bash
   python3 ~/.claude/skills/agent-review/scripts/post_pr_comments.py /tmp/claude/agent-review-findings.json
   ```

   The document is `{"pr": <number>, "head_sha": "<sha>", "title": "Agent Review",
   "summary": "<optional one-liner>", "findings": [...]}`, where `head_sha` is
   `git rev-parse HEAD`. Pass the findings through unchanged: each already carries
   `file`, `line`, `side`, `priority`, `title`, `message`, `code_quote`,
   `review_title`, `brief`, `extent`, and `preexisting` from the workflow. `title`
   is the review heading (the default is deliberately generic, as the same poster
   also backs combined runs); `review_title` is the readable name each finding
   groups under; `extent` lets the poster route pre-existing findings (a full
   brief's become one rollup issue, a diff brief's become individual issues).

   The poster detects the forge from the origin remote (github.com → the `gh`
   CLI; anything else → the Forgejo API) and prints a JSON result — act on its
   `posting` field:
   - **`"gh"` or `"forgejo"`** — the poster did everything itself: one PR review
     (`event: COMMENT`) with an inline comment per change-introduced finding
     (falling back to body-only if a finding sits off the diff), plus issues for
     pre-existing findings — one per incidental (diff-extent) finding, and a
     single rollup checklist issue per full-extent brief, so a full audit can't
     flood the tracker. Report from the result: `review`, `issues.created`,
     `issues.skipped_existing`, and any `issues.dedup_note`.
   - **`"gh_unavailable"` or `"render"`** — `gh` can't write here (the Claude Code
     Web routine, where writes go through the GitHub connector). The poster wrote
     nothing and returned a `render` payload for you to post with your GitHub
     connector tools, **verbatim**:
     - If `render.comment_markdown` is not null, post it as **one** comment —
       prefer a single pull-request review with that text as the body if your
       connector can create reviews, else one conversation comment on the PR. One
       comment, never one per finding.
     - For each item in `render.issues_to_create`, create an issue with its
       `title` and `body` exactly as given.
     - Then report `render.stats`, the created issues, `render.issues_existing`,
       and any `render.dedup_note`.
   - **`"forgejo_unavailable"`** — the poster could not write to the Forgejo
     instance and there is no connector to hand the payload to. Nothing was
     posted. Report the result's `reason` — usually a missing or unusable entry
     in `~/.config/forgejo/instances.toml`, which the reason spells out how to
     fix — and present the findings as the chat summary instead. Do not try to
     publish the render payload by other means.

   **Never write PR or issue text yourself** — all outward-facing wording comes
   from the poster, so internal process vocabulary (the fan-out, file slices,
   reviewer indices, kebab-case brief names) cannot leak onto the forge. Reviews
   and issues are labelled by each brief's readable `review_title`.

   **Operational detail goes to the run summary only**, never into a PR or issue:
   - **The coverage account**: state coverage from the dispatch-derived
     `coverage` record — which criteria were fully covered, which partially, and
     which not run — never from a reviewer's own description of what it did. Any
     coverage claim in the summary names its source: the account, or a command
     and its output.
   - **Skipped briefs**: report the `skipped` list (name and reason) so the user
     knows which concerns were *not* checked this round — a subtree with no
     changes, an occasion that didn't match, or a whole-view scope refused at
     capacity. A skipped brief is an unchecked concern, not a passed one.
   - **Failed reviewers**: if the workflow returns any `failures`, report them —
     each is part of a review whose files went **unreviewed** this run, so that
     concern is only partly checked. Name the affected review and offer to re-run.
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
  brief with zero findings against a small diff is normal and correct. Do not
  pad the report to look thorough.
- The forge is detected from the origin remote, in both the planner and the
  poster: a github.com remote goes through the `gh` CLI, any other remote is
  treated as a Forgejo (or Gitea) instance and goes through its API. Neither
  path needs you to name the forge or pass credentials — the scripts resolve
  both.
- **GitHub**: `gh` handles reads (PR detection, issue de-duplication) and, where
  it can write, posting. Where `gh` is read-only (a Claude Code Web routine),
  the poster hands back ready-to-post content for the GitHub connector instead,
  while reads still go through `gh`. With no `gh` at all, diff mode still works
  against the default branch and reports to chat.
- **Forgejo**: the scripts authenticate with the per-host credentials file
  `~/.config/forgejo/instances.toml` — one section per remote host or SSH alias,
  carrying the instance `url` and an API `token`. A token is needed for reads as
  well as writes on instances that require sign-in, so a missing entry surfaces
  as a plan warning ("Cannot check for a PR: …") and the run degrades to a
  default-branch diff with chat output. When that happens, relay the warning's
  fix — add the section it names — rather than treating the run as PR-less by
  design.
