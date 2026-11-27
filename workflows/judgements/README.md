# Judgement stages

A run's driver is a script and judges nothing. Wherever a run's course or
its published result turns on reading the change, a finding or an author's
reply, the driver asks a judgement stage: one agent, asked once, whose
answer the driver carries out as given.

| Stage | Decides | Driver step |
|---|---|---|
| `push-inspection` | how much review a head needs: none, the increment since the head Minos last finished on, or the whole pull request | `run-steps/inspect-push.mjs` |
| `verdict-classification` | which confirmed findings of the main and brief verdicts gate, take a never-gating class or are advisory | `run-steps/classify.mjs` |
| `earlier-findings` | whether each blocking finding of an earlier Minos review still stands: answered by the author, gone from the code, or not | `run-steps/settle-earlier-findings.mjs` |

## A stage's files

A stage is two files here, both named for it.

`NAME.md` is the prompt. It may include the sections the review prompts
share (`{{reviewed-change}}`, `{{writing-a-finding}}`, `{{severity-scale}}`,
from `workflows/review-briefs/`); every other `{{slot}}` is a run slot the
definition fills. File slots are filled first and run slots after, in one
pass, so a value is never itself read for slots.

`NAME.mjs` exports the definition as `stage`:

| Part | What it is |
|---|---|
| `name` | the stage's name: lower-case words joined by hyphens, the same as the files |
| `slots` | the run slots the prompt carries; a prompt with any other slot is refused before dispatch |
| `inputProblem(input)` | what is wrong with an input, or `null` |
| `promptValues(input)` | the text of each run slot |
| `answerSchema(input)` | the JSON schema of the answer, which may narrow with the input |
| `answerProblem(answer, input)` | what is wrong with an answer that the schema cannot say, or `null` |

A definition reads no forge and no clock, and nothing of the run but its
input. The input is a plain JSON value holding everything the stage is
told, and the stage's agent works in the clone, where it reads the code
for itself. The definition's comment on `inputProblem` gives the input's
fields.

## How a stage runs

The driver step gathers the input by mechanical means and calls
`run.judge(stage, input)` (`workflows/run-driver.mjs`). That builds the
workflow's arguments (`judgementArgs` in `workflows/judgement-stage.mjs`),
writes them to `NAME-args.json` in the run directory and dispatches
`judgement.js` through `dispatch-stage` with the runner `run-judgement`.
Every stage goes through that one workflow. A stage is therefore a stage
like the reviews are: its span is in the run's timings, its diagnostics are
in `NAME.log`, and the status page shows it.

The routing is the `judgement` role's (`workflows/role-routing.mjs`), one
role for every stage.

A stage that gives no answer, or one its `answerProblem` refuses, fails
the step that asked, and the run ends incomplete. No default answer stands
in: a default would be the driver deciding.

The step then carries the answer out. What an answer makes the run do is
the step's code and never the stage's.

## The record, and running a stage alone

`run.judge` keeps a record of each judgement in the run directory, as
`judgements/NAME.json`:

```json
{
  "kind": "minos-judgement-v1",
  "stage": "NAME",
  "input": {},
  "answer": {},
  "routing": { "engine": "", "model": "", "effort": "" }
}
```

The run's archive carries the directory. A record's `input` is all a stage
needs to answer again, so a stage is run alone on a recorded head with:

```sh
node workflows/replay-judgement.mjs RECORD WORKSPACE
```

`WORKSPACE` is a clone of the repository checked out at the head the input
names. The command builds the prompt from this tree as it stands, runs the
same runner and workflow on the routing the environment resolves, and
prints the recorded answer beside the replayed one. It reads and writes no
forge. A record written by hand, holding the choice some other reader made
on a head as its `answer`, is replayed the same way; that is how a stage's
choices are compared with choices made before it existed.

## Adding a stage

Write `NAME.md` and `NAME.mjs` here, the step that gathers its input and
carries out its answer under `workflows/run-steps/`, and a row in the
table above.
