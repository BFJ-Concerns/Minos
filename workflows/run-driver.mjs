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
// The course ends in one of two ways. Its last step settles `outcome`,
// clean or changes-requested, once the forge holds that result. Or a step
// throws: HeadMoved ends the run superseded, and anything else ends it
// incomplete, naming the step and the error's message as the cause. A step
// therefore handles no failure it cannot put right; it throws and the run
// ends the one way a failed run ends.
//
// The closing duties then run for every ending, each
// `async (run, settled, ending)`. One that fails is reported and the rest
// still run. The process exits 0 when the head is spent and 75 when it
// stays eligible, which is the meaning run-body's own exit status carries.

import { execFile, spawn } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { JUDGEMENT_RUNNER, JUDGEMENT_WORKFLOW, answerFrom, judgementArgs, judgementRecord } from "./judgement-stage.mjs";
import { CHANGES_REQUESTED, CLEAN, HeadMoved, INCOMPLETE, SUPERSEDED, spendsHead } from "./run-endings.mjs";
import { claim } from "./run-steps/claim.mjs";
import { classify } from "./run-steps/classify.mjs";
import { conclude } from "./run-steps/conclude.mjs";
import { inspectPush } from "./run-steps/inspect-push.mjs";
import { publish } from "./run-steps/publish.mjs";
import { recordFailure } from "./run-steps/record-failure.mjs";
import { review } from "./run-steps/review.mjs";
import { reviewBriefs } from "./run-steps/review-briefs.mjs";
import { settleEarlierFindings } from "./run-steps/settle-earlier-findings.mjs";
import { withdraw } from "./run-steps/withdraw.mjs";
import { writeReport } from "./run-steps/write-report.mjs";

export const COURSE = [claim, inspectPush, review, reviewBriefs, classify, publish, settleEarlierFindings, conclude];
export const CLOSING = [withdraw, recordFailure, writeReport];

export const EXIT_HEAD_SPENT = 0;
export const EXIT_HEAD_ELIGIBLE = 75;

// Carries the run through its course and closing duties and returns how it
// ended.
export async function driveRun(run, { course = COURSE, closing = CLOSING, report = reportToStderr } = {}) {
  let settled = {};
  let ending;
  let step;
  try {
    for (step of course) settled = { ...settled, ...(await step(run, settled)) };
    ending = settled.outcome === CLEAN || settled.outcome === CHANGES_REQUESTED
      ? { outcome: settled.outcome }
      : { outcome: INCOMPLETE, step: step ? step.name : "course", cause: "the course ended without settling an outcome" };
  } catch (error) {
    ending = error instanceof HeadMoved
      ? { outcome: SUPERSEDED, step: step.name, cause: error.message }
      : { outcome: INCOMPLETE, step: step.name, cause: error.message };
  }
  for (const duty of closing) {
    try {
      await duty(run, settled, ending);
    } catch (error) {
      report(`closing duty ${duty.name} failed: ${error.message}`);
    }
  }
  return ending;
}

function reportToStderr(message) {
  process.stderr.write(`run-driver: ${message}\n`);
}

// Runs `minos forge ARGS` and returns the JSON it printed. A command the
// forge did not carry out throws; where it still printed its result, as a
// refused write does, the error carries that as `result`.
function forge(run, args) {
  return new Promise((settle, reject) => {
    execFile(run.env.MINOS_BIN, ["forge", ...args], { cwd: run.workspace, env: run.env, maxBuffer: Infinity }, (error, stdout, stderr) => {
      let result;
      try {
        result = JSON.parse(stdout);
      } catch {
        result = undefined;
      }
      if (!error && result !== undefined) return settle(result);
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
    child.once("close", (code) => {
      const result = join(run.runDir, `${name}-result.json`);
      if (code !== 0 || !existsSync(result))
        return reject(new Error(`stage ${name} ended without a result; ${name}.log has its diagnostics`));
      try {
        settle(JSON.parse(readFileSync(result, "utf8")));
      } catch {
        reject(new Error(`stage ${name} left a result that is not JSON`));
      }
    });
  });
}

// Puts one input to a judgement stage and returns its answer, keeping the
// record of the judgement in the run directory. A stage is judged once in
// a run, under its own name.
export async function judge(run, stage, input) {
  const args = judgementArgs(stage, input, { env: run.env });
  writeFileSync(join(run.runDir, `${stage.name}-args.json`), `${JSON.stringify(args)}\n`);
  const answer = answerFrom(stage, input, await run.dispatch(stage.name, JUDGEMENT_WORKFLOW, JUDGEMENT_RUNNER));
  const records = join(run.runDir, "judgements");
  mkdirSync(records, { recursive: true });
  writeFileSync(join(records, `${stage.name}.json`), `${JSON.stringify(judgementRecord(stage, input, answer, args.routing), null, 2)}\n`);
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
    forge: (...args) => forge(run, args),
    dispatch: (name, workflow, runner) => dispatch(run, name, workflow, runner),
    judge: (stage, input) => judge(run, stage, input),
  };
  return run;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const ending = await driveRun(openRun());
  process.stdout.write(`${JSON.stringify(ending)}\n`);
  process.exitCode = spendsHead(ending) ? EXIT_HEAD_SPENT : EXIT_HEAD_ELIGIBLE;
}
