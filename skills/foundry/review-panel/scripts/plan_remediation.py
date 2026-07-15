#!/usr/bin/env python3
"""Build the remediation round's plan from the run's plan and a failed bar.

    python3 plan_remediation.py <plan.json> <verify.json> > remediation-plan.json

When the bar judge fails the assembled review, one remediation round re-runs
the implicated work instead of discarding the run: fresh clean-context
reviewers for the briefs the judge implicated (their prompts carry the bar's
complaints), plus a plain retry of any reviewer shard that returned nothing in
the first round — and exact suppressed candidates whose checker decision the
bar rejected. The Codex leg is re-run only when it failed. This
script derives that round's plan mechanically from the original plan and the
verification report, so the re-dispatch reviews exactly what the record says
went wrong, nothing more.

The output is a plan in the same shape plan_review.py emits, consumable by
review_workflow.js (and run_codex_review.py) unchanged:

  - an implicated brief keeps all its shards and gains a `remediation` field
    ({"reasons": [...]}, carrying only that brief's own complaints from the
    verdict's per-brief association) that the workflow folds into each
    reviewer prompt;
  - a brief whose only defect was unreturned shards keeps just those shards —
    the returned shards' output stands, so only the gap is re-reviewed;
  - every other brief is omitted: its round-one output carries forward, and
    merge_remediation.py does that bookkeeping;
  - the Codex leg is re-run only when it *failed* (an execution failure, which
    may be transient). An *implicated* leg is not re-run: its external CLI
    reviewer takes no re-briefing, so a re-run would repeat the same review
    with the same inputs — the round-one result is carried instead and the
    complaint is surfaced as a warning. A leg not re-run is emitted as skipped
    with skip_kind "carried" (the marker merge_remediation.py drops in favour
    of the round-one leg records).

`candidate_reconsiderations` rides on the plan for the merge step. It is not a
new finding: the exact candidate is recovered from the failed bar's exhaustive
verification result and re-enters ordinary checker-disjoint verification.

Exits 1 with an {"error": ...} document when there is nothing to remediate:
the bar did not run, did not fail, or implicated nothing that maps to a
dispatched brief and no shard failed. The caller reports that state instead of
looping.
"""

import argparse
import json
import sys


def load(path):
    try:
        with open(path, encoding="utf-8") as handle:
            return json.load(handle)
    except (OSError, json.JSONDecodeError) as exc:
        print(f"Could not read '{path}': {exc}", file=sys.stderr)
        sys.exit(1)


def refuse(message):
    print(json.dumps({"error": message}))
    sys.exit(1)


def failed_shard_indices(failures):
    """Map brief name -> set of 1-based shard indices that returned nothing.

    A failure's shard is recorded as "index/total"; a malformed record keeps
    the brief in the map with no parseable index, which the caller treats as
    "re-run every shard" — the conservative direction.
    """
    failed = {}
    for failure in failures or []:
        name = failure.get("brief")
        if not name:
            continue
        indices = failed.setdefault(name, set())
        shard = str(failure.get("shard") or "")
        head = shard.split("/", 1)[0]
        if head.isdigit():
            indices.add(int(head))
    return failed


def main():
    parser = argparse.ArgumentParser(
        description="Derive the remediation round's plan from a failed bar verdict."
    )
    parser.add_argument("plan", help="The round-one plan JSON from plan_review.py.")
    parser.add_argument("verify", help="The round-one verification report JSON.")
    args = parser.parse_args()

    plan = load(args.plan)
    verify = load(args.verify)

    bar = verify.get("bar") or {}
    if not bar.get("ran"):
        refuse("Nothing to remediate: the bar check did not run.")
    if bar.get("outcome") != "fail":
        refuse(f"Nothing to remediate: the bar outcome is '{bar.get('outcome')}', not 'fail'.")

    reasons = bar.get("reasons") or []
    reconsiderations = bar.get("candidate_reconsiderations") or []
    attested = (verify.get("bar_attestation") or {}).get("candidate_reconsiderations") or []
    if reconsiderations != attested:
        refuse("Nothing to remediate: candidate reconsiderations do not match the bar attestation.")
    # The verdict associates each implicated brief with its own subset of the
    # reasons, so a re-run reviewer is never fed complaints about another
    # brief's review. A bare-string entry (a degraded verdict shape) falls
    # back to the full reason list — over-informing beats losing the round.
    implicated = {}
    for item in bar.get("implicated_briefs") or []:
        if isinstance(item, str):
            implicated[item] = list(reasons)
        elif item.get("brief"):
            implicated[item["brief"]] = list(item.get("reasons") or []) or list(reasons)
    failed = failed_shard_indices(verify.get("failures"))

    leg = plan.get("codex_review") or {}
    leg_name = leg.get("name", "codex-review")

    warnings = list(plan.get("warnings") or [])
    briefs = []
    active_names = set()
    for brief in plan.get("briefs") or []:
        name = brief.get("name")
        shards = brief.get("shards") or []
        if shards:
            active_names.add(name)
        if name in implicated and shards:
            entry = dict(brief)
            entry["remediation"] = {"reasons": implicated[name]}
            briefs.append(entry)
        elif name in failed and shards:
            indices = failed[name]
            # No parseable index means the failure record is unusable — re-run
            # the whole brief rather than guess which slice went unreviewed.
            kept = [s for s in shards if s.get("index") in indices] if indices else list(shards)
            if not kept:
                kept = list(shards)
            entry = dict(brief)
            entry["shards"] = kept
            briefs.append(entry)

    for name in sorted(implicated):
        if name != leg_name and name not in active_names:
            warnings.append(
                f"The bar implicated '{name}', which matches no dispatched brief; "
                f"nothing was re-run for it."
            )

    # Only an execution failure earns the leg a re-run — that may be
    # transient. An implicated leg is carried: its external CLI reviewer takes
    # no re-briefing, so re-running it would repeat the same review with the
    # same inputs at real cost, and the complaint is reported instead.
    rerun_leg = leg_name in failed and not leg.get("skip_reason")
    if leg_name in implicated and not rerun_leg:
        warnings.append(
            f"The bar implicated '{leg_name}'; the external leg cannot be re-briefed "
            f"with the complaint, so its round-one result is carried and the "
            f"complaint is reported rather than remediated."
        )

    if rerun_leg:
        codex_review = dict(leg)
    else:
        codex_review = {
            "name": leg_name,
            "title": leg.get("title", "Codex Review"),
            "kind": "external",
            "skip_reason": "Round-one leg result carried forward; not re-run.",
            "skip_kind": "carried",
        }

    if not briefs and not rerun_leg and not reconsiderations:
        refuse(
            "Nothing to remediate mechanically: the bar implicated no dispatched "
            "brief and no reviewer shard failed. Report the bar verdict instead."
        )

    remediation_plan = dict(plan)
    remediation_plan["briefs"] = briefs
    remediation_plan["codex_review"] = codex_review
    remediation_plan["candidate_reconsiderations"] = reconsiderations
    remediation_plan["warnings"] = warnings

    print(json.dumps(remediation_plan, indent=2))


if __name__ == "__main__":
    main()
