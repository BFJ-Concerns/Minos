// Classifies the confirmed findings: which gate, which take a never-gating
// class, which are advisory. That is a judgement, so this step gathers what
// the judgement stage is given (each verdict's digest, and what the author
// already has from Minos on this pull request), asks the stage, and holds
// its answer to the decision validation in verdict-classification.mjs.
//
// Takes `review` and `briefReview`. Does nothing when no review ran.
// Settles `decisions`: `{ main, brief }`, each a validated verdict
// decision, `brief` absent when the brief stage returned no verdict.
export async function classify(run, settled) {
  throw new Error("the classify step is not built");
}
