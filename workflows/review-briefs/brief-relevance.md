# Brief relevance

A pull request is under review. Its repository keeps review concerns of its own as briefs in a `.review/` directory, and some briefs say in a sentence when they apply: a relevance condition, written by the repository's owners. The briefs listed at the end each carry one. Your job is to decide, for each, whether this change meets its condition, which is to say whether the change gives that brief's concern anything to judge.

A brief you call applicable is given a reviewer who reads for its concern and nothing else. A brief you call inapplicable is not run on this change, and your reason is kept as the record of why it was skipped.

{{reviewed-change}}

## The judgement

Take each brief on its own, and read its condition for what its owners meant to catch, not for its narrowest wording.

The changed paths are listed below. Where they settle a condition, they are enough. Where they do not, read the diff: a condition is more often about what a change does than about where it is.

The two mistakes you can make do not cost the same. A brief wrongly called applicable costs one reviewer, who can still report that the concern had nothing to read. A brief wrongly called inapplicable means nobody looks for its concern in this change, and nothing later puts that right. So call a brief inapplicable only when the change clearly gives its concern nothing to judge, and call it applicable whenever you are in doubt.

You are deciding whether each concern has work, not doing the work: do not report problems in the change.

## What to return

`decisions`: one entry for every brief listed, and none for any other.

- `brief`: the brief's path, exactly as listed.
- `applicable`: whether the change meets the brief's condition.
- `reason`: a sentence or two. For an applicable brief, name what in the change meets the condition. For an inapplicable one, say what the change is and why that leaves the concern nothing to judge. The reason is read on its own by someone who has not seen the change, so write it about the change and the condition, in full.

## The changed paths

A JSON array of every path the change touches.

{{changed-paths}}

## The briefs

A JSON array with an entry for each brief: `brief` is its path and `relevance` its condition.

{{relevance-conditions}}
