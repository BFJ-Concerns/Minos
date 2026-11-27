// How a run ends, for the driver and every step. A run ends one of four
// ways, and an ending is `{ outcome }` with, where the run stopped short,
// the `step` that stopped it and the `cause`.

// The head was judged and nothing blocks it.
export const CLEAN = "clean";
// The head was judged and a gating finding stands, this run's or an
// earlier one's.
export const CHANGES_REQUESTED = "changes-requested";
// A step failed, so the head was not judged.
export const INCOMPLETE = "incomplete";
// The pull request's head moved, so the head the run was started for is
// no longer the thing to review.
export const SUPERSEDED = "superseded";

// A head that was judged is spent: it keeps its completion marker until it
// moves. Any other ending leaves the head eligible for a later run.
export function spendsHead(ending) {
  return ending.outcome === CLEAN || ending.outcome === CHANGES_REQUESTED;
}

// A step throws this when the forge shows a head other than the one the
// run was started for. The driver ends the run as superseded and not as a
// failure of the step.
export class HeadMoved extends Error {
  constructor(observedHead) {
    super(`the pull request's head moved to ${observedHead}`);
    this.name = "HeadMoved";
    this.observedHead = observedHead;
  }
}
