// Publishes what the decisions call for: composes the publication plan,
// posts the review holding the gating findings, and files the plan's
// triage entries at the configured filing destination. A gating finding
// that reached no durable surface throws; a filing that failed does not,
// and is carried for the run report.
//
// Takes `review`, `briefReview` and `decisions`. Does nothing when no
// review ran.
// Settles `publication`: the plan as posted, with the filing's result.
export async function publish(run, settled) {
  throw new Error("the publish step is not built");
}
