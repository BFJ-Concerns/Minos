// A closing duty. Takes the run's marks off a head it did not judge: for an
// incomplete ending the incomplete status, and for that and a superseded
// one the removal of the in-flight marker. Does nothing for a head the run
// spent, whose marks the conclude step wrote.
export async function withdraw(run, settled, ending) {
  throw new Error("the withdraw duty is not built");
}
