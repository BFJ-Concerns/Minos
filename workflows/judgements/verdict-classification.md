# Verdict classification

A review of this pull request has finished. Reviewers read the change and proposed findings, and verifiers checked each one against the code. The findings below are the ones that were confirmed. You decide what becomes of each: whether it blocks the pull request, is filed as advice on a defect, or takes a class that makes it neither. That settles the verdict, which is `request-changes` when any finding blocks and `clean` when none does.

Your answer is acted on as you give it, by a script that makes no judgement of its own. A finding you mark as gating is posted on the pull request in a review that requests changes, and the author is expected to deal with it before merging. A finding that neither gates nor takes a class is advisory: it is filed where the project keeps such advice and is never posted on the pull request, which carries only what blocks it. A finding you give a class neither blocks the pull request nor is filed as that advice. What else becomes of it follows from its class, as the classes below say, and this run's record keeps it whichever it takes.

{{reviewed-change}}

## The confirmed findings

<confirmed-findings>
{{findings}}
</confirmed-findings>

`main` holds the findings of the review itself. `brief` holds those raised under the repository's own `.review/` briefs, and is present only when that part of the review ran. You return one decision for each group present, including a group that holds no findings.

Each finding carries:

- `key`: the name your decision refers to it by.
- `severity`: the verifier's grade of it.
- `atOrAboveThreshold`: whether that grade reaches this repository's threshold, which is `{{threshold}}`.
- `path`, `line`, `title` and `explanation`: the finding as its reviewer wrote it.
- `verification`: the verifier's reason for confirming it.
- `source`: the review concern, or the brief, that raised it.

The facts of these findings are settled. A verifier followed each one through the code and confirmed it, and you are not asked to check that again or to look for further problems in the change. Read the code where a decision below turns on it.

## The default

The threshold gives each finding its default: one at or above it gates, and one below it is advisory. You depart from a default only on a ground this prompt names. There are two kinds. A class sets a finding aside, so that it neither gates nor is filed as advice, and there are three classes. An undergrade makes a finding below the threshold gate. A finding that has neither keeps its default, however you weigh it otherwise.

## The classes

Two classes object to the kind of finding. Severity does not catch either, because a verifier confirms that what a finding says is true, and these findings are true: the objection is to what they ask for.

- `declared-out-of-scope`: a gap the pull request's description explicitly declares as known or out of scope, where the project's documents do not contradict the declaration. The author has said they know of it, so no surface tells them again.
- `speculative-hardening`: a request for a new safeguard (a guard, a check, a test or a script) against an input, an environment or a later edit that nothing in the repository or the project's documents shows the project to have, where the change's own logic does nothing wrong. Such a request is never filed among the defects, because an entry there is read later as work to do, and work done from these is how a project fills with guards it never needed. What happens to it instead is the repository's choice. By default it is filed nowhere. A repository that has asked for its safeguard requests has each filed apart from the defects, marked as a suggestion. You are not shown which, because the class is the same under either: a finding takes it for what it asks for, and never for where it would then go. A test that cannot fail, behaviour the change adds and leaves unproved, and a document the code contradicts are real gaps, not hardening.

The third objects to saying a thing again.

- `already-raised`: the finding restates one the author already has, and the code it cites has not changed since. The next section says what the author already has.

A finding that fits a class takes it whatever its severity: one below the threshold is classed, not left advisory.

A verifier's reason may say that the delivered behaviour is correct and the finding describes a gap. That bears on how much the finding matters. It is never grounds for a class.

Whether a finding is `speculative-hardening` turns on what the repository shows. A finding may be written as a request for a safeguard while its `verification` cites the place where the situation it guards against does arise. The verifier then found a fault, and the finding does not take the class.

## What the author already has

These are the findings Minos posted on this pull request before this run, from every earlier review of it. An empty list means there are none.

<earlier-findings>
{{earlier-findings}}
</earlier-findings>

Each carries its `path` and `line` as they were when it was posted, the `head` commit it was raised on, and in `finding` its text as the author read it.

