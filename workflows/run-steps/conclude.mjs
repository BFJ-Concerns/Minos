import { CHANGES_REQUESTED, CLEAN } from "../run-endings.mjs";
import { applied, movedHead, setStatus, writeSummary } from "./forge-writes.mjs";
import { summaryFacts } from "./summary-facts.mjs";

export async function conclude(run, settled) {
  const { publication, outstanding } = settled;
  if (!Array.isArray(outstanding)) throw new Error("earlier findings have no settlement result");
  const currentBlock = publication?.plan.verdict === "request-changes";
  const blocked = currentBlock || outstanding.length > 0;
  if (!blocked) await applied(run, "approve", [run.head, run.target], ["applied", "not-needed"]);
  const outcome = blocked ? CHANGES_REQUESTED : CLEAN;
  await writeSummary(run, outcome, summaryFacts(settled));

  const status = blocked ? "attention" : "clean";
  const terminalStatusProblem = await setStatus(run, status);
  // The summary records the verdict. The reaction or label is an
  // additional signal and can fail independently.
  let terminalMarkerProblem;
  try {
    await applied(run, "marker", [run.head, run.target, status, "add"]);
  } catch (error) {
    if (movedHead(error)) throw error;
    terminalMarkerProblem = error.message;
  }
  const cause = currentBlock
    ? "this run published blocking findings"
    : "blocking findings from an earlier review remain outstanding";
  return {
    outcome,
    ...(blocked ? { terminalCause: cause } : {}),
    terminalStatusProblem, terminalMarkerProblem,
  };
}
