#!/usr/bin/env python3
"""Assemble the verification workflow's input from the run's stage files.

The verify workflow (scripts/verify_workflow.js) needs the plan, the quote-
validated findings, and the review workflow's collated records in one JSON
object. Hand-building that object invites drift between what the stages emit
and what the workflow expects, and freeform reviewer notes make it easy to
corrupt by string assembly — so this script does the merge: two file paths in,
one valid JSON document out.

    python3 assemble_verify_input.py <plan.json> <validated.json> \
        [--no-verify] [--bar off|on|auto] \
        [--coverage-result <coverage-check.json>] \
        [--inspection-record <inspection.json>] > verify-input.json

<validated.json> is validate_quotes.py's output — the review workflow result
with findings reduced to quote-checked survivors and the suppression record
attached. Every field the final report needs rides through.

Two optional, additive coverage inputs travel to the verify workflow as
separate, unconflated artefacts — the distinction the bar's depth judgement
turns on:

--coverage-result forwards the mechanical coverage-accounting *result* (a
`status` plus `omissions`, e.g. account-coverage's output over the diff's own
blobs). The workflow's convergence gate keys off this: a non-`complete` status
hard-gates convergence deterministically.

--inspection-record forwards the lead-owned *inspection record* the accounting
consumed — the changed files with their per-hunk read/omitted accounts and the
named definitions and call-sites inspected. This is the evidence the bar judges
coverage *depth* from; the accounting result only proves each item was labelled,
not that it was read deeply. A caller with neither omits both flags and nothing
changes.
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


def main():
    parser = argparse.ArgumentParser(
        description="Merge the plan and the validated review result into the verify workflow's input."
    )
    parser.add_argument("plan", help="The plan JSON from plan_review.py.")
    parser.add_argument("validated", help="The validated result JSON from validate_quotes.py.")
    parser.add_argument(
        "--no-verify", action="store_true",
        help="Skip the per-finding checkers (the quote gate has already run).",
    )
    parser.add_argument(
        "--bar", choices=["off", "on", "auto"], default="off",
        help="Bar-check mode: off (default), on (forced), auto (run when the trigger fires).",
    )
    parser.add_argument(
        "--coverage-result", default=None,
        help="Optional path to the mechanical coverage-accounting result "
             "(status + omissions); the workflow's convergence gate keys off it.",
    )
    parser.add_argument(
        "--inspection-record", default=None,
        help="Optional path to the lead-owned inspection record (changed files, "
             "per-hunk read/omitted accounts, named definition/call-site references); "
             "the bar judges coverage depth from it.",
    )
    args = parser.parse_args()

    plan = load(args.plan)
    validated = load(args.validated)
    # Both coverage artefacts are optional and kept distinct: the accounting
    # result the gate trusts, and the inspection record the bar judges depth
    # from. Absent, each key is null and its downstream use stays inactive.
    coverage_result = load(args.coverage_result) if args.coverage_result else None
    inspection_record = load(args.inspection_record) if args.inspection_record else None

    print(json.dumps({
        "plan": plan,
        "findings": validated.get("findings", []),
        "coverage": validated.get("coverage", []),
        "skipped": validated.get("skipped", []),
        "failures": validated.get("failures", []),
        "reviews": validated.get("reviews", []),
        "sweep_advisories": validated.get("sweep_advisories", []),
        "mode": validated.get("mode", plan.get("mode")),
        "pr": validated.get("pr", plan.get("pr")),
        "suppressed_by_validator": validated.get("suppressed_by_validator", []),
        "quote_validation": validated.get("quote_validation"),
        "prior_verification_result": validated.get("prior_verification_result"),
        "candidate_reconsiderations": validated.get("candidate_reconsiderations", []),
        "verify": not args.no_verify,
        "bar_mode": args.bar,
        "coverage_result": coverage_result,
        "inspection_record": inspection_record,
    }, indent=2))


if __name__ == "__main__":
    main()
