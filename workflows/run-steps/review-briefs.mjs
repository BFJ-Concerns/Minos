import { dispatchReview } from "./dispatch-review.mjs";

export async function reviewBriefs(run, { inspection }) {
  if (inspection.reviewBase === null) return;
  const { verdict, file, dispatchProblem } = await dispatchReview(run, inspection, {
    name: "review-brief", builder: "review-brief-inputs.mjs", workflow: "review-briefs.js",
  });
  return { briefReview: verdict, briefReviewFile: file, briefDispatchProblem: dispatchProblem };
}
