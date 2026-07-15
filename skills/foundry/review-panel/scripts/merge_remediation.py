#!/usr/bin/env python3
"""Merge the remediation round's validated output into the round-one record.

    python3 merge_remediation.py <verify1.json> <remediation-validated.json> > merged.json

After a failed bar triggers the remediation round (plan_remediation.py), the
re-run's quote-validated output must be folded back into the round-one state
before re-verification. The merge is mechanical, and its one rule is
replacement by account, preservation by evidence: a re-reviewed brief's
*account* — its reviewer notes and its coverage entry — speaks only through
the new round, because the account is what the bar condemned; its round-one
*findings* that survived quote validation and an independent checker are
kept, because a brief-level complaint about a review's honesty is not
finding-level evidence against work that was independently confirmed.

  - findings: every round-one survivor carries forward — each still bears its
    `checked_by`, so the re-verify passes it through without spending a fresh
    checker. Round-one findings suppressed as check-failed (no checker reached
    them — withheld, not rejected) are re-included without `checked_by`, so
    the re-verify finally checks them. The remediation round's own findings
    arrive fresh. A candidate named by the failed bar is recovered from the
    exact exhaustive verification result and re-included without its old
    checker verdict, so it receives a fresh independent check. A re-discovery
    of a kept finding at the same site collapses at the poster.
  - coverage: a fully re-run brief's entry is replaced; a brief that only
    retried failed shards gets a merged entry (round-one dispatched total,
    returns and declared gaps combined); the rest carry forward. The
    remediation doc's "carried" placeholders (skip_kind "carried", from
    plan_remediation.py) are dropped in favour of the round-one records.
  - reviews: replaced for fully re-run briefs, appended otherwise.
  - failures: only the remediation round's — every round-one failure was
    either re-dispatched or superseded by its brief's full re-run.
  - the validator's records accumulate across rounds: suppression lists are
    concatenated and the quote_validation counts are summed, so the totals
    state what actually ran.

The output is shaped like validate_quotes.py's output, ready for
assemble_verify_input.py. The round-one checker records (suppressed_by_checkers,
reclassified, attribution_indeterminate) are deliberately not merged: the
surviving finding objects already carry their applied effects, and the run
report reads both rounds' verification files for the gate story.
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


def is_carried_placeholder(entry):
    """The remediation plan's stand-in for work deliberately not re-run."""
    return entry.get("status") == "not-run" and entry.get("skip_kind") == "carried"


def refuse(message):
    print(message, file=sys.stderr)
    sys.exit(1)


def producer_key(entry):
    producer = entry.get("producer") or {}
    return producer.get("id"), producer.get("ordinal")


