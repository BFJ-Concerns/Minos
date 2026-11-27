// Dispatches the main review of this head, in the mode the run settled:
// that mode's input builder and workflow (review-mode.mjs), under the one
// stage name whichever it is.
//
// Takes `inspection` and `reviewMode`. Does nothing when the head needs no
// review. Settles `review`, the adjudicated verdict, which names its own
// mode.

import { REVIEWS } from "../review-mode.mjs";
import { dispatchReview } from "./dispatch-review.mjs";

export async function review(run, { inspection, reviewMode }) {
  if (inspection.reviewBase === null) return;
  const { verdict, file, carried, dispatchProblem } = await dispatchReview(run, inspection, {
    name: "review", ...REVIEWS[reviewMode.mode], carry: true,
  });
  return { review: verdict, reviewFile: file, carriedReview: carried, reviewDispatchProblem: dispatchProblem };
}
