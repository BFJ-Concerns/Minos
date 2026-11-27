// Claims the pull request for this run: the claim itself, which requests
// the service's review where the forge allows and sets the in-flight
// marker, and then the summary comment, which the first claim of a pull
// request creates and every later one edits to say this head is being
// inspected.
//
// Settles `snapshot`, the pull request as the claim found it, and
// `claimSummaryProblem` when the comment could not be written.

import { INSPECTING } from "../summary-comment.mjs";
import { applied, currentSnapshot, showSummary } from "./forge-writes.mjs";

export async function claim(run) {
  const snapshot = await currentSnapshot(run);
  await applied(run, "claim", []);
  const claimSummaryProblem = await showSummary(run, INSPECTING);
  return { snapshot, claimSummaryProblem };
}
