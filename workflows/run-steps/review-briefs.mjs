// Runs the reviewed repository's `.review/` briefs over the same range as
// the main review: builds the stage's input and dispatches
// review-briefs.js. A verdict that is not complete throws.
//
// Takes `inspection`. Does nothing when its `reviewBase` is null.
// Settles `briefReview`: the complete adjudicated verdict of the brief
// stage.
export async function reviewBriefs(run, settled) {
  throw new Error("the review briefs step is not built");
}
