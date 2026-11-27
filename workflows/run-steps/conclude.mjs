import { CHANGES_REQUESTED, CLEAN } from "../run-endings.mjs";
import { applied, setStatus } from "./forge-writes.mjs";

export async function conclude(run, { publication, outstanding }) {
  if (!Array.isArray(outstanding)) throw new Error("earlier findings have no settlement result");
  const currentBlock = publication?.plan.verdict === "request-changes";
  const blocked = currentBlock || outstanding.length > 0;
  const approval = blocked ? null : await applied(run, "approve", [run.head, run.target], ["applied", "not-needed"]);
  const status = blocked ? "attention" : "clean";
  const terminalStatusProblem = await setStatus(run, status);
  // A current-head verdict can spend the head even when the informational
  // status fails. With neither durable write, the sweep still sees it as eligible.
  if (terminalStatusProblem && !currentBlock && approval?.outcome !== "applied") {
    throw new Error(terminalStatusProblem);
  }
  await applied(run, "marker", [run.head, run.target, status, "add"]);
  const cause = currentBlock
    ? "this run published blocking findings"
    : "blocking findings from an earlier review remain outstanding";
  return {
    outcome: blocked ? CHANGES_REQUESTED : CLEAN,
    ...(blocked ? { terminalCause: cause } : {}),
    terminalStatusProblem,
  };
}
