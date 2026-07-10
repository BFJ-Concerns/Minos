#!/usr/bin/env python3
"""Assemble the verification workflow's input from the run's stage files.

The verify workflow (scripts/verify_workflow.js) needs the plan, the quote-
validated findings, and the review workflow's collated records in one JSON
object. Hand-building that object invites drift between what the stages emit
and what the workflow expects, and freeform reviewer notes make it easy to
corrupt by string assembly — so this script does the merge: two file paths in,
one valid JSON document out.

    python3 assemble_verify_input.py <plan.json> <validated.json> \
        [--no-verify] [--bar off|on|auto] > verify-input.json

<validated.json> is validate_quotes.py's output — the review workflow result
with findings reduced to quote-checked survivors and the suppression record
attached. Every field the final report needs rides through.
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
    args = parser.parse_args()

    plan = load(args.plan)
    validated = load(args.validated)

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
        "verify": not args.no_verify,
        "bar_mode": args.bar,
    }, indent=2))


if __name__ == "__main__":
    main()
