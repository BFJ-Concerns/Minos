#!/usr/bin/env python3
"""Plan a review-panel run: discover aspects and briefs, resolve scopes, find targets.

This is the deterministic core of the review-panel skill. It does no reviewing
itself — it works out *what* should be reviewed and *by whom*, then hands a JSON
plan back to the skill, which dispatches clean-context review agents — one per
shard, where a concern with a large file set is split across several reviewers.

The panel plans two sources of concerns identically:

  - **Aspects** — the standard review dimensions bundled with the skill, in the
    aspects/ directory beside scripts/. They apply in any repository, repo-wide.
    Each declares a `relevance:` line; the skill decides from it whether the
    diff gives the aspect anything to do, and records a skip with
    --skip-aspect name=reason. The default is to run: an aspect is skipped only
    when it is clearly irrelevant, never on doubt.
  - **Briefs** — the repository's own standing concerns under `.review/`,
    optional. A repo without the directory still gets the full aspect panel.

A third member is planned beside them: the **Codex leg** — the Codex CLI's
built-in reviewer, run by run_codex_review.py rather than the agent fan-out.
The plan's `codex_review` entry says whether it runs or why it was skipped.

Scope resolution for briefs mirrors the convention documented in SKILL.md: a
brief's position inside `.review/` determines what it reviews.

  .review/tone.md              -> repo-wide (file sits directly in .review/)
  .review/src/api/limits.md    -> scoped to src/api/ IF that directory exists
  .review/legacy/foo.md        -> repo-wide + warning (no matching repo dir)

The match is on the *full* nested path (src/api, not just api), per the chosen
convention. A mismatch is non-fatal: the brief still runs, scoped repo-wide,
but a warning is surfaced so authors notice typos or stale folder names.
"""

import argparse
import json
import math
import os
import shutil
import subprocess
import sys

import forge


# The shared reviewer method file lives in the skill's prompts/ directory, a
# sibling of this scripts/ directory. Resolve it from THIS file's location so the
# path is correct wherever the skill is vendored (global ~/.claude or a project's
# .claude). Each reviewer agent reads it directly — see review_workflow.js — so
# its multi-KB, backtick-heavy text never has to be relayed through the workflow
# input, which is brittle for large freeform payloads.
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
PROMPTS_DIR = os.path.join(os.path.dirname(SCRIPT_DIR), "prompts")
# The bundled standard aspects — one file per review dimension, same format as a
# repo brief. They live in the installed skill, so their plan entries carry
# absolute paths (a repo brief's path is repo-relative).
ASPECTS_DIR = os.path.join(os.path.dirname(SCRIPT_DIR), "aspects")
TEMPLATE_PATH = os.path.join(PROMPTS_DIR, "reviewer-method.md")
# The other two static method files the run's later stages read the same way:
# the per-finding checker's method and the review-bar judge's method. The plan
# carries all three paths so no stage ever has to guess where the skill lives.
CHECKER_METHOD_PATH = os.path.join(PROMPTS_DIR, "finding-check.md")
BAR_METHOD_PATH = os.path.join(PROMPTS_DIR, "bar-check.md")


# --- Sharding policy -------------------------------------------------------
# A brief's review work is a set of files: every tracked file in scope (a full
# audit) or just the changed files in scope (a diff review). Handing one agent a
# very large set produces a shallow review — it skims, or runs out of budget. So
# when the set is large and the brief is shardable, the plan splits it across
# several reviewers, each handed a deterministic slice it enumerates itself (see
# review_workflow.js). That is the fan-out machinery used as intended — iterating a
# large group across agents — and it is decided here, in code, not improvised at
# runtime by a reviewer.
#
# SHARD_TARGET_SIZE is the rough upper bound on files per reviewer; at or below
# it, a single reviewer takes the brief (the long-standing behaviour for small
# briefs is unchanged). It is the one knob most worth tuning after a live trial:
# dense source may want fewer files per reviewer, light test files more.
SHARD_TARGET_SIZE = 30
# A ceiling on reviewers per brief, so a pathological repo-wide sweep can't fan
# out without bound. Past this, slices grow beyond SHARD_TARGET_SIZE rather than
# adding agents — coverage stays complete, the agents just get fatter. Set well
# above the runtime's per-engine concurrency cap so ordinary large sweeps still parallelise
# fully; it only bites on enormous scopes.
MAX_SHARDS_PER_BRIEF = 24
# The most files a single whole-view reviewer is asked to hold. A whole-tree
# brief can't be split — its concern needs the whole scope in one view — so
# beyond this the reviewer would skim and the review would look complete while
# being shallow. The plan refuses to dispatch it (not-run, with the measured
# size) rather than let that happen; the fixes are to scope the brief to a
# subtree or, if it is really a per-file concern, declare `sweep: per-file`.
WHOLE_VIEW_CAPACITY = 150

