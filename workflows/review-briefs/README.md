# Review prompts

The instructions a review's planning, proposing, merging and verifying legs
receive are the Markdown files in this directory. A workflow fills a file's
slots and sends it.

| File | Sent to | Answer |
|---|---|---|
| `exploration.md` | the leg that plans the review | the plan (`explorationSchema`) |
| `specialist.md` | one proposing leg for each planned unit, with that unit's concern from `concerns/` | `specialistSchema` |
| `brief-relevance.md` | one leg, for the `.review/` briefs that run only if their relevance condition is met | its own schema in `review-briefs.js` |
| `brief-partition.md` | one leg for each `.review/` brief that reads its whole part with a `per-file` sweep | its own schema in `review-briefs.js` |
| `repository.md` | one proposing leg for each unit of a reviewed repository's `.review/` brief | `specialistSchema` |
| `merge.md` | one leg, between proposing and verifying | `mergeSchema` |
| `verifier.md` | the verifier legs, a batch of findings each | `verifierSchema` |
| `scope.md` | the engagement gate in `review-scope.js` | its own schema there |

`reviewed-change.md`, `writing-a-finding.md` and `severity-scale.md` are
sections several of those prompts share. They are never sent on their own.
Neither are the files in `assignment/`, which make up the value of one slot
of `repository.md`, nor the passages a repository's settings choose
(*Settings* below).

The schemas are in `workflows/review-contracts.mjs`, with what the merge and
verifier legs are shown of each finding.

## Concerns

Each file in `concerns/` is one specialist concern, and the directory is the
whole list: the file's name is the concern's name. Its front matter carries
one key, `summary`, a clause that completes "reads the change for …". The
planner and the other specialists are shown the summary; only the concern's
own specialist is shown the body. A concern's body is about its own ground
and ends by naming what belongs to the others.

## Assignments

`repository.md` tells a brief's reviewer what it was given to judge in
`{{assignment}}`. The value is built from the files in `assignment/`, each
one sentence with run slots of its own, and those that apply are joined in
this order with a blank line between them:

| File | Applies to |
|---|---|
| `changed-part.md` | a brief that reads what the change did (`extent: diff`) |
| `whole-part.md` | a brief that reads its whole part as it stands (`extent: full`) |
| `unit.md` | a unit of a partitioned part |
| `files.md` | a unit, or a whole part, whose files are listed |

Each file is filled in a pass of its own before the result becomes the
value of `{{assignment}}`.

## Settings

Some of what a prompt says depends on a per-repository setting. Each such
passage has a slot and a directory of the slot's name, holding one file for
each value the setting takes, named for the value. The slot takes the file
for the repository's value.

| Slot | In | Setting | Files |
|---|---|---|---|
| `{{safeguard-requests}}` | `specialist.md`, `repository.md` | `review.file-safeguard-requests` | `false.md`: reviewers leave out requests for a safeguard against a situation nothing suggests will arise. `true.md`: they report them, written as requests |
| `{{verifier-safeguard-requests}}` | `verifier.md` | `review.file-safeguard-requests` | `false.md`: empty, so a verifier judges every finding as a claimed fault. `true.md`: what each verdict means for a request for a safeguard, and that a confirmed one is filed as a suggestion and never sent to the author as a defect |

A file is empty where its value adds nothing to the prompt, and it is still
there. A setting that changes two prompts in different ways has a slot and
a directory for each passage. `workflows/prompt-settings.mjs` lists the
settings, their values and their slots.

## Slots

A prompt is its file with every `{{slot}}` filled.

File slots take the text of another file here: `{{reviewed-change}}`,
`{{writing-a-finding}}`, `{{severity-scale}}`, `{{concern}}`, which takes
a concern file's body without its front matter, and the setting slots
above.

Run slots take a value from the run:

| Slot | Value |
|---|---|
| `{{target}}`, `{{head}}` | the commits bounding the change under review |
| `{{project-documents}}` | each guidance document in configured order, in a `<project-guidance>` block naming its path |
| `{{pull-request}}` | the title and description in a `<pull-request-description>` block, or nothing when the pull request has neither |
| `{{concern-name}}` | the concern's name |
| `{{other-concerns}}` | a list line for every other concern: its name and summary |
| `{{concerns}}` | the same line for every concern |
| `{{planned-focus}}` | the planned unit's `concern` sentence and its `scope` paths |
| `{{same-concern-units}}` | every other planned unit of the same concern, each in the form `{{planned-focus}}` takes, or nothing when the unit is its concern's only one |
| `{{brief-path}}`, `{{brief}}` | a `.review/` brief's path and text |
| `{{assignment}}` | the scope and extent a `.review/` brief's reviewer was given, built from the files in `assignment/` |
| `{{brief-directory}}` | the directory a brief covers, as a path from the repository root with a trailing slash; `./` for a brief at the top of `.review/` |
| `{{unit-concern}}` | the `concern` sentence the partition gave the unit |
| `{{unit-files}}` | a list line for each of the unit's files, or of the whole part's where it was not partitioned |
| `{{changed-paths}}` | every path the change touches, as a JSON array |
| `{{relevance-conditions}}` | the briefs awaiting a relevance judgement, as a JSON array of `brief` (the path) and `relevance` (the condition) |
| `{{file-inventory}}` | every tracked file in the part a brief covers, as a JSON array of paths |
| `{{findings}}` | the findings as a JSON array, with the fields `review-contracts.mjs` lists for that stage |
| `{{changed-files}}` | every path the change touches, as a JSON array of `path`, `added` and `deleted`, the lines the change added to that file and removed from it: zero for both on a binary file, and a moved file listed under both its paths |

File slots are filled first and run slots after, in one pass, so that a
value is never itself read for slots: a pull-request description may contain
anything. A slot left unfilled, or a slot name outside these lists, stops the
workflow before it dispatches.

## Stages

A review proposes, merges, then verifies.

1. Every proposing leg's `findings` and `outsideConcernFindings` are proposed
   findings alike.
2. The merge leg groups all of a workflow's proposed findings at once, before
   they are batched for verification. It is skipped when there are fewer than
   two. Each group goes on as one finding: its primary write-up, with the
   group's other write-ups beside it.
3. A verifier leg gives each finding one verdict, which stands for every
   write-up in its group. A finding a verifier raises itself, in
   `newFindings`, is a proposed finding too: it is merged with whatever else
   its round raised and verified in the next round, by a leg other than the
   one that raised it. A workflow runs `VERIFICATION_ROUNDS` rounds
   (`review-contracts.mjs`), and what the last of them raises has no
   verdict, which leaves the run incomplete.

`workflows/review-stages.mjs` runs the merge and the verification for both
review workflows. `workflows/finding-disposition.mjs` says what each verdict
makes of its finding, and what becomes of one left unsettled.
