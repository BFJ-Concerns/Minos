# Review prompts

The instructions a review's planning, proposing, merging and verifying legs
receive are the Markdown files in this directory. A workflow fills a file's
slots and sends it.

| File | Sent to | Answer |
|---|---|---|
| `exploration.md` | the leg that plans the review | the plan (`explorationSchema`) |
| `specialist.md` | one proposing leg for each planned unit, with that unit's concern from `concerns/` | `specialistSchema` |
| `general.md` | the general reviewers of a quick review, which has no plan and no concerns: one leg given the whole change, and one for each share where that leg divided the change | `generalWholeChangeSchema` from the leg given the whole change, `generalSchema` from a share's |
| `brief-relevance.md` | one leg, for the `.review/` briefs that run only if their relevance condition is met | its own schema in `review-briefs.js` |
| `brief-partition.md` | one leg for each `.review/` brief that reads its whole part with a `per-file` sweep | its own schema in `review-briefs.js` |
| `repository.md` | one proposing leg for each unit of a reviewed repository's `.review/` brief | `specialistSchema` |
| `merge.md` | one leg, between proposing and verifying | `mergeSchema` |
| `verifier.md` | the verifier legs, a batch of findings each | `verifierSchema` |

`reviewed-change.md`, `what-makes-a-finding.md`, `writing-a-finding.md` and
`severity-scale.md` are sections several of those prompts share. They are
never sent on their own. Neither are the files in `assignment/` and
`general-assignment/`, which make up the value of one slot of
`repository.md` and of `general.md`, nor the passages a repository's
settings choose (*Settings* below).

Which of `specialist.md` and `general.md` a pull request's main review is
made with is its review mode (`workflows/review-mode.mjs`). The other
prompts are the same under both.

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

## General assignments

`general.md` tells a general reviewer what it was given in a slot of the
same name, `{{assignment}}`, whose value is one of the two files in
`general-assignment/`, filled in a pass of its own:

| File | Given to |
|---|---|
| `whole-change.md` | the reviewer given the whole change. It says when a change is divided and how the division is returned, in `shares` |
| `share.md` | the reviewer of one share of a divided change. It carries that share and the others' |

Whether a change is too large for one reader is the judgement of the
reviewer given the whole of it, made before it reads closely, so that an
ordinary change costs one proposing leg and no planning leg. A change is
divided once: a share's reviewer answers on a schema with no `shares`. A
share is what its reviewer must get through and never a bound on what it
reads or reports, as a planned unit's scope is for a specialist that
shares its concern.

## Settings

Some of what a prompt says depends on a per-repository setting. Each such
passage has a slot and a directory of the slot's name, holding one file for
each value the setting takes, named for the value. The slot takes the file
for the repository's value.

| Slot | In | Setting | Files |
|---|---|---|---|
| `{{safeguard-requests}}` | `specialist.md`, `general.md`, `repository.md` | `review.file-safeguard-requests` | `false.md`: reviewers leave out requests for a safeguard against a situation nothing suggests will arise. `true.md`: they report them, written as requests |
| `{{exploration-depth}}` | `exploration.md` | `review.depth` | How wide the plan is. `light.md`: a concern is engaged by the change's own lines and the code that uses them directly, and divided only when one specialist could not get through it. `standard.md`: a concern is divided only when the change is too large for one reading. `thorough.md`: the change's effects are followed to what depends on them indirectly, and a concern is divided sooner |
| `{{specialist-depth}}` | `specialist.md` | `review.depth` | How far a specialist reads around each changed hunk. `light.md`: the code the hunk sits in, and further only for a reason it can name. `standard.md`: empty, so the specialist reads as far as its own judgement takes it. `thorough.md`: the callers and readers, the neighbouring code and the history of every hunk |
| `{{general-depth}}` | `general.md` | `review.depth` | How far a general reviewer reads around each changed hunk it was given, in the three files `specialist-depth/` has, written for a reader of every kind of problem |
| `{{verifier-safeguard-requests}}` | `verifier.md` | `review.file-safeguard-requests` | `false.md`: empty, so a verifier judges every finding as a claimed fault. `true.md`: what each verdict means for a request for a safeguard, that a confirmed one is filed as a suggestion and never sent to the author as a defect, and that a request whose situation does arise is refuted and its fault returned as the verifier's own finding |