# The Codex leg — the Codex CLI's built-in reviewer, run beside the agent
# fan-out by run_codex_review.py. It shares the name namespace with aspects and
# briefs so it can be selected or excluded like any other concern.
CODEX_LEG_NAME = "codex-review"
CODEX_LEG_TITLE = "Codex Review"


def plan_codex_leg(args, selected, outcome):
    """The plan's codex_review entry: planned, or skipped with the reason.

    The leg reviews the branch diff with `codex review --base <ref>`, so it is
    diff-mode only, and it needs the codex CLI on the machine. Every exclusion
    is recorded as a skip with its reason — an unchecked concern, never a
    silent absence — mirroring how aspects and briefs are skipped.
    """
    entry = {
        "name": CODEX_LEG_NAME,
        "title": CODEX_LEG_TITLE,
        "kind": "external",
        "skip_reason": None,
        "skip_kind": None,
    }
    if args.no_codex:
        entry["skip_reason"] = "Excluded by --no-codex."
        entry["skip_kind"] = "flag"
    elif selected and CODEX_LEG_NAME not in selected:
        entry["skip_reason"] = "Not among the names this run was restricted to."
        entry["skip_kind"] = "flag"
    elif args.mode == "full":
        entry["skip_reason"] = (
            "codex review reviews a branch diff; a full audit has no diff to hand it."
        )
        entry["skip_kind"] = "mode"
    elif outcome == "empty-diff":
        entry["skip_reason"] = "No changed files between the base and HEAD."
        entry["skip_kind"] = "empty"
    elif shutil.which("codex") is None:
        entry["skip_reason"] = "codex CLI not found on PATH."
        entry["skip_kind"] = "unavailable"
    return entry


def run_git(args, cwd):
    """Run a git command, returning stdout stripped. Raises on failure."""
    result = subprocess.run(
        ["git", *args],
        cwd=cwd,
        capture_output=True,
        text=True,
        check=True,
    )
    return result.stdout.strip()


def try_git(args, cwd):
    """Run a git command, returning stdout or None if it fails."""
    try:
        return run_git(args, cwd)
    except subprocess.CalledProcessError:
        return None


def repo_root():
    """Absolute path to the enclosing git work tree, or None if not in one."""
    try:
        return run_git(["rev-parse", "--show-toplevel"], cwd=os.getcwd())
    except subprocess.CalledProcessError:
        return None


def detect_pr(root, warnings):
    """Return the current branch's open PR, or None if there is none / the
    forge is unreachable.

    Forge-keyed on the origin remote (see forge.py): github.com goes through
    `gh`, anything else through the Forgejo API. Any failure — tool missing,
    not authed, no PR — collapses to None and the caller falls back to a
    default-branch diff and chat output. The one failure that gets a warning
    is missing Forgejo credentials: silently degrading to "no PR" there would
    read as a clean chat-only run when the fix is a one-line config entry.
    """
    remote = forge.detect_remote(root)
    if remote is not None and remote.kind == "forgejo":
        return _detect_pr_forgejo(root, remote, warnings)
    return _detect_pr_github(root)


def _detect_pr_github(root):
    """PR metadata via gh, which resolves the current branch's PR itself."""
    try:
        out = subprocess.run(
            ["gh", "pr", "view", "--json", "number,baseRefName,state"],
            cwd=root,
            capture_output=True,
            text=True,
            check=True,
        ).stdout
    except (subprocess.CalledProcessError, FileNotFoundError):
        return None
    try:
        data = json.loads(out)
    except json.JSONDecodeError:
        return None
    if not data.get("number"):
        return None
    return {
        "number": data["number"],
        "base_ref": f"origin/{data['baseRefName']}",
        "base_branch": data["baseRefName"],
    }


def _detect_pr_forgejo(root, remote, warnings):
    """PR metadata via the Forgejo API: the open PR whose head is this branch."""
    instance, reason = forge.load_instance(remote.host)
    if instance is None:
        warnings.append(f"Cannot check for a PR: {reason}")
        return None
    branch = try_git(["branch", "--show-current"], cwd=root)
    if not branch:
        return None  # detached HEAD — nothing to match a PR head against
    client = forge.ForgejoClient(instance, remote.owner, remote.repo)
    pulls, _capped, err = client.paged("pulls", params={"state": "open"})
    if pulls is None:
        warnings.append(f"Cannot check for a PR: {err}")
        return None
    for pull in pulls:
        head = (pull.get("head") or {}).get("ref")
        if head == branch and pull.get("number"):
            base_branch = (pull.get("base") or {}).get("ref") or "main"
            return {
                "number": pull["number"],
                "base_ref": f"origin/{base_branch}",
                "base_branch": base_branch,
            }
    return None


