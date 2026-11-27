#!/usr/bin/env node

// The input of the repository-description stage, for the unit that writes
// a repository's description (scripts/run-body/describe-body). It reads
// the commit the clone is checked out at and the description kept until
// now, and writes the stage's arguments where dispatch-stage reads them:
// <run dir>/repository-description-args.json, with the bare input beside
// them in repository-description-input.json as a run keeps a judgement's.
//
//   MINOS_DESCRIPTION_COMMIT   the default-branch commit to describe
//   MINOS_DESCRIPTION_RECORD   the record the service keeps; absent means
//                              none has been written yet
//   MINOS_RUN_DIR              where the arguments are written
//
// A kept record that cannot be read is not given to the stage as the
// earlier description: the stage writes one afresh, and the service's
// record is replaced with it.

import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { judgementArgs, loadStage } from "./judgement-stage.mjs";
import { descriptionFromRecord } from "./repository-description.mjs";

export const STAGE = "repository-description";

// The stage's input: the commit, and the kept description or null.
export function descriptionInput(commit, recordPath, { warn = (message) => process.stderr.write(`${message}\n`) } = {}) {
  let earlier = null;
  try {
    earlier = descriptionFromRecord(recordPath) ?? null;
  } catch (error) {
    warn(`repository-description-input: the kept description is not used: ${error.message}`);
  }
  return { commit, earlier };
}

export async function writeDescriptionArgs(env = process.env) {
  for (const name of ["MINOS_DESCRIPTION_COMMIT", "MINOS_DESCRIPTION_RECORD", "MINOS_RUN_DIR"]) {
    if (typeof env[name] !== "string" || env[name] === "") throw new Error(`${name} is required`);
  }
  const stage = await loadStage(STAGE);
  const input = descriptionInput(env.MINOS_DESCRIPTION_COMMIT, env.MINOS_DESCRIPTION_RECORD);
  const args = judgementArgs(stage, input, { env });
  writeFileSync(join(env.MINOS_RUN_DIR, `${STAGE}-args.json`), `${JSON.stringify(args)}\n`);
  writeFileSync(join(env.MINOS_RUN_DIR, `${STAGE}-input.json`), `${JSON.stringify(input)}\n`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    await writeDescriptionArgs();
  } catch (error) {
    process.stderr.write(`repository-description-input: ${error.message}\n`);
    process.exitCode = 1;
  }
}
