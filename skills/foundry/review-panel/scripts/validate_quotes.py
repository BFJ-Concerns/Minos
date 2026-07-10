#!/usr/bin/env python3
"""Validate that every finding's code_quote exists where the finding says it does.

This is the zero-model gate between the reviewers and the per-finding checkers:
mechanical code, no judgement. A finding whose verbatim quote cannot be found in
the cited file did not come from reading that file — it is fabricated or
mislocated, the single failure mode that most reliably poisons a review — so it
is suppressed here before any checker spends a call on it, and before anything
reaches a PR.

The gate distrusts the findings wholesale, paths included. A finding's `file`
must be a repository-relative path that stays inside the repository and names a
git-tracked file within the finding's own scope — an absolute path, a `../`
escape, or an untracked file is suppressed, never opened.

Reads the review-workflow result JSON (a file-path argument, else stdin), checks
each finding, and prints the same document back with:

  - `findings` reduced to the survivors, each annotated `quote_check: "passed"`,
    with a wrong `line` corrected to where the quote actually is;
  - `suppressed_by_validator` listing each suppressed finding with its reason;
  - `quote_validation` summary counts.

Matching is mechanical and content-exact per line, with one concession to how
models copy code: each line is compared after stripping leading and trailing
whitespace, so indentation lost in transcription does not fail a genuine quote.
The content itself must match exactly; a quote that is empty, whitespace, or
absent from the file fails.

Run from the reviewed repo's root. `side: RIGHT` (or unset) quotes are checked
against the working tree — the same bytes the reviewers read. `side: LEFT`
quotes cite a removed line, so they are checked against the diff's merge-base;
pass `--base <ref>` (the plan's `base_ref`) and the merge-base is resolved from
it, or LEFT-side findings are suppressed as uncheckable.
"""

import argparse
import json
import os
import subprocess
import sys


def stripped_lines(text):
    """Normalise text for matching: split lines, strip each, drop the trailing
    blank a final newline produces."""
    lines = [line.strip() for line in text.replace("\r\n", "\n").split("\n")]
    while lines and lines[-1] == "":
        lines.pop()
    return lines


def git_lines(args):
    """stdout lines of a git command in the cwd, or None if it fails."""
    try:
        out = subprocess.run(
            ["git", *args], capture_output=True, text=True, check=True,
        ).stdout
    except (subprocess.CalledProcessError, FileNotFoundError):
        return None
    return out


def tracked_set():
    """The set of git-tracked paths in the working tree, or None if git failed."""
    out = git_lines(["ls-files"])
    if out is None:
        return None
    return {line for line in out.splitlines() if line.strip()}


def confine_path(path, root, tracked):
    """None when the path is admissible, else the suppression reason.

    Admissible means: relative, resolving inside the repository root, and (when
    the tracked set is known) a git-tracked file. Everything else is a path the
    reviewers were never dispatched to, so a finding citing it is suppressed
    unopened.
    """
    if os.path.isabs(path):
        return "file is an absolute path; findings cite repository-relative paths"
    resolved = os.path.realpath(os.path.join(root, path))
    if resolved != root and not resolved.startswith(root + os.sep):
        return "file escapes the repository root"
    if tracked is not None and path not in tracked:
        return "file is not a git-tracked path in this repository"
    return None


def in_scope(path, scope):
    """Does the cited file fall inside the finding's own brief scope?"""
    if not scope:
        return True
    return path == scope or path.startswith(scope + "/")


def read_file_lines(path, side, merge_base):
    """The file's lines in the revision the quote cites, or None if unreadable.

    RIGHT (the current version) reads the working tree — the same bytes the
    reviewers read. LEFT (a removed line) reads the diff's merge-base via git.
    """
    if side == "LEFT":
        if not merge_base:
            return None
        out = git_lines(["show", f"{merge_base}:{path}"])
        if out is None:
            return None
        return stripped_lines(out)
    try:
        with open(path, encoding="utf-8", errors="replace") as handle:
            return stripped_lines(handle.read())
    except OSError:
        return None


def find_occurrences(file_lines, quote_lines):
    """1-based start lines of every place the quote's line sequence appears."""
    n, m = len(file_lines), len(quote_lines)
    if m == 0 or m > n:
        return []
    return [
        i + 1
        for i in range(n - m + 1)
        if file_lines[i:i + m] == quote_lines
    ]


