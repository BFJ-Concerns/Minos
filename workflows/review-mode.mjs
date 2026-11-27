// Which review a head gets. The main review of a pull request's change is
// made in one of two modes, each a workflow of its own over the stages
// every review shares (review-stages.mjs):
//
//   specialist  exploration plans the review and a specialist proposes for
//               each planned unit of a concern (review.js)
//   quick       general reviewers propose, with no plan and no concerns:
//               one given the whole change, or one for each share where
//               that reviewer judged the change too large for one reader
//               (quick-review.js)
//
// Both end in the same merge and verification, return the same envelope
// and are adjudicated by the same wrapper, so classification, publication
// and filing read a quick review's verdict as they read any other. Each
// names itself in its envelope's `mode`, which the adjudicated verdict
// carries: the verdict is what a continuation hands on, so a review says
// which it was wherever its verdict is read. The mode is the main review's
// alone. The brief stage (review-briefs.js) reviews a repository's own
// `.review/` briefs after it under either mode, unchanged.
//
// A repository's `review.mode` setting says how a run comes by its mode,
// and the service exports it to the run as MINOS_REVIEW_MODE. Only the
// run reads the setting, to settle the mode before it starts the review:
// the scripted driver in its choose-review-mode step
// (run-steps/choose-review-mode.mjs), the resident lead in step 3 of
// lifecycle/lifecycle.md. An input builder and a workflow belong to one
// mode each and are never told the setting.

export const SPECIALIST = "specialist";
export const QUICK = "quick";

// Each mode's review: its input builder and its workflow, as names under
// the workflows directory. Whichever the mode, the review is dispatched as
// the stage `review`, so its input, result and timings sit under the one
// name and a continuation carries its verdict in the same way.
export const REVIEWS = {
  [SPECIALIST]: { builder: "review-inputs.mjs", workflow: "review.js" },
  [QUICK]: { builder: "quick-review-inputs.mjs", workflow: "quick-review.js" },
};

export const MODES = Object.keys(REVIEWS);

// The role a quick review's reviewers are recorded under, on their legs
// and in the envelope's `reviewers`, and the `reportedBy` their findings
// carry to the merge leg.
export const GENERAL_REVIEWER = "general";

export const MODE_VARIABLE = "MINOS_REVIEW_MODE";

// The values `review.mode` takes. Each names the mode every head of the
// repository is reviewed in. The Go loader holds the configured value to
// the same list.
export const MODE_SETTINGS = [SPECIALIST, QUICK];

// The repository's setting, as the service exported it. Unset or empty is
// the specialist review, the documented default, as when a run is started
// by hand; anything else outside the list is refused, never read as the
// default.
export function modeSettingFromEnvironment(env = process.env) {
  const value = env[MODE_VARIABLE] === undefined || env[MODE_VARIABLE] === "" ? SPECIALIST : env[MODE_VARIABLE];
  if (!MODE_SETTINGS.includes(value)) throw new Error(`${MODE_VARIABLE} must be one of ${MODE_SETTINGS.join(", ")}`);
  return value;
}
