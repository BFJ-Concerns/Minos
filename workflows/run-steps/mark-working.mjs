import { setStatus } from "./forge-writes.mjs";

export async function markWorking(run) {
  const workingStatusProblem = await setStatus(run, "working");
  return { workingStatusProblem };
}
