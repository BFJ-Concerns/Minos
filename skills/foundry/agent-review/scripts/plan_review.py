#!/usr/bin/env python3
"""Plan an agent-review run: discover briefs, resolve subtree scopes, find targets.

This is the deterministic core of the agent-review skill. It does no reviewing
itself — it works out *what* should be reviewed and *by whom*, then hands a JSON
plan back to the skill, which dispatches clean-context review agents — one per
shard, where a brief with a large file set is split across several reviewers.

Scope resolution mirrors the convention documented in SKILL.md: a brief's
position inside `.review/` determines what it reviews.

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
import subprocess
import sys


# The shared reviewer method file lives in the skill's prompts/ directory, a
# sibling of this scripts/ directory. Resolve it from THIS file's location so the
# path is correct wherever the skill is vendored (global ~/.claude or a project's
# .claude). Each reviewer agent reads it directly — see review_workflow.js — so
# its multi-KB, backtick-heavy text never has to be relayed through the workflow
# input, which is brittle for large freeform payloads.
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
PROMPTS_DIR = os.path.join(os.path.dirname(SCRIPT_DIR), "prompts")
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


def detect_pr(root):
    """Return PR metadata via gh, or None if there's no PR / gh is unavailable.

    Only the head branch's open PR is relevant; gh resolves it from the current
    branch automatically. Any failure (gh missing, not authed, no PR) collapses
    to None — the caller then falls back to a default-branch diff and chat output.
    """
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
    """Files added/modified/renamed/copied between base's merge-base and HEAD.

    Three-dot diff so we compare against the merge-base, not the literal base tip
    — the same set a reviewer cares about. Deletions are excluded: they can't host
    review comments and rarely need brief-level review.

    Returns None when the diff command itself fails — the caller must treat that
    as an error, never as an empty diff: conflating a broken git invocation with
    "nothing changed" would report a failed run as EMPTY-DIFF.
    """
    out = try_git(
        ["diff", "--name-only", "--diff-filter=ACMR", f"{base}...HEAD"],
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
    output (PR comments, issues) shows instead of the kebab-case filename, so a
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

    Returns (extent, sweep, sweep_explicit, occasions, title, warnings):
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
        name a reader sees in PR comments and issues, in place of the kebab-case
        filename.
      - warnings: a present-but-unrecognised value for extent or sweep is flagged
        rather than swallowed.
    """
    extent, sweep, warnings = "diff", "per-file", []
    sweep_explicit = False
    occasions = None
    title = None
    try:
        with open(brief_path, encoding="utf-8") as handle:
            if handle.readline().strip() != "---":
                # No frontmatter block — return the full 6-tuple so callers that
                # unpack title don't crash on the common no-frontmatter brief.
                return extent, sweep, sweep_explicit, occasions, title, warnings
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
    except OSError:
        pass
    return extent, sweep, sweep_explicit, occasions, title, warnings


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
    parser = argparse.ArgumentParser(description="Plan an agent-review run.")
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
        "briefs",
        nargs="*",
        help="Optional brief names (filename without .md) to restrict the run to. "
             "Default: every brief. Used to baseline a single new brief.",
    )
    args = parser.parse_args()

    root = repo_root()
    if root is None:
        print(json.dumps({"error": "Not inside a git repository."}))
        sys.exit(1)

    review_dir = os.path.join(root, ".review")
    if not os.path.isdir(review_dir):
        print(json.dumps({"error": f"No .review/ directory found at repo root ({root})."}))
        sys.exit(1)

    brief_paths = discover_briefs(review_dir)
    if not brief_paths:
        print(json.dumps({"error": "No .md briefs found under .review/."}))
        sys.exit(1)

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
    }

    # Optional brief selection: restrict the run to named briefs (by filename
    # stem). Used to baseline a single new brief without re-auditing the rest.
    selected = {name.lower() for name in args.briefs}
    matched = set()

    # A diff run needs the changed-file set and base ref. They feed diff-extent
    # briefs directly, and full-extent briefs use the base ref to classify each
    # finding as change-caused (→ PR) or independent (→ chat).
    changed = None
    if args.mode == "diff":
        pr = detect_pr(root)
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

    for brief_path in brief_paths:
        name = os.path.splitext(os.path.basename(brief_path))[0]
        if selected and name.lower() not in selected:
            continue
        matched.add(name.lower())

        scope, scope_warning = resolve_scope(brief_path, review_dir, root)
        if scope_warning:
            warnings.append(scope_warning)

        extent, sweep, sweep_explicit, occasions, brief_title, fm_warnings = parse_frontmatter(brief_path)
        warnings.extend(fm_warnings)
        # The human-readable review title shown in PR comments and issues. Prefer
        # the brief's declared `title`; fall back to one derived from the
        # filename so a reader never sees the kebab-case internal name.
        title = brief_title or derive_title(name)
        # --full forces a whole-scope audit of every selected brief, overriding
        # the brief's own declared extent. sweep (shardability) still applies.
        effective_extent = "full" if args.mode == "full" else extent

        rel_brief = os.path.relpath(brief_path, root)
        entry = {
            "path": rel_brief,
            "name": name,
            "title": title,              # human-readable, for outward-facing output
            "scope": scope,              # None == repo-wide
            "extent": effective_extent,  # "diff" or "full"
            "sweep": sweep,              # "per-file" or "whole-tree"
            "occasions": occasions,      # None == applies on every occasion
            "skip_reason": None,
            "skip_kind": None,           # "occasion" | "empty" | "capacity" when skipped
        }

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

    # Surface requested briefs that matched nothing; error only if none matched
    # at all, so a single typo in a multi-brief request doesn't run the wrong set.
    if selected:
        for name in sorted(selected - matched):
            warnings.append(f"Requested brief '{name}' matched no .md file under .review/.")
        if not matched:
            print(json.dumps({
                "error": "None of the requested briefs matched a .md file under .review/: "
                         + ", ".join(sorted(selected))
            }))
            sys.exit(1)

    print(json.dumps(plan, indent=2))


if __name__ == "__main__":
    main()
