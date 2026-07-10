#!/usr/bin/env python3
"""Run the Codex CLI's built-in review as one leg of the review panel.

The panel's other reviewers are clean-context agents dispatched by the ensemble
workflow; this leg is different machinery reaching the same report: it runs
`codex review --base <ref>` — the Codex CLI's own non-interactive reviewer —
and maps its findings into the panel's finding schema, so they flow through the
same quote validator, checker gate, and poster as every other concern's.

    python3 run_codex_review.py <plan.json> > codex-leg.json

Reads the plan from plan_review.py. When the plan's `codex_review` entry is
missing or carries a skip_reason, this script runs nothing and emits the skip
as its result — so the calling skill invokes it unconditionally and never has
to branch on why the leg was excluded.

Output shape (merge_codex_review.py folds it into the workflow result):

  {
    "leg": {
      "name": "codex-review", "title": "Codex Review",
      "status": "full" | "none" | "not-run",
      "skip_reason": ..., "skip_kind": ...,      # not-run only
      "error": ...,                              # none only
      "source": "rollout" | "stdout",            # full only
      "overall": {"correctness": ..., "explanation": ..., "confidence": ...}
    },
    "findings": [ ... panel-schema findings ... ]
  }

Findings come from the structured `review_output` that Codex records in its
rollout transcript (`$CODEX_HOME/sessions/...`): title, body, numeric priority
on the same P0–P3 scale, and a file/line-range location. The transcript is the
primary source because stdout is a human rendering of the same data; when no
transcript can be matched to the run, the stdout rendering is parsed instead,
and the `source` field records which was used.

`code_quote` is filled mechanically here — the verbatim lines at the cited
location, read from the working tree (or the merge-base, for a file the branch
deleted). Codex's reviewer does not quote the code it flags, and the panel's
downstream gates key on the quote: the validator uses it to confine the finding
to real, cited code, and the poster uses it to render and de-duplicate. A
location that cannot be read produces an empty quote, which the validator then
suppresses — the correct outcome for a finding citing code that is not there.
"""

import argparse
import json
import os
import re
import subprocess
import sys
import time

# How long the CLI may run before the leg is recorded as failed rather than
# left hanging. Codex reviews a diff in a few minutes; a large branch takes
# longer, so the ceiling is generous. Override with --timeout.
DEFAULT_TIMEOUT = 1200

# The stdout rendering of one finding, e.g.
#   - [P2] Handle empty input — /abs/path/pricing.py:21-22
# Body lines follow, indented. Used only when no rollout transcript matches.
STDOUT_FINDING_RE = re.compile(
    r"^- \[(?P<prio>P[0-3])\] (?P<title>.+?) — (?P<path>.+?):(?P<start>\d+)(?:-(?P<end>\d+))?\s*$"
)


def fail(message):
    print(message, file=sys.stderr)
    sys.exit(1)


def emit(leg, findings):
    print(json.dumps({"leg": leg, "findings": findings}, indent=2))


def load_plan(path):
    try:
        with open(path, encoding="utf-8") as handle:
            return json.load(handle)
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"Could not read plan '{path}': {exc}")


# --- Rollout transcript extraction ------------------------------------------

def sessions_dir():
    codex_home = os.environ.get("CODEX_HOME") or os.path.expanduser("~/.codex")
    return os.path.join(codex_home, "sessions")


def recent_rollouts(since):
    """Rollout JSONL files modified at or after `since`, newest first.

    Rollouts are laid out by date (sessions/YYYY/MM/DD/rollout-*.jsonl); rather
    than reconstruct which date directories a run could touch, walk the tree and
    filter on mtime — the sessions tree is small enough that this is cheap.
    """
    found = []
    root = sessions_dir()
    if not os.path.isdir(root):
        return found
    for current, _dirs, files in os.walk(root):
        for name in files:
            if not name.endswith(".jsonl"):
                continue
            path = os.path.join(current, name)
            try:
                mtime = os.path.getmtime(path)
            except OSError:
                continue
            if mtime >= since:
                found.append((mtime, path))
    return [path for _mtime, path in sorted(found, reverse=True)]