def default_base(root):
    """Best guess at the base ref to diff against when there's no PR."""
    # origin/HEAD points at the remote's default branch when it's been set.
    head = try_git(["symbolic-ref", "refs/remotes/origin/HEAD"], cwd=root)
    if head:
        return head.replace("refs/remotes/", "")  # e.g. origin/main
    for candidate in ("origin/main", "origin/master", "main", "master"):
        if try_git(["rev-parse", "--verify", "--quiet", candidate], cwd=root) is not None:
            return candidate
    return None


def reviewable(paths):
    """Drop the briefs themselves — `.review/` is configuration, not code to audit."""
    return [p for p in paths if not p.startswith(".review/")]


def tracked_files(root, scope):
    """All git-tracked files within a scope (or the whole repo), respecting .gitignore.

    Used to size a full-extent brief's scope: the count drives how many reviewers
    the brief is split across (see the sharding policy). The reviewers enumerate
    their own slices with the same command, so the list itself is never relayed.
    """
    args = ["ls-files"]
    if scope:
        args += ["--", scope]
    out = try_git(args, cwd=root)
    if out is None:
        return []
    return reviewable(line for line in out.splitlines() if line.strip())


def changed_files(root, base):
    """Files added/modified/renamed/copied/deleted between base's merge-base and HEAD.

    Three-dot diff so we compare against the merge-base, not the literal base tip
    — the same set a reviewer cares about. Deletions are included: removing a
    check, a registration, a cleanup step, or a test is as reviewable as adding
    one, and a deleted file that never enters a reviewer's set is a silent
    coverage gap. Reviewers inspect a deleted file through the diff and cite its
    removed lines with side: LEFT (which the quote validator checks against the
    merge-base, since the file no longer exists in the working tree).

    Returns None when the diff command itself fails — the caller must treat that
    as an error, never as an empty diff: conflating a broken git invocation with
    "nothing changed" would report a failed run as EMPTY-DIFF.
    """
    out = try_git(
        ["diff", "--name-only", "--diff-filter=ACMRD", f"{base}...HEAD"],
        cwd=root,
    )
    if out is None:
        return None
    return reviewable(line for line in out.splitlines() if line.strip())


def discover_briefs(review_dir):
    """All markdown briefs under .review/, as paths relative to the repo root's parent."""
    briefs = []
    for current, _dirs, files in os.walk(review_dir):
        for name in sorted(files):
            if name.lower().endswith(".md"):
                briefs.append(os.path.join(current, name))
    return sorted(briefs)


def resolve_scope(brief_path, review_dir, root):
    """Map a brief to its subtree scope.

    Returns (scope, warning) where scope is a repo-relative directory path or
    None for repo-wide. warning is a human-readable string or None.
    """
    rel = os.path.relpath(brief_path, review_dir)
    scope_dir = os.path.dirname(rel)  # "" for a brief sitting directly in .review/
    if not scope_dir:
        return None, None
    scope_dir = scope_dir.replace(os.sep, "/")
    if os.path.isdir(os.path.join(root, scope_dir)):
        return scope_dir, None
    warning = (
        f"Brief '{rel}' is under '.review/{scope_dir}/', but the repo has no "
        f"'{scope_dir}/' directory. Treating it as repo-wide. "
        f"Fix the folder name to scope it, or move the brief if this is intentional."
    )
    return None, warning


def in_scope(path, scope):
    """Does a changed file fall within a brief's scope? None scope means repo-wide."""
    if scope is None:
        return True
    return path == scope or path.startswith(scope + "/")


def derive_title(name):
    """A human-readable review title from a brief's filename stem.

    Used as the fallback when a brief declares no `title` in its frontmatter:
    `error-tone` becomes "Error Tone". Best-effort — hyphens (and underscores)
    become spaces and each word is capitalised, so an acronym like `api` renders
    as "Api". A brief that wants exact casing should set `title:` in its
    frontmatter; this is only the fallback. The title is what outward-facing
    output (PR comments) shows instead of the kebab-case filename, so a
    reader never sees the internal brief name.
    """
    words = [w for w in name.replace("_", "-").split("-") if w]
    return " ".join(word.capitalize() for word in words) or name


