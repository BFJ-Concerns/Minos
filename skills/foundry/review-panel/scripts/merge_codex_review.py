#!/usr/bin/env python3
"""Fold the Codex leg's result into the review workflow's result.

    python3 merge_codex_review.py <workflow-result.json> <codex-leg.json> > combined.json

The Codex leg (run_codex_review.py) runs beside the ensemble fan-out, not
inside it, so its findings and records arrive in a separate file. This script
merges them into the workflow result *before* the quote validator, so from the
validator on the leg is indistinguishable from any other concern: its findings
pass the same gates, its coverage joins the same account, and a leg that was
skipped or failed is reported alongside the other skips and failures.

The merge is additive only — nothing in the workflow result is rewritten.
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
        description="Merge the Codex leg's result into the review workflow result."
    )
    parser.add_argument("result", help="The review workflow result JSON.")
    parser.add_argument("leg", help="The Codex leg result JSON from run_codex_review.py.")
    args = parser.parse_args()

    doc = load(args.result)
    leg_doc = load(args.leg)
    leg = leg_doc.get("leg") or {}
    status = leg.get("status")
    name = leg.get("name", "codex-review")
    title = leg.get("title", "Codex Review")

    doc.setdefault("findings", [])
    doc.setdefault("reviews", [])
    doc.setdefault("coverage", [])
    doc.setdefault("skipped", [])
    doc.setdefault("failures", [])

    if status == "not-run":
        doc["skipped"].append({
            "name": name, "scope": None,
            "reason": leg.get("skip_reason") or "not run",
        })
        doc["coverage"].append({
            "brief": name, "title": title, "scope": None,
            "status": "not-run",
            "reason": leg.get("skip_reason") or "not run",
            "skip_kind": leg.get("skip_kind"),
        })
    elif status == "none":
        # The leg was planned but produced nothing — the CLI missing, failing,
        # or timing out. That is a coverage gap, reported exactly like a
        # reviewer shard that returned nothing.
        doc["failures"].append({"brief": name, "shard": "1/1", "scope": None})
        doc["coverage"].append({
            "brief": name, "title": title, "scope": None,
            "extent": "diff", "status": "none",
            "reason": leg.get("error") or "the leg produced no result",
        })
    else:
        findings = leg_doc.get("findings") or []
        overall = leg.get("overall") or {}
        notes = " ".join(
            part for part in (
                f"Codex's overall verdict: {overall['correctness']}." if overall.get("correctness") else None,
                overall.get("explanation"),
                f"(findings recovered from {leg.get('source')})" if leg.get("source") else None,
            ) if part
        )
        doc["findings"].extend(findings)
        # The leg's account is recorded honestly as an *external verdict*, not a
        # reviewer-method review. The Codex CLI emits only a verdict plus its
        # findings — never the files-read/commands-run trail a reviewer's own
        # clean-review note must carry — so labelling it reviewer-method `full`
        # would assert an evidence trail that does not exist. `method:
        # "external-cli"` on both the review and its coverage entry names the
        # category the bar judges on verdict-honesty (extent + faithful finding
        # mapping) rather than on that trail; every agent reviewer stays held to
        # the full evidence discipline. See prompts/bar-check.md.
        doc["reviews"].append({
            "brief": name, "shard": "1/1",
            "method": "external-cli",
            "notes": notes or None,
            "findings": len(findings),
        })
        doc["coverage"].append({
            "brief": name, "title": title, "scope": None,
            "extent": "diff", "status": "external-verdict",
            "method": "external-cli",
            "verdict": overall.get("correctness"),
            "findings_mapped": len(findings),
            "shards_dispatched": 1, "shards_returned": 1,
        })

    print(json.dumps(doc, indent=2))


if __name__ == "__main__":
    main()
