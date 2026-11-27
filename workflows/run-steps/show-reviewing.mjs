import { REVIEWING } from "../summary-comment.mjs";
import { showSummary } from "./forge-writes.mjs";

export async function showReviewing(run, { inspection }) {
  if (inspection.reviewBase === null) return;
  return { reviewingSummaryProblem: await showSummary(run, REVIEWING) };
}