def review_output_from_rollout(path, repo_root, base_ref):
    """The `review_output` payload in one rollout, or None.

    A rollout is this run's if it belongs to a session working in the reviewed
    repository (its meta or turn-context records the cwd), it entered review
    mode against the same base (`entered_review_mode` records the target
    branch), and it recorded a completed review (`exited_review_mode` carrying
    `review_output`). Together with the caller's start-time filter these
    separate this run's review from another codex session in the same
    checkout; a concurrent `codex review` of the same repository against the
    same base in the same window remains indistinguishable — and is reviewing
    the same thing.
    """
    cwd_matches = False
    base_matches = False
    review_output = None
    try:
        with open(path, encoding="utf-8") as handle:
            for line in handle:
                try:
                    entry = json.loads(line)
                except json.JSONDecodeError:
                    continue
                payload = entry.get("payload") or {}
                if entry.get("type") in ("session_meta", "turn_context"):
                    cwd = payload.get("cwd") or (payload.get("state") or {}).get("cwd")
                    if cwd and os.path.realpath(cwd) == repo_root:
                        cwd_matches = True
                if payload.get("type") == "entered_review_mode":
                    branch = (payload.get("target") or {}).get("branch")
                    if branch == base_ref:
                        base_matches = True
                if payload.get("type") == "exited_review_mode" and payload.get("review_output"):
                    review_output = payload["review_output"]
    except OSError:
        return None
    return review_output if (cwd_matches and base_matches) else None


def extract_from_rollouts(since, repo_root, base_ref):
    for path in recent_rollouts(since):
        output = review_output_from_rollout(path, repo_root, base_ref)
        if output is not None:
            return output
    return None


# --- Stdout fallback parsing --------------------------------------------------

def parse_stdout(text):
    """Reconstruct findings from the CLI's human rendering.

    Returns (findings, overall_explanation): raw finding dicts in the rollout's
    shape, so one mapping path serves both sources. The overall explanation is
    the prose before the finding list.
    """
    findings = []
    preamble = []
    current = None
    for line in text.splitlines():
        match = STDOUT_FINDING_RE.match(line)
        if match:
            current = {
                "title": f"[{match.group('prio')}] {match.group('title')}",
                "body": "",
                "priority": int(match.group("prio")[1]),
                "code_location": {
                    "absolute_file_path": match.group("path"),
                    "line_range": {
                        "start": int(match.group("start")),
                        "end": int(match.group("end") or match.group("start")),
                    },
                },
            }
            findings.append(current)
        elif current is not None:
            stripped = line.strip()
            if stripped:
                current["body"] = f"{current['body']} {stripped}".strip()
        elif line.strip() and not line.strip().lower().startswith("full review comments"):
            preamble.append(line.strip())
    return findings, " ".join(preamble) or None


# --- Mapping into the panel schema -------------------------------------------

def priority_label(raw, title):
    """P0–P3 from the structured integer, else from the title's [Pn] tag, else P2."""
    if isinstance(raw, int) and 0 <= raw <= 3:
        return f"P{raw}"
    match = re.match(r"^\[(P[0-3])\]", title or "")
    if match:
        return match.group(1)
    return "P2"


def quote_at(repo_root, rel_path, start, end, merge_base):
    """(quote, side): the verbatim lines at the cited location.

    Reads the working tree — the version the validator checks RIGHT-side quotes
    against. A file the branch deleted is read from the merge-base instead and
    cited side LEFT, matching how the panel's own reviewers cite removed code.
    Returns ("", "RIGHT") when the location cannot be read; the validator then
    suppresses the finding, which is the correct fate for an unreadable citation.
    """
    abs_path = os.path.join(repo_root, rel_path)
    side = "RIGHT"
    lines = None
    if os.path.isfile(abs_path):
        try:
            with open(abs_path, encoding="utf-8", errors="replace") as handle:
                lines = handle.read().splitlines()
        except OSError:
            lines = None
    elif merge_base:
        try:
            shown = subprocess.run(
                ["git", "show", f"{merge_base}:{rel_path}"],
                cwd=repo_root, capture_output=True, text=True, check=True,
            ).stdout
            lines = shown.splitlines()
            side = "LEFT"
        except subprocess.CalledProcessError:
            lines = None
    if not lines or start < 1 or start > len(lines):
        return "", "RIGHT"
    end = max(start, min(end or start, len(lines)))
    return "\n".join(lines[start - 1:end]), side


