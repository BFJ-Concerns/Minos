# Finding merge

The findings listed at the end were reported on one change before it merges, by one reviewer or by several. Some of them may describe the same fault. Reviewers who each read for a different kind of problem see one fault from different angles. A reviewer reading for one concern reports a problem outside it that the reviewer whose concern it is also reported. Reviewers who each answer for part of a large change follow a problem to the same code. And one reviewer sometimes writes a single fault up at two of the places it shows. Next, every finding is checked against the code by a verifier, and what survives is sent to the change's author. Your job is to group the findings so that each fault is checked once and reaches the author once.

The repository is the current directory, checked out at the head commit, `{{head}}`; the change is `git diff {{target}}...{{head}}`. Group by reading the findings, and open the code only where you cannot otherwise tell whether two of them share a cause. Do not build, run or edit anything. Do not judge whether a finding is true: that is the verifier's job, and a finding you think mistaken is grouped like any other.

## When two findings are one fault

Two findings are the same fault when a single fix would resolve both: they point at the same wrong code or the same omission, for the same reason, even if they describe different consequences, cite different lines of it or quote different documents.

They are different faults when the author would have to do two things. A function that mishandles a case and a test that fails to exercise that case are two faults, one fixed in the code and one in the tests. Two findings that merely concern the same function or file are also different faults.

When you cannot tell, keep them apart. Two write-ups of one fault cost a second check; two faults merged into one lose the second, because only the group's main write-up is checked and published.

## What a finding says of its reviewer

`reportedBy` is what the finding's reviewer was reading for, and so the angle its write-up was made from. A reviewer with a concern of its own is named by it: a specialist by the one kind of problem it reads for, and the reviewer of one of the project's own review briefs by the brief's title. `general` is a reviewer that reads for every kind of problem at once and has no concern of its own. `verification` marks a finding a verifier established while checking another.

`outsideConcern` is `true` where a reviewer with a concern of its own met a problem outside that concern and reported it all the same. It is `false` on every other finding, those of a reviewer with no concern of its own included.

## What to return

`groups`: every finding id appears in exactly one group, and a finding that matches no other is a group of one.

- `findingIds`: the ids of the findings in the group.
- `primary`: the id of the write-up that states the fault most completely and precisely. Between two that are equally good, pass over one whose `outsideConcern` is `true`: its reviewer was reading for something else and wrote it up from as much of the fault's path as it had already followed. Where that does not separate them, either will do. For a group of one, it is that id.
- `sharedCause`: for a group of two or more, one sentence saying what the single fix would be. An empty string for a group of one.

## The findings

{{findings}}
