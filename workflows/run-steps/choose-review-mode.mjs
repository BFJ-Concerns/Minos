// Settles which review this head gets, before the review is dispatched:
// the repository's `review.mode` setting turned into the mode the review
// step carries out (review-mode.mjs).
//
// A setting that names a mode is that mode, and reading it is mechanical.
// Which review a change deserves is a judgement, so this step never weighs
// the change itself: a setting that left the mode open would be settled
// here by asking a judgement stage (workflows/judgements/README.md) and
// carrying out its answer.
//
// Takes `inspection`. Does nothing when the head needs no review.
// Settles `reviewMode`: `mode`, the review this head gets.

import { modeSettingFromEnvironment } from "../review-mode.mjs";

export async function chooseReviewMode(run, { inspection }) {
  if (inspection.reviewBase === null) return;
  return { reviewMode: { mode: modeSettingFromEnvironment(run.env) } };
}
