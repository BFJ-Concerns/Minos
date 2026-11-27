# Review prompts

The instructions a review's planning, proposing, merging and verifying legs
receive are the Markdown files in this directory. A workflow fills a file's
slots and sends it.

| File | Sent to | Answer |
|---|---|---|
| `exploration.md` | the leg that plans the review | the plan (`explorationSchema` in `review.js`) |
| `specialist.md` | one proposing leg for each planned unit, with that unit's concern from `concerns/` | `specialistSchema` |
| `repository.md` | one proposing leg for each unit of a reviewed repository's `.review/` brief | `specialistSchema` |
| `merge.md` | one leg, between proposing and verifying | `mergeSchema` |
| `verifier.md` | the verifier legs, a batch of findings each | `verifierSchema` |
| `scope.md` | the engagement gate in `review-scope.js` | its own schema there |

`reviewed-change.md`, `writing-a-finding.md` and `severity-scale.md` are
sections several of those prompts share. They are never sent on their own.

The schemas are in `workflows/review-contracts.mjs`, with what the merge and
verifier legs are shown of each finding.

## Concerns

Each file in `concerns/` is one specialist concern, and the directory is the
whole list: the file's name is the concern's name. Its front matter carries
one key, `summary`, a clause that completes "reads the change for …". The
planner and the other specialists are shown the summary; only the concern's
own specialist is shown the body. A concern's body is about its own ground
and ends by naming what belongs to the others.

## Slots

A prompt is its file with every `{{slot}}` filled.

File slots take the text of another file here: `{{reviewed-change}}`,
`{{writing-a-finding}}`, `{{severity-scale}}`, and `{{concern}}`, which takes
a concern file's body without its front matter.

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
| `{{assignment}}` | the scope and extent a `.review/` brief's reviewer was given |
| `{{findings}}` | the findings as a JSON array, with the fields `review-contracts.mjs` lists for that stage |

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

`workflows/finding-disposition.mjs` says what each verdict makes of its
finding, and what becomes of one left unsettled.