def map_findings(raw_findings, repo_root, merge_base, leg_name, leg_title):
    mapped = []
    for raw in raw_findings or []:
        title = (raw.get("title") or "").strip()
        priority = priority_label(raw.get("priority"), title)
        title = re.sub(r"^\[P[0-3]\]\s*", "", title) or "Codex review finding"
        location = raw.get("code_location") or {}
        abs_path = location.get("absolute_file_path") or ""
        line_range = location.get("line_range") or {}
        start = line_range.get("start") or None
        end = line_range.get("end") or start
        # Relativise against the repo root. A path outside the repository is
        # passed through as-is for the validator to suppress — mapping is not
        # the place to silently drop a finding.
        if os.path.isabs(abs_path):
            real = os.path.realpath(abs_path)
            rel_path = os.path.relpath(real, repo_root) if (
                real == repo_root or real.startswith(repo_root + os.sep)
            ) else abs_path
        else:
            rel_path = abs_path
        quote, side = ("", "RIGHT")
        if rel_path and not os.path.isabs(rel_path) and start:
            quote, side = quote_at(repo_root, rel_path, start, end, merge_base)
        finding = {
            "file": rel_path,
            "line": start,
            "side": side,
            "priority": priority,
            "title": title,
            "message": (raw.get("body") or "").strip(),
            "code_quote": quote,
            "brief": leg_name,
            "review_title": leg_title,
            "scope": None,
            "extent": "diff",
            "producer": f"{leg_name}@codex-cli",
        }
        confidence = raw.get("confidence_score")
        if isinstance(confidence, (int, float)):
            finding["confidence"] = confidence
        mapped.append(finding)
    return mapped


# --- Entry point --------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(
        description="Run the Codex CLI's built-in review and map its findings into the panel schema."
    )
    parser.add_argument("plan", help="The plan JSON from plan_review.py.")
    parser.add_argument(
        "--timeout", type=int, default=DEFAULT_TIMEOUT,
        help=f"Seconds before the CLI run is abandoned (default {DEFAULT_TIMEOUT}).",
    )
    args = parser.parse_args()

    plan = load_plan(args.plan)
    entry = plan.get("codex_review")
    if not entry:
        # A plan from before the leg existed: record it as not planned rather
        # than inventing a run the planner never sanctioned.
        emit({
            "name": "codex-review", "title": "Codex Review", "status": "not-run",
            "skip_reason": "The plan carries no codex_review entry — re-run plan_review.py.",
            "skip_kind": "flag",
        }, [])
        return
    leg = {"name": entry["name"], "title": entry["title"]}
    if entry.get("skip_reason"):
        leg.update({
            "status": "not-run",
            "skip_reason": entry["skip_reason"],
            "skip_kind": entry.get("skip_kind"),
        })
        emit(leg, [])
        return

    repo_root = os.path.realpath(plan["repo_root"])
    base_ref = plan.get("base_ref")
    if not base_ref:
        leg.update({"status": "none", "error": "The plan carries no base_ref to review against."})
        emit(leg, [])
        return

    started = time.time()
    try:
        result = subprocess.run(
            ["codex", "review", "--base", base_ref],
            cwd=repo_root, capture_output=True, text=True, timeout=args.timeout,
            env={**os.environ, "FORCE_COLOR": "0"},
        )
    except FileNotFoundError:
        leg.update({"status": "none", "error": "codex CLI not found on PATH."})
        emit(leg, [])
        return
    except subprocess.TimeoutExpired:
        leg.update({"status": "none", "error": f"codex review did not finish within {args.timeout}s."})
        emit(leg, [])
        return
    if result.returncode != 0:
        tail = (result.stderr or result.stdout or "").strip()[-500:]
        leg.update({"status": "none", "error": f"codex review exited {result.returncode}: {tail}"})
        emit(leg, [])
        return

    # Primary source: the structured review_output in the run's rollout
    # transcript. Fallback: parse the stdout rendering of the same data.
    output = extract_from_rollouts(started - 5, repo_root, base_ref)
    if output is not None:
        raw_findings = output.get("findings") or []
        leg["source"] = "rollout"
        leg["overall"] = {
            "correctness": output.get("overall_correctness"),
            "explanation": output.get("overall_explanation"),
            "confidence": output.get("overall_confidence_score"),
        }
        leg["status"] = "full"
    else:
        raw_findings, explanation = parse_stdout(result.stdout or "")
        if raw_findings:
            leg["source"] = "stdout"
            leg["overall"] = {"correctness": None, "explanation": explanation, "confidence": None}
            leg["status"] = "full"
        else:
            # No structured payload and nothing the stdout parser recognised.
            # That is EITHER a genuinely clean review or an output format this
            # parser no longer understands — indistinguishable from here, so
            # report a coverage gap rather than risk dressing lost findings as
            # a clean pass.
            leg["status"] = "none"
            tail = (result.stdout or "").strip()[-300:]
            leg["error"] = (
                "no rollout transcript matched this run and the stdout rendering "
                "yielded no findings — a clean review and an unrecognised output "
                f"format are indistinguishable here. stdout ended: {tail!r}"
            )
            emit(leg, [])
            return

    emit(leg, map_findings(raw_findings, repo_root, plan.get("merge_base"),
                           entry["name"], entry["title"]))


if __name__ == "__main__":
    main()
