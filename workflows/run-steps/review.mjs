// Runs the main review over the range push inspection chose: builds the
// review's input and dispatches review.js. A verdict that is not complete
// throws, since nothing may be published from one.
//
// Takes `inspection`. Does nothing when its `reviewBase` is null, which is
// a head that needs no review.
// Settles `review`: the complete adjudicated verdict.
export async function review(run, settled) {
  throw new Error("the review step is not built");
}
