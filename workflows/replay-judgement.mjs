#!/usr/bin/env node

// Puts a recorded input to its judgement stage again, outside any run:
//
//   node workflows/replay-judgement.mjs RECORD WORKSPACE
//
// RECORD is a judgement record (judgementRecord in judgement-stage.mjs): a
// run's own, from its `judgements` directory, or one written by hand for a
// head some other reader decided, with that reader's choice as its
// `answer`. WORKSPACE is a clone of the repository checked out at the head
// the input names, holding every commit the input names. The stage runs as
// it does in a run: the prompt as the tree now words it, the routing the
// environment resolves (MINOS_PROVISIONED_ENGINES, MINOS_ROUTING), the same
// runner and workflow. No forge is read or written.
//
// It prints one JSON line: the stage, the `recorded` answer (null when the
// record holds none), the `replayed` answer and the routing it was given
// on. Exit 1 means the stage gave no usable answer; exit 2 means the record
// could not be put to it.

import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { JUDGEMENT_RUNNER, JUDGEMENT_WORKFLOW, RECORD_KIND, answerFrom, judgementArgs, loadStage } from "./judgement-stage.mjs";

const workflowsDirectory = dirname(fileURLToPath(import.meta.url));
const [recordPath, workspace, ...extra] = process.argv.slice(2);

if (!recordPath || !workspace || extra.length > 0) {
  process.stderr.write("usage: node workflows/replay-judgement.mjs RECORD WORKSPACE\n");
  process.exitCode = 2;
} else {
  let stage;
  let record;
  let args;
  try {
    record = JSON.parse(readFileSync(recordPath, "utf8"));
    if (!record || record.kind !== RECORD_KIND) throw new Error(`a judgement record has kind ${RECORD_KIND}`);
    stage = await loadStage(record.stage);
    args = judgementArgs(stage, record.input);
  } catch (error) {
    process.stderr.write(`replay-judgement: ${error.message}\n`);
    process.exitCode = 2;
  }
  if (args) {
    const scratch = mkdtempSync(join(tmpdir(), "minos-replay-judgement-"));
    try {
      const argsFile = join(scratch, "args.json");
      writeFileSync(argsFile, `${JSON.stringify(args)}\n`);
      const run = spawnSync(
        join(workflowsDirectory, JUDGEMENT_RUNNER),
        [join(workflowsDirectory, JUDGEMENT_WORKFLOW), "--json-args", `@${argsFile}`],
        { cwd: resolve(workspace), encoding: "utf8", stdio: ["ignore", "pipe", "inherit"], maxBuffer: Infinity },
      );
      if (run.error) throw run.error;
      if (run.status !== 0) throw new Error(`stage ${stage.name} gave no answer`);
      const replayed = answerFrom(stage, record.input, JSON.parse(run.stdout));
      process.stdout.write(`${JSON.stringify({
        stage: stage.name,
        recorded: record.answer === undefined ? null : record.answer,
        replayed,
        routing: args.routing,
      })}\n`);
    } catch (error) {
      process.stderr.write(`replay-judgement: ${error.message}\n`);
      process.exitCode = 1;
    } finally {
      rmSync(scratch, { recursive: true, force: true });
    }
  }
}
