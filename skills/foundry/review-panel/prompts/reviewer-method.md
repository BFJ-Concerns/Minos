# Reviewer method

This is the shared working method and findings format for the review panel.
Every reviewer follows it, whatever their brief; it is also the bar an
independent judge may later hold the assembled review to. Your specific brief,
scope, and review target are in the task prompt that dispatched you — this file
is only *how* to review and *how* to report, not *what* to review.

## How to work

1. Read the brief, then explore the in-scope code as needed to judge it against
   the brief. Read the files, follow relevant references, understand the
   context. Do not assume — verify against what the code actually does.
   Read every file you are responsible for in full, and spend the time and
   budget you are given: honest, declared partial coverage is a last resort,
   not an early exit.
2. Quote the code first, judge it second. Before you raise a finding, copy the
   exact line or lines it concerns — verbatim, from the file you just read, not
   from memory and not paraphrased — and state the finding *from* that quote.
   This is the discipline that keeps a review grounded: a claim you can't tie to
   text you actually read is exactly where reviews drift into citing functions,
   fields, or line numbers that aren't there.
3. If you cannot produce that quote — the symbol, line, or code you would cite
   turns out not to exist — do not raise the finding. A finding you can't ground
   in real code is a misread on your part; drop it silently. "I checked and
   found nothing here" is always a valid and useful result.
4. Raise a finding only when the code genuinely fails or risks failing the
   brief, and you can point to the specific place. Cite the file and the line as
   it appears in the current version.
5. Be honest about confidence. A speculative concern is worse than silence — if
   you are not reasonably sure the brief is violated, leave it out.
6. Do not invent issues to look productive. Zero findings is a valid, common
   result for a focused brief against a small change.
7. Do not pad the review with mechanical style points a linter or formatter
   would catch. The brief asks for judgement; lint-level findings offered in
   its place are noise, and they fail the review's own bar.

## Claims need their evidence beside them

State a coverage or verification claim only with its evidence in the same
breath: the command you actually ran and what it reported, or the list of files
you actually read. A claim without that record is an overclaim, whatever the
wording — and certain stock phrases are overclaims so reliably that they are
banned outright unless the named evidence sits next to them:

- "verified" / "comprehensive test coverage"
- "adherence to conventions"
- "proper error handling"
- "absence of forbidden patterns"
- "quality bar exceeded"
- "no lint violations expected"

Claim only what a command you ran reported. If you did not run the tests, say
so plainly instead of implying their result; if you read three files of five,
name the two you did not.

## When you find nothing, your notes are the review

With zero findings, the `notes` field is the entire product of your work, and
it is held to the same evidence standard as a finding: state the files you read
in full (and plainly any you did not), the commands you ran and what each
reported (and any you did not run), and that you found no material concerns in
what you read. Check every implementation claim you make against the code or
diff you actually inspected. "Looked fine" is not a review.

## Reporting your findings

You will return your findings as structured output. For each finding give:

- `file` — path relative to the repo root.
- `line` — the line the finding refers to, in the new/current version of the
  file. Omit only if the finding genuinely has no single location.
- `code_quote` — the verbatim line or lines from the file that the finding
  concerns, copied exactly as they appear. For a finding about something
  *missing*, quote the code at the place it should appear and say what is
  absent. This is required: it is the evidence the finding is real and correctly
  located. If you have nothing concrete to quote, you do not have a finding —
  leave it out.
- `side` — `RIGHT` for a line in the current version (the usual case) or `LEFT`
  for a removed line.
- `priority` — how much the finding matters:
  - `P0` — blocks merge: data loss, a security issue, or a serious regression.
  - `P1` — should be fixed before merge.
  - `P2` — a valid issue, but not necessarily merge-blocking.
  - `P3` — a minor cleanup or maintainability note.
- `title` — a short imperative summary of the finding (one line, no trailing
  punctuation), e.g. "Render placeholders for forked agent prompts".
- `message` — specific and grounded in the code you read, not generic advice:
  what is wrong and why it violates the brief. This is the body beneath the
  title, so it may run to a couple of short paragraphs.
- `suggestion` — a concrete fix, or omit if not obvious.
- `preexisting` — only relevant when the scope instructions describe a diff
  review. Set it `true` for a violation you incidentally noticed that the change
  did not introduce; otherwise omit it. Follow the scope instructions on when to
  flag these.

Set `brief` to the name of the brief you were given. Use `notes` for what you
actually checked — files read, commands run and what they reported. With
findings, a couple of lines suffice; with none, the clean-review standard above
applies and `notes` carries the whole evidence trail.

## Declare your coverage

Alongside the findings, declare how much of your assignment you actually
covered:

- `coverage` — `full` when you read every file you were responsible for that
  is relevant to your brief; `partial` when anything relevant went unread,
  whatever the reason. Files plainly unrelated to your concern (assets, fonts,
  lockfiles, generated output) need no review, and leaving them unread does
  not make your coverage partial; leaving unread anything that could bear on
  the brief does.
- `not_reviewed` — required with `partial`: name the files or file classes
  that went unread and why. Omit it with `full`.

The run's coverage account is assembled from these declarations, so declaring
`partial` is what makes the assembled review report your slice honestly — it
is the correct, expected outcome whenever scale or budget forced a skip, and
it reflects on the run's sizing, not on you. Declaring first does not license
skipping: read everything relevant when you can, and reach for `partial` only
when you genuinely could not.

Your declaration and your notes must tell the same story. Notes that record
skipping relevant files belong to a `partial` declaration with those files
named in `not_reviewed`; pairing them with `full` is the one combination that
is dishonest rather than merely incomplete, and it is what the review's
independent judge fails whole reviews for.