def parse_frontmatter(brief_path):
    """Read a brief's optional frontmatter for the keys this skill honours.

    Briefs are prose; frontmatter is optional. We parse only the keys we need by
    hand, so there's no YAML dependency and a malformed block can't break the run.

    Undeclared sweep defaults to per-file — most review concerns judge each file
    on its own merits, so the common case splits and scales. An undeclared
    occasion means the brief applies on every occasion.

    Returns (extent, sweep, sweep_explicit, occasions, title, relevance, warnings):
      - extent: "diff" (default) or "full" — how much of the scope to read.
        `extent: full` audits the whole scope every run; `diff` (or no
        frontmatter) reviews only what the branch changed.
      - sweep: "per-file" (default) or "whole-tree" — whether the brief can be
        split across reviewers. A per-file brief judges each file on its own, so
        a large set is shared across several reviewers; a whole-tree brief needs
        the whole scope in one view and is never split.
      - sweep_explicit: True only when the brief actually declared `sweep`. A
        full-extent brief that did not declare it defaults to per-file and so is
        split when large; the caller uses this to nudge the author toward
        `sweep: whole-tree` if that split would be wrong (a cross-file invariant
        written before `sweep` existed, say).
      - occasions: None when the brief applies on every occasion (the default),
        else the list of occasion tokens it declared — the run-conditions on
        which the brief applies. The caller matches these against the occasion
        the run names (`--occasion`).
      - title: the brief's declared human-readable review title, or None when it
        set none (the caller then derives one from the filename). This is the
        name a reader sees in PR comments, in place of the kebab-case filename.
      - relevance: the declared `relevance:` line, or None. Aspects declare it —
        it is the condition the skill weighs before recording a --skip-aspect —
        and it rides into the plan so the skill can weigh it without re-reading
        the file.
      - warnings: a present-but-unrecognised value for extent or sweep is flagged
        rather than swallowed.
    """
    extent, sweep, warnings = "diff", "per-file", []
    sweep_explicit = False
    occasions = None
    title = None
    relevance = None
    try:
        with open(brief_path, encoding="utf-8") as handle:
            if handle.readline().strip() != "---":
                # No frontmatter block — return the full 7-tuple so callers that
                # unpack title don't crash on the common no-frontmatter brief.
                return extent, sweep, sweep_explicit, occasions, title, relevance, warnings
            for line in handle:
                if line.strip() == "---":
                    break
                key, sep, value = line.partition(":")
                if not sep:
                    continue
                # Strip an inline YAML comment so a documented example copied
                # verbatim (`extent: full   # …`) still parses; the extent/sweep/
                # occasion values never legitimately contain '#'.
                key, raw = key.strip(), value.split("#", 1)[0].strip()
                if key == "extent":
                    if raw.lower() in ("diff", "full"):
                        extent = raw.lower()
                    else:
                        warnings.append(
                            f"Brief '{os.path.basename(brief_path)}' has an unrecognised "
                            f"extent '{raw}'; expected 'diff' or 'full'. Defaulting to diff."
                        )
                elif key == "sweep":
                    if raw.lower() in ("per-file", "whole-tree"):
                        sweep = raw.lower()
                        sweep_explicit = True
                    else:
                        warnings.append(
                            f"Brief '{os.path.basename(brief_path)}' has an unrecognised "
                            f"sweep '{raw}'; expected 'per-file' or 'whole-tree'. "
                            f"Defaulting to per-file."
                        )
                elif key == "occasion":
                    # Occasion tokens are author-defined (e.g. `merge`, `nightly`,
                    # `release`), one or comma-separated. A blank value is treated
                    # as undeclared: the brief applies on every occasion.
                    tokens = [t.strip().lower() for t in raw.split(",") if t.strip()]
                    if tokens:
                        occasions = tokens
                elif key == "title":
                    # Title is freeform display text, so take the whole value —
                    # no inline-comment stripping (a title may legitimately
                    # contain '#') — and drop surrounding quotes. A blank title is
                    # ignored so the caller falls back to deriving one.
                    candidate = value.strip().strip("\"'").strip()
                    if candidate:
                        title = candidate
                elif key == "relevance":
                    # Freeform prose, like title: take the whole value.
                    candidate = value.strip()
                    if candidate:
                        relevance = candidate
    except OSError:
        pass
    return extent, sweep, sweep_explicit, occasions, title, relevance, warnings


def plan_shards(file_set_size, shardable):
    """How many reviewers should cover a set of `file_set_size` files.

    Returns 1 when the brief is not shardable (a whole-tree brief, judged as a
    unit) or the set is small enough for one reviewer. Otherwise it is
    ceil(size / SHARD_TARGET_SIZE), capped at MAX_SHARDS_PER_BRIEF.
    """
    if not shardable or file_set_size <= SHARD_TARGET_SIZE:
        return 1
    return min(math.ceil(file_set_size / SHARD_TARGET_SIZE), MAX_SHARDS_PER_BRIEF)


def shard_ranges(file_set_size, shard_count):
    """Split 1..file_set_size into `shard_count` balanced, contiguous 1-based ranges.

    Sizes differ by at most one, so there is no straggler shard left with the
    remainder. Each (start, end) is inclusive, and the ranges tile 1..size
    exactly — every file lands in one shard, none in two — which is what makes
    coverage a property of this arithmetic rather than of an agent's counting. A
    reviewer turns its range into files with `… | sed -n 'start,endp'`.
    """
    base, remainder = divmod(file_set_size, shard_count)
    ranges = []
    start = 1
    for index in range(shard_count):
        # The first `remainder` shards take one extra file to absorb the remainder.
        size = base + (1 if index < remainder else 0)
        end = start + size - 1
        ranges.append((start, end))
        start = end + 1
    return ranges


