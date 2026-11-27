import { HeadMoved } from "../run-endings.mjs";

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
