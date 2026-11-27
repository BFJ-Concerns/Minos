// Finds which gating findings of earlier Minos reviews on this pull request
// still stand. Whether the author answered a finding, and whether the
// defect it describes is gone from the code at this head, are judgements,
// so this step gathers the earlier gating findings with their threads and
// the author's comments, asks the judgement stage, and carries its answer.
// It runs on every head, reviewed or not.
//
// Settles `outstanding`: the earlier gating findings not settled, empty
// when none stands.
export async function settleEarlierFindings(run, settled) {
  throw new Error("the settle earlier findings step is not built");
}