def build_shards(file_set_size, shardable, extent, target_files):
    """Per-reviewer shard descriptors for one brief — one shard per reviewer.

    Three shapes, matching how the reviewer finds its files (see
    review_workflow.js):
      - {index, total, files}  a single diff reviewer gets the explicit changed
        files inline — the set is small (bounded by the diff), and handing it
        over stops the agent re-deriving a different set with different git flags.
      - {index, total, whole: True}  a single full reviewer enumerates the whole
        scope itself; the list is deliberately not relayed (it can be large, and
        relaying a big list through the workflow input is what crashed past runs).
      - {index, total, start, end}  a sharded reviewer (diff or full) enumerates
        deterministically and cuts its own 1-based line range; no list relayed.
    """
    count = plan_shards(file_set_size, shardable)
    if count == 1:
        if extent == "diff":
            return [{"index": 1, "total": 1, "files": target_files}]
        return [{"index": 1, "total": 1, "whole": True}]
    return [
        {"index": i + 1, "total": count, "start": start, "end": end}
        for i, (start, end) in enumerate(shard_ranges(file_set_size, count))
    ]


def main():
    parser = argparse.ArgumentParser(description="Plan a review-panel run.")
    parser.add_argument(
        "--mode",
        choices=["diff", "full"],
        default="diff",
        help="diff: honour each brief's extent against the branch changes (default). "
             "full: force every selected brief to audit its whole scope.",
    )
    parser.add_argument(
        "--full",
        action="store_const",
        const="full",
        dest="mode",
        help="Shorthand for --mode full.",
    )
    parser.add_argument(
        "--base",
        metavar="REF",
        default=None,
        help="Branch or ref to diff the current branch against (e.g. origin/develop, "
             "a tag, or a commit). Overrides PR detection and the auto-detected "
             "default branch. Diff mode only; ignored by --full.",
    )
    parser.add_argument(
        "--occasion",
        metavar="NAME",
        default=None,
        help="The occasion this run is for (e.g. merge, nightly, release). A brief "
             "that declares `occasion:` in its frontmatter runs only when this "
             "matches one of its tokens; a brief declaring none always runs.",
    )
    parser.add_argument(
        "--skip-aspect",
        action="append",
        default=[],
        metavar="NAME=REASON",
        help="Skip a bundled aspect as not relevant to this diff, recording the "
             "stated reason. Repeatable. The skill grants a skip only when the "
             "diff clearly gives the aspect nothing to do; the reason is "
             "reported with the run's other skips.",
    )
    parser.add_argument(
        "--no-aspects",
        action="store_true",
        help="Plan only the repository's .review/ briefs, without the bundled "
             "standard aspects.",
    )
    parser.add_argument(
        "--no-briefs",
        action="store_true",
        help="Plan only the bundled standard aspects, without the repository's "
             ".review/ briefs.",
    )
    parser.add_argument(
        "--no-codex",
        action="store_true",
        help="Plan without the Codex leg (the Codex CLI's built-in review, "
             "run beside the agent fan-out on every diff run).",
    )
    parser.add_argument(
        "briefs",
        nargs="*",
        help="Optional aspect/brief names (filename without .md) to restrict the "
             "run to. Default: everything. Used to baseline a single new brief "
             "or run one aspect alone.",
    )
    args = parser.parse_args()

    root = repo_root()
    if root is None:
        print(json.dumps({"error": "Not inside a git repository."}))
        sys.exit(1)

    # Two sources: the bundled aspects ship with the skill (a missing directory
    # is a broken install), while a repo's .review/ is optional — a repo with no
    # briefs still gets the standard panel.
    aspect_paths = [] if args.no_aspects else (
        discover_briefs(ASPECTS_DIR) if os.path.isdir(ASPECTS_DIR) else None
    )
    if aspect_paths is None:
        print(json.dumps({"error": f"Bundled aspects directory not found at {ASPECTS_DIR} — broken skill install."}))
        sys.exit(1)

    review_dir = os.path.join(root, ".review")
    brief_paths = [] if args.no_briefs else (
        discover_briefs(review_dir) if os.path.isdir(review_dir) else []
    )
    if not aspect_paths and not brief_paths:
        # The Codex leg is a third plannable member: a diff run that still has
        # it — not excluded, CLI present — proceeds as a leg-only run rather
        # than refusing. Only when the leg too is out is there nothing to plan.
        codex_can_run = (
            not args.no_codex and args.mode == "diff" and shutil.which("codex") is not None
        )
        if not codex_can_run:
            print(json.dumps({"error":
                "Nothing to plan: the flags exclude the bundled aspects, this repo "
                "has no .review/ briefs, and the Codex leg is excluded or "
                "unavailable."}))
            sys.exit(1)

    # --skip-aspect name=reason — parsed up front so a malformed value fails the
    # run before anything is planned. A reasonless skip is refused: the reason is
    # what the run reports in place of the unchecked concern.
    skip_aspects = {}
    if args.skip_aspect and args.mode == "full":
        # Relevance is a property of a diff; a full audit has no diff to be
        # irrelevant to, so every selected aspect runs.
        print(json.dumps({"error": "--skip-aspect is a diff-mode flag; a full audit runs every selected aspect."}))
        sys.exit(1)
    for raw in args.skip_aspect:
        name, sep, reason = raw.partition("=")
        if not sep or not name.strip() or not reason.strip():
            print(json.dumps({"error": f"--skip-aspect expects NAME=REASON, got '{raw}'."}))
            sys.exit(1)
        key = name.strip().lower()
        # The correctness invariant is enforced here, in code, not left to the
        # orchestrator's judgement: correctness runs on every diff.
        if key == "correctness":
            print(json.dumps({"error": "The correctness aspect always runs; --skip-aspect cannot skip it."}))
            sys.exit(1)
        skip_aspects[key] = reason.strip()

    # The three static method files must exist — reviewers, checkers, and the
    # bar judge each read theirs. A missing file is a broken skill install, not
    # a repo problem; fail clearly here rather than letting agents fail to read
    # them later.
    for label, path in (
        ("Reviewer method", TEMPLATE_PATH),
        ("Finding-check method", CHECKER_METHOD_PATH),
        ("Bar-check method", BAR_METHOD_PATH),
    ):
        if not os.path.isfile(path):
            print(json.dumps({"error": f"{label} file not found at {path}."}))
            sys.exit(1)

    warnings = []
    plan = {
        "repo_root": root,
        "template_path": TEMPLATE_PATH,
        "checker_method_path": CHECKER_METHOD_PATH,
        "bar_method_path": BAR_METHOD_PATH,
        "mode": args.mode,
        "occasion": args.occasion,
        # "planned" for a run with work to do; "empty-diff" is the distinct
        # terminal outcome for a diff run whose changed set is empty — reported
        # as such, never as a clean pass (see below).
        "outcome": "planned",
        "pr": None,
        "base_ref": None,
        "base_source": None,  # "explicit" | "pr" | "default" — set in diff mode
        "merge_base": None,   # sha the three-dot diff compares against, diff mode
        "warnings": warnings,
        "briefs": [],
        "codex_review": None,
    }

    # Optional selection: restrict the run to named aspects/briefs. Used to
    # baseline a single new brief or run one aspect alone. Explicit selection is
    # authoritative — an aspect the user asked for by name is never
    # relevance-skipped.
    selected = {name.lower() for name in args.briefs}
    matched = set()
    named_and_skipped = sorted(selected & set(skip_aspects))
    if named_and_skipped:
        print(json.dumps({"error":
            "Explicitly selected aspect(s) cannot be relevance-skipped: "
            + ", ".join(named_and_skipped)}))
        sys.exit(1)
    if args.no_codex and CODEX_LEG_NAME in selected:
        print(json.dumps({"error":
            f"'{CODEX_LEG_NAME}' was selected by name and excluded by --no-codex; "
            "drop one of the two."}))
        sys.exit(1)

    # A diff run needs the changed-file set and base ref. They feed diff-extent
    # briefs directly, and full-extent briefs use the base ref to classify each
    # finding as change-caused (→ PR) or independent (→ chat).
    changed = None
    if args.mode == "diff":
        pr = detect_pr(root, warnings)
        # Base resolution, most explicit first: an explicit --base wins outright;
        # otherwise a detected PR's base; otherwise a guess at the default branch.
        # We record which, so the skill can present a *guessed* base as provisional
        # (and point the user at --base) rather than reporting it as authoritative —
        # the failure that hurts most is a wrong base reading as "nothing changed".
        if args.base:
            base, base_source = args.base, "explicit"
        elif pr:
            base, base_source = pr["base_ref"], "pr"
        else:
            base, base_source = default_base(root), "default"

        if base is None:
            print(json.dumps({
                "error": "Could not determine a branch to diff against — no PR, and no "
                         "origin/HEAD, main, or master found. Pass --base <ref> to name "
                         "the branch this work forks from, or run with --full."
            }))
            sys.exit(1)
        # An explicit --base that doesn't resolve is a user error worth catching
        # loudly, rather than letting `git diff` yield a confusing empty result.
        if base_source == "explicit" and try_git(
            ["rev-parse", "--verify", "--quiet", base], cwd=root
        ) is None:
            print(json.dumps({
                "error": f"--base '{base}' is not a valid ref in this repository."
            }))
            sys.exit(1)

        plan["pr"] = pr
        plan["base_ref"] = base
        plan["base_source"] = base_source
        # The merge-base is what the three-dot diff actually compares against;
        # the quote validator uses it to check side: LEFT quotes against the
        # version the reviewers saw removed. None when it can't be resolved.
        plan["merge_base"] = try_git(["merge-base", base, "HEAD"], cwd=root)
        changed = changed_files(root, base)
        if changed is None:
            # The diff command failed — a broken base, a corrupt repo. This is
            # an error, never an empty diff: reporting it as EMPTY-DIFF would
            # dress a failed run as a reviewed one.
            print(json.dumps({
                "error": f"git diff against '{base}' failed; cannot plan a diff run."
            }))
            sys.exit(1)
        if not changed:
            # EMPTY-DIFF: the branch changed nothing. Diff-extent briefs have
            # nothing to review and are skipped below; full-extent briefs audit
            # their whole scope regardless of the diff, so they still plan and
            # dispatch. The outcome must never be reported as a clean pass —
            # with a guessed base it as often means the wrong base as a clean
            # branch — so it is flagged for the skill to report as terminal for
            # the branch-review portion.
            hint = (
                " (base auto-detected; pass --base <ref> if that's the wrong branch)"
                if base_source == "default"
                else ""
            )
            warnings.append(f"No changed files between {base} and HEAD{hint}.")
            plan["outcome"] = "empty-diff"

    # The Codex leg is planned alongside the concerns: run on a default diff
    # run, or skipped with a recorded reason. Selecting it by name counts as a
    # match, so `/review-panel codex-review` runs the leg alone without the
    # "no names matched" error firing.
    plan["codex_review"] = plan_codex_leg(args, selected, plan["outcome"])
    if CODEX_LEG_NAME in selected:
        matched.add(CODEX_LEG_NAME)

    # Aspects plan first, then briefs, through the same pipeline. Entries carry
    # `kind` so downstream stages can tell them apart; an aspect's `path` is
    # absolute (it lives in the installed skill), a brief's is repo-relative.
    # Names are assigned for every source up front, before selection, so a
    # concern's identity is stable whatever is selected or excluded. The
    # collision namespace is the full bundled-aspect set even under
    # --no-aspects, so the same brief never changes name across flags. A
    # colliding brief takes a name derived from its .review/ path (unique by
    # construction — two briefs cannot share a path), with a plain '-brief'
    # suffix for the root-level case.
    aspect_stems = {
        os.path.splitext(os.path.basename(path))[0].lower()
        for path in (discover_briefs(ASPECTS_DIR) if os.path.isdir(ASPECTS_DIR) else [])
    }
    sources = [("aspect", path, os.path.splitext(os.path.basename(path))[0])
               for path in aspect_paths]
    # The Codex leg's name is reserved alongside the aspect stems, so a repo
    # brief named codex-review is renamed (with a warning) instead of silently
    # sharing an identity with the leg.
    taken = set(aspect_stems) | {CODEX_LEG_NAME}
    for path in brief_paths:
        stem = os.path.splitext(os.path.basename(path))[0]
        name = stem
        if name.lower() in taken:
            rel = os.path.splitext(os.path.relpath(path, review_dir))[0]
            slug = "-".join(part for part in rel.replace(os.sep, "/").split("/") if part)
            name = slug if slug.lower() not in taken and slug != stem else f"{slug}-brief"
            warnings.append(
                f"Brief '{stem}' ({os.path.relpath(path, root)}) collides with another "
                f"concern's name; planned as '{name}'. Rename the brief to avoid this."
            )
        taken.add(name.lower())
        sources.append(("brief", path, name))

    for kind, brief_path, name in sources:
        if selected and name.lower() not in selected:
            continue
        matched.add(name.lower())

        if kind == "aspect":
            scope = None  # aspects always review repo-wide within the diff
        else:
            scope, scope_warning = resolve_scope(brief_path, review_dir, root)
            if scope_warning:
                warnings.append(scope_warning)

        extent, sweep, sweep_explicit, occasions, brief_title, relevance, fm_warnings = parse_frontmatter(brief_path)
        warnings.extend(fm_warnings)
        # The human-readable review title shown in PR comments and issues. Prefer
        # the brief's declared `title`; fall back to one derived from the
        # filename so a reader never sees the kebab-case internal name.
        title = brief_title or derive_title(name)
        # --full forces a whole-scope audit of every selected brief, overriding
        # the brief's own declared extent. sweep (shardability) still applies.
        effective_extent = "full" if args.mode == "full" else extent

        entry = {
            # A brief's path is repo-relative; an aspect's is absolute, because
            # it lives in the installed skill, not the reviewed repository.
            "path": brief_path if kind == "aspect" else os.path.relpath(brief_path, root),
            "kind": kind,                # "aspect" (bundled) or "brief" (.review/)
            "name": name,
            "title": title,              # human-readable, for outward-facing output
            "scope": scope,              # None == repo-wide
            "extent": effective_extent,  # "diff" or "full"
            "sweep": sweep,              # "per-file" or "whole-tree"
            "occasions": occasions,      # None == applies on every occasion
            "relevance": relevance,      # aspects: the declared relevance condition
            "skip_reason": None,
            "skip_kind": None,           # "relevance" | "occasion" | "empty" | "capacity"
        }

        # A granted relevance skip: the skill judged the diff clearly gives this
        # aspect nothing to do, and said why. Recorded like every other skip —
        # an unchecked concern, never a silent absence.
        if kind == "aspect" and name.lower() in skip_aspects:
            entry["skip_reason"] = f"Skipped as not relevant to this diff: {skip_aspects.pop(name.lower())}"
            entry["skip_kind"] = "relevance"
            entry["file_set_size"] = None
            plan["briefs"].append(entry)
            continue

        # Occasion selection: a brief that declares occasions runs only when the
        # run names one of them. A skipped brief is an unchecked concern, so it
        # stays in the plan with its reason rather than silently vanishing.
        if occasions is not None:
            run_occasion = (args.occasion or "").lower()
            if run_occasion not in occasions:
                named = f"this run is for '{args.occasion}'" if args.occasion else "this run named none"
                entry["skip_reason"] = (
                    f"Applies only on occasion(s) {', '.join(occasions)}; {named}."
                )
                entry["skip_kind"] = "occasion"
                entry["file_set_size"] = None
                plan["briefs"].append(entry)
                continue

        scope_label = scope or "the whole repo"
        # The set of files this brief reviews: every tracked file in scope for a
        # full audit, or just the changed files in scope for a diff review. We
        # size that set here; the reviewers enumerate the actual paths themselves
        # (full extent, or any sharded brief) or get the small changed list inline
        # (an unsharded diff brief) — see build_shards and review_workflow.js.
        if effective_extent == "full":
            target_files = None
            file_set_size = len(tracked_files(root, scope))
            empty_reason = f"No tracked files in scope ({scope_label})."
        else:
            target_files = [f for f in changed if in_scope(f, scope)]
            file_set_size = len(target_files)
            empty_reason = f"No changed files in scope ({scope_label})."

        entry["file_set_size"] = file_set_size
        entry["sweep_advisory"] = False
        if file_set_size == 0:
            entry["skip_reason"] = empty_reason
            entry["skip_kind"] = "empty"
        elif sweep == "whole-tree" and file_set_size > WHOLE_VIEW_CAPACITY:
            # Whole-view capacity check: this brief needs its whole scope in one
            # view, and the scope is bigger than one reviewer can genuinely hold.
            # Refuse loudly (not-run, with the measured size) rather than dispatch
            # a reviewer that would skim and look complete.
            entry["skip_reason"] = (
                f"Not run: {file_set_size} files in scope ({scope_label}) exceed the "
                f"{WHOLE_VIEW_CAPACITY}-file whole-view capacity of a single reviewer, "
                f"and this brief declares sweep: whole-tree, so it cannot be split. "
                f"Scope the brief to a subtree, or declare `sweep: per-file` if each "
                f"file can be judged on its own."
            )
            entry["skip_kind"] = "capacity"
        else:
            # A per-file brief can be split across reviewers when the set is large;
            # a whole-tree brief must be judged as a unit, so it is never split.
            shardable = sweep == "per-file"
            entry["shards"] = build_shards(
                file_set_size, shardable, effective_extent, target_files
            )
            # A brief that *declares* full extent but no sweep defaults to per-file
            # and so is split when large. If it is really a cross-file invariant
            # (authored before `sweep` existed, say), that split is wrong — flag it
            # once it is actually split, so the skill can nudge the author toward
            # `sweep: whole-tree`. Keyed on the brief's own declared extent, not a
            # --full override, so a plain diff brief forced --full isn't nagged.
            entry["sweep_advisory"] = (
                extent == "full" and not sweep_explicit and len(entry["shards"]) > 1
            )

        plan["briefs"].append(entry)

    # A --skip-aspect that matched no planned aspect: the skip did NOT happen, so
    # say so — the conservative direction is that the aspect runs.
    for name in sorted(skip_aspects):
        warnings.append(
            f"--skip-aspect '{name}' matched no bundled aspect; nothing was skipped."
        )

    # Surface requested names that matched nothing; error only if none matched
    # at all, so a single typo in a multi-name request doesn't run the wrong set.
    if selected:
        for name in sorted(selected - matched):
            warnings.append(f"Requested name '{name}' matched no bundled aspect or .review/ brief.")
        if not matched:
            print(json.dumps({
                "error": "None of the requested names matched a bundled aspect or a .md brief under .review/: "
                         + ", ".join(sorted(selected))
            }))
            sys.exit(1)

    print(json.dumps(plan, indent=2))


if __name__ == "__main__":
    main()
