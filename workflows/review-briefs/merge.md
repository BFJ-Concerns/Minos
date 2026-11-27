# Finding merge

Several specialists read one change, each for a different kind of problem, and reported the findings listed at the end. Sometimes two of them describe one fault from different angles, and a specialist may have reported a problem outside its own concern that the specialist whose concern it is also reported. Next, every finding is checked against the code by a verifier, and what survives is sent to the change's author. Your job is to group the findings so that each fault is checked once and reaches the author once.

The repository is the current directory, checked out at the head commit, `{{head}}`; the change is `git diff {{target}}...{{head}}`. Group by reading the findings, and open the code only where you cannot otherwise tell whether two of them share a cause. Do not build, run or edit anything. Do not judge whether a finding is true: that is the verifier's job, and a finding you think mistaken is grouped like any other.

## When two findings are one fault

Two findings are the same fault when a single fix would resolve both: they point at the same wrong code or the same omission, for the same reason, even if they describe different consequences, cite different lines of it or quote different documents.

They are different faults when the author would have to do two things. A function that mishandles a case and a test that fails to exercise that case are two faults, one fixed in the code and one in the tests. Two findings that merely concern the same function or file are also different faults.

When you cannot tell, keep them apart. Two write-ups of one fault cost a second check; two faults merged into one lose the second, because only the group's main write-up is checked and published.

## What to return

`groups`: every finding id appears in exactly one group, and a finding that matches no other is a group of one.

- `findingIds`: the ids of the findings in the group.
- `primary`: the id of the write-up that states the fault most completely and precisely. Between two that are equally good, take the one whose specialist reported it within its own concern (`outsideConcern` is `false`). For a group of one, it is that id.
- `sharedCause`: for a group of two or more, one sentence saying what the single fix would be. An empty string for a group of one.

## The findings

{{findings}}
