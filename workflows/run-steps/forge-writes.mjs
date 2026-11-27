import { writeFileSync } from "node:fs";
import { join } from "node:path";

import { HeadMoved } from "../run-endings.mjs";
import { composeSummary } from "../summary-comment.mjs";

export async function currentSnapshot(run) {
  const snapshot = await run.forge("snapshot");
  if (snapshot.head_sha !== run.head) throw new HeadMoved(snapshot.head_sha);
  return snapshot;
}

export async function applied(run, operation, args, accepted = ["applied"]) {
  const result = await run.forge(operation, ...args);
  if (!accepted.includes(result?.outcome)) {
    const failure = new Error(`forge ${operation}: ${result?.reason ?? result?.outcome ?? "no write outcome"}`);
    failure.result = result;
    throw failure;
  }
  return result;
}

export function movedHead(error) {
  return error instanceof HeadMoved || error.result?.reason === "head moved";
}

// Status is informational. A rejected status must still stop the course if
// it says the head moved.
export async function setStatus(run, state, head = run.head, target = run.target) {
  try {
    await applied(run, "status", [head, target, state]);
  } catch (error) {
    if (movedHead(error)) throw error;
    return error.message;
  }
}

// Makes the summary comment say `state` of this run's head, with the text
// composed from `facts` (summary-comment.mjs names what the parts read).
// The command binds the state to the head and edits the one comment in
// place. A write the forge did not confirm throws, a moved head as
// HeadMoved does. This is how a step writes a state that is the head's
// completion marker, clean or changes requested: the step fails when the
// marker is not on the forge.
export async function writeSummary(run, state, facts = {}) {
  const text = join(run.runDir, "summary-comment.md");
  writeFileSync(text, `${composeSummary({ ...facts, state, head: run.head, target: run.target })}\n`);
  return applied(run, "summary", [run.head, run.target, state, text]);
}

// The same write for a state that only shows where the run has got to. A
// comment that could not be composed or written is then a presentation
// defect: it is returned as the problem for the report and the course goes
// on, unless it says the head moved.
export async function showSummary(run, state, facts) {
  try {
    await writeSummary(run, state, facts);
  } catch (error) {
    if (movedHead(error)) throw error;
    return error.message;
  }
}
