// Claims the pull request for this run. It reads the forge's snapshot,
// throws HeadMoved when the head is no longer the one the run was started
// for, and makes the claim, which requests a review from the service
// identity and sets the in-flight marker and is safe to repeat.
//
// Settles `snapshot`: the pull request as the forge showed it at the
// claim.
export async function claim(run, settled) {
  throw new Error("the claim step is not built");
}
