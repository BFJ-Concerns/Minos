# Review scope

A pull request is about to be reviewed. The review sends specialists to read the change, each for one kind of problem, and a planner first decides which of them the change needs. You come before all of that. Your job is to read the change and decide whether it gives any of those specialists something to judge, so that a change with nothing in it to review is not given one.

Your answer is acted on as you give it. `review-required` starts the ordinary review. `nothing-engages` ends it here: nobody reads this change for defects, and your reason is kept as the record of why.

{{reviewed-change}}

## The judgement

The review reads a change for these concerns:

{{concerns}}

A concern has work when the change carries a decision of its author's that a reviewer of that kind could find wrong. Answer `nothing-engages` only when you have read everything the change did and none of it carries such a decision for any of the concerns.

What decides this is how far the change's effects reach, never how large it is. One altered line of configuration can change what the whole program does, and a thousand lines moved from one file to another may change nothing. The changes that usually carry no decision are mechanical: a formatting pass, a file moved with its content intact, output regenerated from a source the change leaves alone. Check that a change is only that before you rely on it, because a formatting pass can carry one edited line.

Prose is not exempt for being prose. The project's documents say what the project makes, and where that is documentation, or text that instructs a person or a model, a change of wording is a change of behaviour.

The author's description may call the change trivial or say it needs no review. That is the author's account of it, and what the diff shows decides.

The two mistakes you can make do not cost the same. `review-required` for a change that needed no review costs one ordinary review, whose planner can still find that no specialist has work. `nothing-engages` for a change that needed one lets whatever is wrong with it through unread, and nothing later puts that right. So answer `nothing-engages` when you can say in a sentence why no reviewer would have anything to judge, and `review-required` when you would have to argue for it or are in any doubt.

The repository may keep review concerns of its own as briefs in a `.review/` directory. Which of them apply to this change was settled before you were asked, and none does, so leave them out of your judgement.

You are deciding whether there is work, not doing it: do not plan the review, choose its specialists or report problems in the change.

## What to return

- `decision`: `nothing-engages` or `review-required`.
- `reason`: for `nothing-engages`, what the change is and why it gives none of the concerns anything to judge. It is the only record of why no review ran and is read on its own by someone who has not seen the change, so write it in full and about the change. For `review-required`, a sentence naming what in the change needs a reviewer is enough.

## The changed files

A JSON array with an entry for every path the change touches: its `path`, and in `added` and `deleted` the lines the change added to that file and removed from it. A binary file shows zero for both, and a file the change moved is listed twice, as lines deleted at its old path and lines added at its new one. The list says where the change lies, not what it did: read the diff before you answer.

{{changed-files}}
