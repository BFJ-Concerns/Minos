#!/usr/bin/env node

// The run driver: a script that takes one run from its claim to its end.
// It calls the run's steps in order and does nothing else. A step is
// mechanical work (a forge read or write, a workflow dispatched, a file
// composed), and where the run's course or its published result turns on
// reading the change, a finding or an author's reply, the step asks a
// judgement stage (workflows/judgements/README.md) and carries out its
// answer. Neither the driver nor a step decides such a thing.
//
// A step is `async (run, settled) => settled-more`, one module under
// workflows/run-steps exporting one function. `run` is what openRun
// returns: the run's coordinates and the three things a step does to the
// world outside it (forge, dispatch, judge). `settled` holds what earlier
// steps settled, under the names their modules document; a step returns
// the names it settles and never changes one it was handed. A step with
// nothing to do for this run (no review was called for, say) returns
// nothing and the course goes on, so the course is the same list for
// every run.
//
// The last step settles `outcome`,
// clean or changes-requested, once the forge holds that result. Or a step
// throws: HeadMoved ends the run superseded, and anything else ends it
// incomplete, naming the step and the error's message as the cause. A step
// therefore handles no failure it cannot put right; it throws and the run
// ends the one way a failed run ends. At a stage boundary memory pressure
// can hand the run to a successor.
//
// The closing duties then run for every ending, each
// `async (run, settled, ending)`. One that fails is reported and the rest
// still run. Exit 0 spends the head or wakes the continuation. Exit 75
// leaves the head eligible for a periodic sweep.

