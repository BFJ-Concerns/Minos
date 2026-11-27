import { CONTINUATION, INCOMPLETE, SUPERSEDED } from "../run-endings.mjs";
import { applied, movedHead, setStatus } from "./forge-writes.mjs";

async function removeFromCurrentHead(run) {
  const snapshot = await run.forge("snapshot");
  await applied(run, "marker", [snapshot.head_sha, snapshot.target_sha, "in-flight", "remove"]);
}

export async function withdraw(run, settled, ending, problems) {
  if (ending.outcome === CONTINUATION) return;
  if (ending.outcome === SUPERSEDED) return removeFromCurrentHead(run);
  try {
    if (ending.outcome === INCOMPLETE) {
      const problem = await setStatus(run, "incomplete");
      if (problem) problems?.push(problem);
    }
    await applied(run, "marker", [run.head, run.target, "in-flight", "remove"]);
  } catch (error) {
    if (!movedHead(error)) throw error;
    await removeFromCurrentHead(run);
  }
}
