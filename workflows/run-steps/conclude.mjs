// Ends a run that judged its head. The outcome is changes-requested when a
// decision gates a finding or an earlier gating finding is outstanding, and
// clean otherwise: arithmetic over what the judgement stages answered. It
// then writes that result to the forge (the approval a clean head needs
// after an earlier request for changes, the status, the markers), and
// throws when the forge does not take a write the result depends on, so
// that no head is called clean over a block still standing.
//
// Takes `decisions` and `outstanding`.
// Settles `outcome`: CLEAN or CHANGES_REQUESTED (run-endings.mjs).
export async function conclude(run, settled) {
  throw new Error("the conclude step is not built");
}