A file is empty where its value adds nothing to the prompt, and it is still
there. A setting that changes two prompts in different ways has a slot and
a directory for each passage.

A passage holds the sentences that differ between a setting's values, and
the prompt around its slot holds what is true under all of them. Where a
value changes what a prompt already says, that sentence moves into the
passage for the value it was written for, as `exploration-depth/standard.md`
shows, so that no reader is given a rule and then its amendment. A passage
is read without its siblings: it says what its own value asks for and does
not compare itself with another value.

Depth sets how wide the review plans and how far its specialists or its
general reviewers read. It never bounds what a leg returns: no depth
passage caps or thins findings, and every changed hunk is considered at
every depth. A quick review has no plan for depth to widen: the reviewer
given the whole change divides it when one reader could not give every
hunk the reading its depth asks for, so a deeper reading divides a change
sooner without a passage of its own. The verifier
and the `.review/` brief prompts take no depth passage. A finding is checked
in the same way at every depth, and how much a brief's reviewer reads is set
by the brief's own extent and sweep. `workflows/prompt-settings.mjs` lists the
settings, their values and their slots.

## Slots

A prompt is its file with every `{{slot}}` filled.

File slots take the text of another file here: `{{reviewed-change}}`,
`{{what-makes-a-finding}}`, `{{writing-a-finding}}`, `{{severity-scale}}`,
`{{concern}}`, which takes
a concern file's body without its front matter, and the setting slots
above.

Run slots take a value from the run:

| Slot | Value |
|---|---|
| `{{target}}`, `{{head}}` | the commits bounding the change under review |
| `{{project-documents}}` | each guidance document in configured order, in a `<project-guidance>` block naming its path |
| `{{repository-description}}` | the description Minos keeps of the repository, in a `<repository-description>` block naming the default-branch commit it was made from, or nothing when the run was set up with none (`workflows/repository-description.mjs`) |
| `{{pull-request}}` | the title and description in a `<pull-request-description>` block, or nothing when the pull request has neither |
| `{{concern-name}}` | the concern's name |
| `{{other-concerns}}` | a list line for every other concern: its name and summary |
| `{{concerns}}` | the same line for every concern |
| `{{planned-focus}}` | the planned unit's `concern` sentence and its `scope` paths |
| `{{same-concern-units}}` | every other planned unit of the same concern, each in the form `{{planned-focus}}` takes, or nothing when the unit is its concern's only one |
| `{{brief-path}}`, `{{brief}}` | a `.review/` brief's path and text |
| `{{assignment}}` | in `repository.md`, the scope and extent a `.review/` brief's reviewer was given, built from the files in `assignment/`; in `general.md`, what a general reviewer was given, from the files in `general-assignment/` |
| `{{share}}` | a share of a divided change: its `focus` sentence and its `paths`, in the form `{{planned-focus}}` takes |
| `{{other-shares}}` | every other share of the change, each in the form `{{share}}` takes |
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
   findings alike. A general reviewer has no concern and returns `findings`
   alone, those of a reviewer that went on to divide the change included.
2. The merge leg groups all of a workflow's proposed findings at once, before
   they are batched for verification. It is skipped when there are fewer than
   two. Each group goes on as one finding: its primary write-up, with the
   group's other write-ups beside it.
3. A verifier leg gives each finding one verdict, which stands for every
   write-up in its group. A finding a verifier raises itself, in
   `newFindings`, is a proposed finding too: it is merged with whatever else
   its round raised and verified in the next round, by a leg other than the
   one that raised it. It names the finding its verifier was checking, and
   where that finding is a whole-scope brief's and the raised one lies under
   the brief's directory, the raised one is verified as whole-scope too. A
   workflow runs `VERIFICATION_ROUNDS` rounds (`review-contracts.mjs`), and
   what the last of them raises has no verdict, which leaves the run
   incomplete.

`workflows/review-stages.mjs` runs the merge and the verification for both
review workflows. `workflows/finding-disposition.mjs` says what each verdict
makes of its finding, and what becomes of one left unsettled.
