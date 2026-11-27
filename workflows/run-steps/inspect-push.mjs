// Decides how much review this head needs, before any review runs. The
// decision is a judgement, so this step gathers what the push-inspection
// stage is given, asks it, and turns its answer into the range the reviews
// read.
//
// Settles `inspection`: the stage's `outcome` and `reason`, and
// `reviewBase`, the commit a review of this head is read from: the earlier
// head for an increment, the target for a full review, null when the head
// needs no review.

import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";

import { briefEngagement } from "../brief-dispositions.mjs";
import { FULL, INCREMENT, NO_REVIEW, stage as pushInspection } from "../judgements/push-inspection.mjs";
import { guidanceFromOrientation } from "../orientation-guidance.mjs";
import { pullRequestFromOrientation } from "../pull-request-description.mjs";
import { reviewBriefsFromWorkspace } from "../review-brief-files.mjs";

// The outcomes the stage may choose among, narrowed by what is mechanical.
// A full review is always open. An increment needs an earlier head this one
// descends from. No review is closed when a `.review/` brief of the
// repository's engages what the author changed, because its owner asked
// for that review, unless nothing changed at all.
export function availableOutcomes(earlier, briefsEngage) {
  const known = earlier.outcome !== "none";
  const unchanged = known && Array.isArray(earlier.changedPaths) && earlier.changedPaths.length === 0;
  return [
    ...(unchanged || !briefsEngage ? [NO_REVIEW] : []),
    ...(known && earlier.ancestor ? [INCREMENT] : []),
    FULL,
  ];
}

// The paths the author changed since the earlier head, or every path of
// the pull request where there is no earlier head to compare with.
function authorChangedPaths(run, earlier) {
  if (earlier.outcome !== "none" && earlier.changedPaths !== null) return earlier.changedPaths;
  return execFileSync("git", ["-C", run.workspace, "diff", "--no-renames", "--name-only", "-z", run.target, run.head], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    maxBuffer: Infinity,
  }).split("\0").filter(Boolean);
}

export async function inspectPush(run) {
  const answered = await run.forge("earlier", run.head, run.target);
  const earlier = answered.outcome === "none"
    ? { outcome: "none" }
    : { head: answered.head, outcome: answered.outcome, ancestor: answered.ancestor, changedPaths: answered.changed_paths };
  const { briefs } = reviewBriefsFromWorkspace(run.workspace);
  const { briefsEngage } = briefEngagement(briefs, null, authorChangedPaths(run, earlier));
  const orientation = JSON.parse(readFileSync(run.orientation, "utf8"));
  const pullRequest = pullRequestFromOrientation(orientation);
  const input = {
    target: run.target,
    head: run.head,
    guidance: guidanceFromOrientation(orientation),
    ...(pullRequest === undefined ? {} : { pullRequest }),
    earlier,
    outcomes: availableOutcomes(earlier, briefsEngage),
  };

  const { outcome, reason } = await run.judge(pushInspection, input);

  const reviewBase = { [NO_REVIEW]: null, [INCREMENT]: earlier.head, [FULL]: run.target }[outcome];
  return { inspection: { outcome, reason, reviewBase } };
}