def check_finding(finding, root, tracked, merge_base):
    """(verdict, detail) for one finding.

    Verdicts:
      - ("passed", corrected_line_or_None) — the quote exists; if the cited line
        missed it, the corrected line is where it actually starts.
      - ("suppressed", reason) — inadmissible path, no quote, no readable file,
        or the quote is not in the file.
    """
    quote = finding.get("code_quote") or ""
    quote_lines = stripped_lines(quote)
    if not any(quote_lines):
        return "suppressed", "code_quote is empty or whitespace"

    path = finding.get("file")
    if not path:
        return "suppressed", "finding names no file"
    side = (finding.get("side") or "RIGHT").upper()
    # A LEFT-side quote cites the pre-change version — the file may legitimately
    # have been deleted by the branch, so the tracked-in-working-tree requirement
    # does not apply. Path confinement (relative, inside the repo) still does;
    # readability at the merge-base is checked below by read_file_lines.
    reason = confine_path(path, root, None if side == "LEFT" else tracked)
    if reason:
        return "suppressed", reason
    if not in_scope(path, finding.get("scope")):
        return "suppressed", (
            f"file is outside the brief's scope ({finding.get('scope')}/)"
        )

    if side == "LEFT" and not merge_base:
        return "suppressed", (
            "side is LEFT but no merge-base is available to check the pre-change version"
        )
    file_lines = read_file_lines(path, side, merge_base)
    if file_lines is None:
        which = f"{merge_base}:{path}" if side == "LEFT" else path
        return "suppressed", f"could not read {which}"

    occurrences = find_occurrences(file_lines, quote_lines)
    if not occurrences:
        return "suppressed", (
            f"code_quote not found in {path}"
            + (f" at {merge_base}" if side == "LEFT" else "")
        )

    cited = finding.get("line")
    if cited is not None:
        span = len(quote_lines)
        for start in occurrences:
            if start <= cited <= start + span - 1:
                return "passed", None
        # The quote is real but the cited line is wrong. Correct it to the
        # occurrence nearest the cited line, so the finding lands where the
        # code actually is instead of being thrown away for an off-by-N.
        nearest = min(occurrences, key=lambda start: abs(start - cited))
        return "passed", nearest
    return "passed", None


def main():
    parser = argparse.ArgumentParser(
        description="Mechanical check that each finding's code_quote exists at its cited path."
    )
    parser.add_argument("file", nargs="?", help="Review result JSON file (else stdin).")
    parser.add_argument(
        "--base", default=None,
        help="Base ref from the plan; its merge-base with HEAD is used to check "
             "side: LEFT quotes against the pre-change version.",
    )
    args = parser.parse_args()

    if args.file:
        try:
            with open(args.file, encoding="utf-8") as handle:
                doc = json.load(handle)
        except (OSError, json.JSONDecodeError) as exc:
            print(f"Could not read result JSON '{args.file}': {exc}", file=sys.stderr)
            sys.exit(1)
    else:
        try:
            doc = json.load(sys.stdin)
        except json.JSONDecodeError as exc:
            print(f"Invalid result JSON on stdin: {exc}", file=sys.stderr)
            sys.exit(1)

    root = os.path.realpath(os.getcwd())
    tracked = tracked_set()
    merge_base = None
    if args.base:
        out = git_lines(["merge-base", args.base, "HEAD"])
        merge_base = out.strip() if out else None

    survivors, suppressed, corrected = [], [], 0
    for finding in doc.get("findings", []):
        verdict, detail = check_finding(finding, root, tracked, merge_base)
        if verdict == "suppressed":
            suppressed.append({"finding": finding, "reason": detail})
            continue
        if detail is not None:
            finding = {**finding, "line": detail}
            corrected += 1
        finding["quote_check"] = "passed"
        survivors.append(finding)

    doc["findings"] = survivors
    doc["suppressed_by_validator"] = suppressed
    doc["quote_validation"] = {
        "checked": len(survivors) + len(suppressed),
        "passed": len(survivors),
        "suppressed": len(suppressed),
        "lines_corrected": corrected,
    }
    print(json.dumps(doc, indent=2))


if __name__ == "__main__":
    main()
