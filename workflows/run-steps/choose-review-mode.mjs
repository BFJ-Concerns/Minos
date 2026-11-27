// Settles which review this head gets, before the review is dispatched:
// the repository's `review.mode` setting turned into the mode the review
// step carries out (review-mode.mjs).
//
// A setting that names a mode is that mode, and reading it is mechanical.
// Which review a change deserves is a judgement, so under `automatic` this
// step asks the review-mode stage (judgements/review-mode.mjs) and carries
// out its answer. It does not ask where the service carried the
// predecessor's main verdict for this head and base: the review step
// reuses that verdict, so the head has its mode already, the one the
// verdict names.
//
// Where the stage gives no usable answer, the head gets the specialist
// review, the one a repository gets unless set, and the run goes on with
// the failure recorded. This is the one judgement whose failure does not
// end the run: neither answer is wrong for any change, the specialist
// review is the one that misses less, and so taking it decides nothing
// about the change that the stage was asked to decide.
//
// Takes `inspection`. Does nothing when the head needs no review.
// Settles `reviewMode`: `mode`, the review this head gets; `setting`, the
// repository's value; and, under `automatic`, `chosen` (the stage's
// `reason`), `carried` (true where a carried verdict settled the mode) or
// `problem` (why the stage gave no usable answer).

import { readFileSync } from "node:fs";

import { stage as reviewModeStage } from "../judgements/review-mode.mjs";
import { guidanceFromOrientation } from "../orientation-guidance.mjs";
import { pullRequestFromOrientation } from "../pull-request-description.mjs";
import { descriptionFromOrientation } from "../repository-description.mjs";
import { AUTOMATIC, MODES, SPECIALIST, modeSettingFromEnvironment } from "../review-mode.mjs";
import { carriedReview } from "./dispatch-review.mjs";
import { movedHead } from "./forge-writes.mjs";

export async function chooseReviewMode(run, { inspection }) {
  if (inspection.reviewBase === null) return;
  const setting = modeSettingFromEnvironment(run.env);
  if (setting !== AUTOMATIC) return { reviewMode: { mode: setting, setting } };

  const carried = carriedReview(run, inspection.reviewBase);
  if (carried && MODES.includes(carried.mode)) return { reviewMode: { mode: carried.mode, setting, carried: true } };

  const orientation = JSON.parse(readFileSync(run.orientation, "utf8"));
  const pullRequest = pullRequestFromOrientation(orientation);
  const repositoryDescription = descriptionFromOrientation(orientation);
  const input = {
    target: inspection.reviewBase,
    head: run.head,
    guidance: guidanceFromOrientation(orientation),
    ...(pullRequest === undefined ? {} : { pullRequest }),
    ...(repositoryDescription === undefined ? {} : { repositoryDescription }),
  };
  try {
    const { mode, reason } = await run.judge(reviewModeStage, input);
    return { reviewMode: { mode, setting, chosen: reason } };
  } catch (error) {
    if (movedHead(error)) throw error;
    return { reviewMode: { mode: SPECIALIST, setting, problem: error.message } };
  }
}
