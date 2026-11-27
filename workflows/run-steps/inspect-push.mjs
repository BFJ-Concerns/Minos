// Decides how much review this head needs, before any review runs. The
// decision is a judgement, so this step gathers what the push-inspection
// stage is given, asks it, and turns its answer into the range the reviews
// read.
//
// Settles `inspection`: the stage's `outcome` and `reason`, and
// `reviewBase`, the commit a review of this head is read from: the earlier
// head for an increment, the target for a full review, null when the head
// needs no review.

import { readFileSync } from "node:fs";

import { briefEngagement } from "../brief-dispositions.mjs";
import { changedPaths } from "../changed-paths.mjs";
import { FULL, INCREMENT, NO_REVIEW, OUTCOMES, stage as pushInspection } from "../judgements/push-inspection.mjs";
import { guidanceFromOrientation } from "../orientation-guidance.mjs";
import { pullRequestFromOrientation } from "../pull-request-description.mjs";
import { descriptionFromOrientation } from "../repository-description.mjs";
import { reviewBriefsFromWorkspace } from "../review-brief-files.mjs";

// The outcomes the stage may choose among: those the repository's
// `push-inspection.outcomes` setting allows, narrowed by what is
// mechanical. A full review is always open, and the setting cannot close
// it. An increment needs an earlier head this one descends from. No review
// is closed when a `.review/` brief of the repository's engages what the
// author changed, because its owner asked for that review, unless nothing
// changed at all.
export function availableOutcomes(earlier, briefsEngage, allowed = OUTCOMES) {
  const known = earlier.outcome !== "none";
  const unchanged = known && Array.isArray(earlier.changedPaths) && earlier.changedPaths.length === 0;
  return [
    ...(unchanged || !briefsEngage ? [NO_REVIEW] : []),
    ...(known && earlier.ancestor && Array.isArray(earlier.changedPaths) ? [INCREMENT] : []),
    FULL,
  ].filter((outcome) => outcome === FULL || allowed.includes(outcome));
}

// The outcomes the repository allows, as the service exports them.
export function allowedOutcomes(env) {
  const text = env && env.MINOS_PUSH_INSPECTION_OUTCOMES;
  if (typeof text !== "string" || text === "") throw new Error("MINOS_PUSH_INSPECTION_OUTCOMES is required");
  let allowed;
  try {
    allowed = JSON.parse(text);
  } catch (error) {
    throw new Error(`MINOS_PUSH_INSPECTION_OUTCOMES is not valid JSON (${error.message})`);
  }
  if (!Array.isArray(allowed) || !allowed.includes(FULL) || allowed.some((outcome) => !OUTCOMES.includes(outcome)))
    throw new Error(`MINOS_PUSH_INSPECTION_OUTCOMES must list outcomes drawn from ${OUTCOMES.join(", ")}, full among them`);
  return allowed;
}

// The paths the author changed since the earlier head, or every path of
// the pull request where there is no earlier head to compare with.
function authorChangedPaths(run, earlier) {
  if (earlier.outcome !== "none" && earlier.changedPaths !== null) return earlier.changedPaths;
  return changedPaths(run.workspace, run.target, run.head);
}

export async function inspectPush(run) {
  const allowed = allowedOutcomes(run.env);
  const answered = await run.forge("earlier", run.head, run.target);
  const earlier = answered.outcome === "none"
    ? { outcome: "none" }
    : { head: answered.head, outcome: answered.outcome, ancestor: answered.ancestor, changedPaths: answered.changed_paths };
  const { briefs } = reviewBriefsFromWorkspace(run.workspace);
  const { briefsEngage } = briefEngagement(briefs, null, authorChangedPaths(run, earlier));
  const orientation = JSON.parse(readFileSync(run.orientation, "utf8"));
  const pullRequest = pullRequestFromOrientation(orientation);
  const repositoryDescription = descriptionFromOrientation(orientation);
  const input = {
    target: run.target,
    head: run.head,
    guidance: guidanceFromOrientation(orientation),
    ...(pullRequest === undefined ? {} : { pullRequest }),
    ...(repositoryDescription === undefined ? {} : { repositoryDescription }),
    earlier,
    outcomes: availableOutcomes(earlier, briefsEngage, allowed),
  };

  const { outcome, reason } = await run.judge(pushInspection, input);

  const reviewBase = { [NO_REVIEW]: null, [INCREMENT]: earlier.head, [FULL]: run.target }[outcome];
  return { inspection: { outcome, reason, reviewBase } };
}
