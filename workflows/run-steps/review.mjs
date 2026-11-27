import { dispatchReview } from "./dispatch-review.mjs";

export async function review(run, { inspection }) {
  if (inspection.reviewBase === null) return;
  const { verdict, file, carried, dispatchProblem } = await dispatchReview(run, inspection, {
    name: "review", builder: "review-inputs.mjs", workflow: "review.js", carry: true,
  });
  return { review: verdict, reviewFile: file, carriedReview: carried, reviewDispatchProblem: dispatchProblem };
}