import { execFile, spawn } from "node:child_process";
import { appendFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { JUDGEMENT_RUNNER, JUDGEMENT_WORKFLOW, answerFrom, judgementArgs, judgementRecord } from "./judgement-stage.mjs";
import { CHANGES_REQUESTED, CLEAN, CONTINUATION, INCOMPLETE, SUPERSEDED, spendsHead } from "./run-endings.mjs";
import { prepareStage } from "./run-process.mjs";
import { claim } from "./run-steps/claim.mjs";
import { classify } from "./run-steps/classify.mjs";
import { conclude } from "./run-steps/conclude.mjs";
import { continueRun } from "./run-steps/continue-run.mjs";
import { movedHead } from "./run-steps/forge-writes.mjs";
import { inspectPush } from "./run-steps/inspect-push.mjs";
import { markWorking } from "./run-steps/mark-working.mjs";
import { publish } from "./run-steps/publish.mjs";
import { recordFailure } from "./run-steps/record-failure.mjs";
import { review } from "./run-steps/review.mjs";
import { reviewBriefs } from "./run-steps/review-briefs.mjs";
import { settleEarlierFindings } from "./run-steps/settle-earlier-findings.mjs";
import { withdraw } from "./run-steps/withdraw.mjs";
import { writeReport } from "./run-steps/write-report.mjs";

export const COURSE = [claim, markWorking, inspectPush, review, reviewBriefs, classify, settleEarlierFindings, publish, conclude];
export const CLOSING = [recordFailure, withdraw, writeReport];

const pressureBoundaries = new Map([
  [claim, "claim"], [inspectPush, "push-inspection"], [review, "review"],
  [reviewBriefs, "review-brief"], [classify, "classification"],
  [settleEarlierFindings, "earlier-findings"], [publish, "publication"],
]);

export const EXIT_HEAD_SPENT = 0;
export const EXIT_HEAD_ELIGIBLE = 75;

// Carries the run through its course and closing duties and returns how it
// ended.
export async function driveRun(run, { course = COURSE, closing = CLOSING, report = reportToStderr, complete = false } = {}) {
  let settled = {};
  let ending;
  let step;
  try {
    for (step of course) {
      const result = await step(run, settled);
      settled = { ...settled, ...result };
      const boundary = pressureBoundaries.get(step);
      // A blocking review spends this head for admission. This run still
      // owns the terminal status and marker writes after posting it.
      const blockingReviewPosted = settled.publication?.plan.verdict === "request-changes";
      if (run.runDir && boundary && result && !blockingReviewPosted && await continueRun(run, boundary)) {
        ending = { outcome: CONTINUATION, step: boundary, cause: "memory pressure" };
        break;
      }
    }
    ending ??= settled.outcome === CLEAN || settled.outcome === CHANGES_REQUESTED
      ? { outcome: settled.outcome }
      : { outcome: INCOMPLETE, step: step ? step.name : "course", cause: "the course ended without settling an outcome" };
  } catch (error) {
    ending = { outcome: movedHead(error) ? SUPERSEDED : INCOMPLETE, step: step.name, cause: error.message };
  }
  const closingProblems = [];
  let terminalWorkComplete = true;
  for (const duty of closing) {
    try {
      await duty(run, settled, ending, closingProblems);
    } catch (error) {
      const problem = `closing duty ${duty.name} failed: ${error.message}`;
      report(problem);
      closingProblems.push(problem);
      if (duty !== writeReport) terminalWorkComplete = false;
    }
  }
  if (complete && terminalWorkComplete) {
    const marker = ending.outcome === CLEAN || ending.outcome === CONTINUATION ? ending.outcome : "non-clean";
    writeFileSync(join(run.runDir, "lead-complete"), `${marker}\n`);
  }
  return ending;
}

function reportToStderr(message) {
  process.stderr.write(`run-driver: ${message}\n`);
}

// Runs `minos forge ARGS` and returns the JSON it printed. A command the
// forge did not carry out throws; where it still printed its result, as a
// refused write does, the error carries that as `result`. What a command
// that succeeded said on stderr (a review whose inline comments were folded
// into its body, say) is kept in forge.log and in `run.forgeNotes`, which
// the report lists; a forge.log that cannot be written costs only the log.
function forge(run, args) {
  return new Promise((settle, reject) => {
    execFile(run.env.MINOS_BIN, ["forge", ...args], { cwd: run.workspace, env: run.env, maxBuffer: Infinity }, (error, stdout, stderr) => {
      let result;
      try {
        result = JSON.parse(stdout);
      } catch {
        result = undefined;
      }
      if (!error && result !== undefined) {
        const said = stderr.trim();
        if (said) {
          const note = `minos forge ${args[0]}: ${said}`;
          run.forgeNotes.push(note);
          // The note is presentation: a log that cannot be written is
          // reported beside it and never undoes the write that succeeded.
          try {
            appendFileSync(join(run.runDir, "forge.log"), `${note}\n`);
          } catch (logError) {
            run.forgeNotes.push(`could not keep the note above in forge.log: ${logError.message}`);
          }
        }
        return settle(result);
      }
      const failure = new Error(`minos forge ${args[0]}: ${(stderr || (error && error.message) || "printed no JSON").trim()}`);
      failure.result = result;
      reject(failure);
    });
  });
}

// Dispatches one workflow stage through dispatch-stage, from the clone,
// waits for it and returns its result. The caller has written the stage's
// input to NAME-args.json. A stage that ended without a result throws.
function dispatch(run, name, workflow, runner) {
  const command = join(dirname(run.env.MINOS_SETUP_WORKSPACE), "dispatch-stage");
  const args = runner === undefined ? [name, workflow] : [name, workflow, runner];
  return new Promise((settle, reject) => {
    const child = spawn(command, args, { cwd: run.workspace, env: run.env, stdio: ["ignore", "inherit", "inherit"] });
    child.once("error", reject);
    child.once("close", (code, signal) => {
      const result = join(run.runDir, `${name}-result.json`);
      if (code !== 0 || !existsSync(result)) {
        const stopped = supervisorStop(run);
        const reason = stopped ? `stopped by the run supervisor: ${stopped}` : signal ? `signal ${signal}` : `exit ${code}`;
        const failure = new Error(`stage ${name} ended without a result; ${name}.log has its diagnostics (${reason})`);
        // A stage the supervisor stopped for silence ends the run; only an
        // interruption from elsewhere is worth the review's one retry.
        failure.killed = !stopped && (signal !== null || [130, 137, 143].includes(code));
        return reject(failure);
      }
      try {
        settle(JSON.parse(readFileSync(result, "utf8")));
      } catch {
        reject(new Error(`stage ${name} left a result that is not JSON`));
      }
    });
  });
}

// The reason run-body gave for stopping a silent stage, if it stopped one.
function supervisorStop(run) {
  try {
    return readFileSync(join(run.runDir, "stage-stopped"), "utf8").trim();
  } catch {
    return "";
  }
}

// Puts one input to a judgement stage and returns its answer, keeping the
// record of the judgement in the run directory. A stage is judged once in
// a run, under its own name.
export async function judge(run, stage, input) {
  const args = judgementArgs(stage, input, { env: run.env });
  const attempt = prepareStage(run, stage.name, `${JSON.stringify(args)}\n`);
  writeFileSync(join(run.runDir, `${attempt}-input.json`), `${JSON.stringify(input)}\n`);
  const answer = answerFrom(stage, input, await run.dispatch(attempt, JUDGEMENT_WORKFLOW, JUDGEMENT_RUNNER));
  const records = join(run.runDir, "judgements");
  mkdirSync(records, { recursive: true });
  writeFileSync(join(records, `${attempt}.json`), `${JSON.stringify(judgementRecord(stage, input, answer, args.routing), null, 2)}\n`);
  run.judgementAnswers?.set(stage.name, answer);
  return answer;
}

// The run as its steps see it, from the environment run-body exports.
export function openRun(env = process.env) {
  const need = (name) => {
    if (typeof env[name] !== "string" || env[name] === "") throw new Error(`${name} is required`);
    return env[name];
  };
  for (const name of ["MINOS_BIN", "MINOS_SETUP_WORKSPACE"]) need(name);
  const run = {
    env,
    pullRequest: { owner: need("MINOS_OWNER"), repository: need("MINOS_REPO_NAME"), number: need("MINOS_PR") },
    head: need("MINOS_HEAD_SHA"),
    target: need("MINOS_TARGET_SHA"),
    runDir: need("MINOS_RUN_DIR"),
    workspace: need("MINOS_WORKSPACE"),
    orientation: need("MINOS_ORIENTATION"),
    judgementAnswers: new Map(),
    forgeNotes: [],
    forge: (...args) => forge(run, args),
    dispatch: (name, workflow, runner) => dispatch(run, name, workflow, runner),
    judge: (stage, input) => judge(run, stage, input),
  };
  return run;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const ending = await driveRun(openRun(), { complete: true });
  process.stdout.write(`${JSON.stringify(ending)}\n`);
  process.exitCode = spendsHead(ending) || ending.outcome === CONTINUATION ? EXIT_HEAD_SPENT : EXIT_HEAD_ELIGIBLE;
}
