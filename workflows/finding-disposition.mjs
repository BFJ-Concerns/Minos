// What a verifier's verdict makes of the finding it was given. The verifier
// judges; this table is the script's, and every consumer of a verdict reads
// the disposition from here rather than from the raw `verdict` field.
//
//   confirmed   the finding is a confirmed defect, carrying the verifier's
//               severity into classification and publication.
//   refuted     the finding reaches no surface. Nothing remembers it, so a
//               later reviewer or run may propose it again.
//   unsettled   reading the repository could not decide the finding. Its
//               verdict is complete, so it does not make a run incomplete.
//               It is never among the confirmed findings, is never
//               classified, never gates and is never rendered as a defect:
//               it is filed as unverified material is, as an open question
//               carrying `outsideFact` and `wouldSettle`.
//   no-verdict  the verifier gave nothing usable for the finding, which
//               makes the run incomplete.
//
// A verdict that names an outside fact is unsettled whatever it calls
// itself: a verifier sure of something the repository does not show has
// still not shown it. The rule is enforced here so that it does not rest
// on the verifier brief's wording alone.

import { SEVERITIES } from "./review-contracts.mjs";

export const CONFIRMED = "confirmed";
export const REFUTED = "refuted";
export const UNSETTLED = "unsettled";
export const NO_VERDICT = "no-verdict";

function usableVerdict(verdict) {
  return Boolean(
    verdict && typeof verdict === "object" &&
    [CONFIRMED, REFUTED, UNSETTLED].includes(verdict.verdict) &&
    Number.isInteger(verdict.confidence) && verdict.confidence >= 0 && verdict.confidence <= 100 &&
    typeof verdict.reason === "string" &&
    typeof verdict.outsideFact === "string" &&
    typeof verdict.wouldSettle === "string" &&
    // A finding left standing needs a grade to be weighed by.
    (verdict.verdict === REFUTED || SEVERITIES.includes(verdict.severity))
  );
}

export function findingDisposition(verdict) {
  if (!usableVerdict(verdict)) return NO_VERDICT;
  if (verdict.outsideFact.trim() !== "") return UNSETTLED;
  return verdict.verdict;
}