A finding of this run is `already-raised` when it describes the same defect as one of these, in the code that earlier finding cites, and that code has not changed since the earlier finding's `head`. Lines move between heads, so match the code and not the line number. Where the clone still has the earlier commit, `git diff EARLIER_HEAD {{head}} -- PATH` shows what changed. Where it does not, compare what the earlier finding says of the code with the code as it stands. Code the author has changed since is fair game: a defect in what they wrote after being told is a new finding, and is said.

Whether an earlier finding still blocks this pull request is not your question. It is settled separately, whatever you decide here, so a finding you class `already-raised` is not thereby forgiven.

This is what the project's filing destination already holds, where this run could read it:

<filed-entries>
{{filed-entries}}
</filed-entries>

A finding below the threshold that restates an entry there takes `already-raised` too, since the advice is already on file. A finding at or above the threshold never takes the class on that ground: a filed entry is not something the author has been shown on this pull request. When the block says the entries could not be read, classify without them.

## A finding graded too low

A finding below the threshold gates only as an undergrade, with the reason in its `undergrade`. There are two reasons. One is that it restates a defect that gates, which the next section covers. The other is that its severity was graded too low. The verifier graded each finding on this scale:

{{severity-scale}}

Call a grade too low only when, having read the finding and the code it cites, you would place it at or above the threshold on that scale and can say why. This is the one direction a grade moves. A finding at or above the threshold never becomes advisory because you think it graded too high.

## One defect, one call

Reviewers work apart, so two findings sometimes describe one defect: the same mistake restated at another line, or at one line under another title, within a group or across `main` and `brief`. Give every finding of one defect the same `defect` label, a short slug you choose, spelled identically wherever you use it. The author is then told of the defect once, at its most severe site, with its other sites named. Distinct defects never share a label, even at one line, and a finding that restates no other needs none.

Findings under one label take one call:

- They gate alike. Where one gates, they all do, so a finding below the threshold that restates a gating defect gates too, and its `undergrade` names the finding it restates.
- Left non-gating, they all carry the same class or all carry none. A class is the reason a finding does not gate, so where one finding of a defect gates, none of the others keeps a class.

`already-raised` is the one class that stands outside that call, because it is a fact about a site and not about the defect. An earlier review may have raised a defect at one place, where the code has not changed, and this run finds the same mistake there and at a line the author has not been told of. Give all of those findings the one label. Class the finding at the earlier site `already-raised`: it is not posted again, and the label's call passes it by. The findings at the new sites take their own call, gating or not, and share it between them as a label's findings do, so the author hears of the defect where it is new.

Whether the author already has a defect at a site is one fact, so findings of one defect at one site take one answer on it. Two findings at one path and line under one title, and a label's findings at one path and line whatever their titles, all take `already-raised` or none of them does. Called both ways, the decision would count the defect as said and post it again at the same line.

All of this holds across the two groups as it does within one. A `brief` finding at the same path and line and under the same title as a `main` finding is treated as that same finding whether or not you label the pair, so give both the same call: the same gating, the same class, and the same label where either has one.

A script checks these rules before anything is posted, and refuses the whole answer when one is broken: the run then ends with no review published. So settle each defect's call once, with both groups in front of you, and write it into every finding of that defect.

## What to return

A decision for `main`, and one for `brief` when that group is present. Each decision has:

- `verdict`: `request-changes` when any finding of the group gates, and `clean` when none does. A group with no findings is `clean`.
- `basis`: one sentence stating the concrete grounds for this call. It goes into the run's record, where it is read by someone who has not seen the change.
- `findings`: one entry for every finding of the group, and no others. Each entry has:
  - `key`: the finding's key.
  - `gating`: whether it blocks the pull request.
  - `class`: `declared-out-of-scope`, `speculative-hardening` or `already-raised`, or an empty string when the finding takes none. A gating finding carries no class. A finding at or above the threshold that does not gate must carry one.
  - `defect`: the label it shares with the other findings of the same defect, or an empty string.
  - `undergrade`: why a finding below the threshold gates, or an empty string. A finding below the threshold that gates must carry one.