def main():
    parser = argparse.ArgumentParser(
        description="Fold the remediation round's validated output into the round-one record."
    )
    parser.add_argument("verify1", help="Round-one verification report JSON.")
    parser.add_argument(
        "remediation",
        help="The remediation round's validated result JSON from validate_quotes.py.",
    )
    args = parser.parse_args()

    round_one = load(args.verify1)
    rem = load(args.remediation)

    verification_result = round_one.get("verification_result") or {}
    disposition_manifest = round_one.get("disposition_manifest") or {}
    reconsiderations = (round_one.get("bar") or {}).get("candidate_reconsiderations") or []
    attested = (round_one.get("bar_attestation") or {}).get("candidate_reconsiderations") or []
    if reconsiderations != attested:
        refuse("candidate reconsiderations do not match the failed bar attestation")

    verification_by_producer = {
        producer_key(entry): entry for entry in verification_result.get("candidates") or []
    }
    manifest_by_id = {
        (entry.get("candidate") or {}).get("candidate_id"): entry
        for entry in disposition_manifest.get("candidates") or []
    }
    reopened = []
    reconsidered_keys = []
    for request in reconsiderations:
        candidate_id = request.get("candidate_id")
        manifest_entry = manifest_by_id.get(candidate_id)
        if not manifest_entry or manifest_entry.get("outcome") != "suppressed":
            refuse(f"candidate reconsideration {candidate_id!r} is not an exact suppressed manifest candidate")
        key = producer_key(manifest_entry.get("candidate") or {})
        verification_entry = verification_by_producer.get(key)
        if not verification_entry or verification_entry.get("outcome") != "suppressed":
            refuse(f"candidate reconsideration {candidate_id!r} is not suppressed in the exhaustive verification result")
        candidate = dict(verification_entry.get("candidate") or {})
        candidate.pop("checked_by", None)
        reopened.append(candidate)
        reconsidered_keys.append({
            "candidate_id": candidate_id,
            "producer_identity": key[0],
            "producer_ordinal": key[1],
            "reasons": list(request.get("reasons") or []),
        })

    rem_coverage = [c for c in rem.get("coverage") or [] if not is_carried_placeholder(c)]
    rem_by_brief = {c.get("brief"): c for c in rem_coverage}
    r1_by_brief = {c.get("brief"): c for c in round_one.get("coverage") or []}

    # A re-run that dispatched fewer shards than round one only retried the
    # failed slice; its brief's round-one output still stands. Everything else
    # in the remediation doc is a full re-run whose output replaces round one's.
    full_rerun = set()
    partial_rerun = set()
    for name, entry in rem_by_brief.items():
        previous = r1_by_brief.get(name)
        dispatched = entry.get("shards_dispatched") or 0
        prev_dispatched = (previous or {}).get("shards_dispatched") or 0
        if previous is not None and dispatched < prev_dispatched:
            partial_rerun.add(name)
        else:
            full_rerun.add(name)

    # Findings: every carried survivor + every re-checkable check-failed +
    # the new round. Survivors of a re-run brief are deliberately kept — see
    # the module docstring — while rejected findings stay rejected.
    findings = list(round_one.get("findings") or [])
    for record in round_one.get("suppressed_by_checkers") or []:
        if record.get("kind") != "check-failed":
            continue
        findings.append(record.get("finding") or {})
    findings.extend(rem.get("findings") or [])
    findings.extend(reopened)

    # Coverage: replace, merge, or carry forward per brief.
    coverage = []
    for entry in round_one.get("coverage") or []:
        name = entry.get("brief")
        if name in full_rerun:
            coverage.append(rem_by_brief[name])
        elif name in partial_rerun:
            addition = rem_by_brief[name]
            merged = dict(entry)
            merged["shards_returned"] = (entry.get("shards_returned") or 0) + (
                addition.get("shards_returned") or 0
            )
            gaps = list(entry.get("declared_gaps") or []) + list(
                addition.get("declared_gaps") or []
            )
            if gaps:
                merged["declared_gaps"] = gaps
            returned = merged["shards_returned"]
            dispatched = merged.get("shards_dispatched") or 0
            merged["status"] = (
                "none" if returned == 0
                else "partial" if returned < dispatched or gaps
                else "full"
            )
            coverage.append(merged)
        else:
            coverage.append(entry)
    for name, entry in rem_by_brief.items():
        if name not in r1_by_brief:
            coverage.append(entry)

    reviews = [
        r for r in round_one.get("reviews") or []
        if r.get("brief") not in full_rerun
    ] + list(rem.get("reviews") or [])

    suppressed_by_validator = list(round_one.get("suppressed_by_validator") or []) + list(
        rem.get("suppressed_by_validator") or []
    )

    counts = {}
    for doc in (round_one, rem):
        for key, value in (doc.get("quote_validation") or {}).items():
            if isinstance(value, (int, float)):
                counts[key] = counts.get(key, 0) + value

    print(json.dumps({
        "findings": findings,
        "coverage": coverage,
        "skipped": round_one.get("skipped") or [],
        "failures": rem.get("failures") or [],
        "reviews": reviews,
        "sweep_advisories": round_one.get("sweep_advisories") or [],
        "mode": round_one.get("mode"),
        "pr": round_one.get("pr"),
        "suppressed_by_validator": suppressed_by_validator,
        "quote_validation": counts or None,
        "prior_verification_result": verification_result,
        "candidate_reconsiderations": reconsidered_keys,
    }, indent=2))


if __name__ == "__main__":
    main()
