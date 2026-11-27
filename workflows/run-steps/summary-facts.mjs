// Only completed section results and settled earlier findings can appear
// in a summary. A carried verdict from before section counts has no rows.
export function summaryFacts({ review, briefReview, outstanding }) {
  return {
    sections: [...(review?.summarySections ?? []), ...(briefReview?.summarySections ?? [])],
    outstanding,
  };
}
