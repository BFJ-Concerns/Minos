// A closing duty. Appends one line to the failure ledger for every ending
// but a clean one, saying why: the pull request and head, the step the
// ending names and its cause.
export async function recordFailure(run, settled, ending) {
  throw new Error("the record failure duty is not built");
}
