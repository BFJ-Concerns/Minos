#!/usr/bin/env python3
"""Publish collated agent-review findings to GitHub.

Reads one findings JSON document — from a file-path argument when given, else
stdin — and routes its findings two ways:

  - **Change-introduced findings** (no `preexisting: true`) describe problems the
    branch caused. They go to the *pull request*: as one PR review with an inline
    comment per finding on the `gh` path, or as one consolidated comment on the
    connector path.
  - **Pre-existing findings** (`preexisting: true`) are violations the change did
    not introduce. They are independent of the PR, so each becomes its own GitHub
    *issue* rather than cluttering the review. Issues are de-duplicated against
    open ones so re-running on the same PR does not pile up copies.

Two write paths, chosen automatically — the skill keeps `gh` as its primary
mechanism and only the *write* differs:

  - **`gh` path (default).** When `gh` can write to the repo, this script posts the
    review and creates the issues itself.
  - **Connector path.** When `gh` cannot write — the Claude Code Web routine
    environment, where `gh` is read-only and writes must go through the GitHub
    connector — this script writes nothing. It instead prints a `render` payload:
    the finished comment markdown and ready-to-create issue bodies, for the
    calling skill to post with whatever GitHub connector tools it has. `gh` reads
    still work there, so issue de-duplication is done here regardless of path.

Write capability is detected up front with a read-only permissions probe, with a
write-failure check as the backstop (the precise way web blocks writes is
undocumented, so the attempt is the only fully reliable signal). `--render` forces
the connector payload without attempting any write — used for tests and when the
caller already knows it is on the connector path.

All outward-facing text is generated here, verbatim, from the findings — never
by the model — so internal process vocabulary (the fan-out, file slices, reviewer
indices, kebab-case brief filenames) cannot leak into a PR. Reviews and issues are
labelled by each brief's human-readable `review_title`.

Expected document shape:

  {
    "pr": 123,
    "head_sha": "abc123...",
    "title": "Code Review",          # optional; the review/comment heading
    "summary": "One-line intro.",    # optional
    "findings": [
      {"brief": "error-tone", "review_title": "Error Message Tone",
       "scope": "src/api", "file": "src/api/x.ts", "line": 42, "side": "RIGHT",
       "priority": "P1", "title": "Short imperative summary",
       "message": "...", "code_quote": "...", "suggestion": "...",
       "preexisting": false}
    ]
  }
"""

import argparse
import hashlib
import json
import re
import shutil
import subprocess
import sys

# Lower number sorts first (most urgent). Anything unrecognised sorts last.
PRIORITY_ORDER = {"P0": 0, "P1": 1, "P2": 2, "P3": 3}

# shields.io badge colour per priority. Used only on the gh path's inline
# comments — one badge per anchored comment. The consolidated comment and issue
# bodies use plain priority text instead: dozens of badge images in one body all
# fetch through GitHub's image proxy, which is slow and flaky at that volume.
PRIORITY_COLOUR = {"P0": "red", "P1": "orange", "P2": "yellow", "P3": "blue"}

# Open issues scanned for the de-dup marker. A repo with more open issues than
# this risks a missed duplicate; the cap is reported so a silent gap is visible.
ISSUE_SCAN_LIMIT = 200

# Embedded in every issue body and matched on re-runs to avoid creating the same
# issue twice. The key is a short hash of the finding; see dedup_key.
MARKER_RE = re.compile(r"agent-review-key:\s*([0-9a-f]{6,40})")


# --- GitHub plumbing -------------------------------------------------------

def gh(args, payload=None):
    """Run a gh command. Returns (ok, stdout, error_text).

    `payload`, if given, is JSON-encoded and piped to stdin (for `gh api
    --input -`). A missing gh binary collapses to (False, "", reason) rather
    than raising, so the caller can fall back to the connector path.
    """
    try:
        result = subprocess.run(
            ["gh", *args],
            input=(json.dumps(payload) if payload is not None else None),
            capture_output=True,
            text=True,
        )
    except FileNotFoundError:
        return False, "", "gh CLI not found on PATH."
    if result.returncode != 0:
        return False, result.stdout, (result.stderr.strip() or result.stdout.strip())
    return True, result.stdout, ""


def gh_present():
    return shutil.which("gh") is not None


def is_permission_failure(err):
    """True when a gh write failed because gh cannot write here at all — a
    read-only token or the read-only web routine — as opposed to a content
    rejection (e.g. an inline comment off the diff) or a transient error.

    Only this distinction drives control flow: a permission failure means fall
    back to the connector path, where any other failure with inline comments is
    retried body-only (see post_via_gh). The auth/permission family is the 4xx
    access codes plus GitHub's stock "not accessible" / "requires authentication"
    phrasing.
    """
    text = err or ""
    lower = text.lower()
    if any(code in text for code in ("HTTP 401", "HTTP 403", "HTTP 404")):
        return True
    return "not accessible" in lower or "requires authentication" in lower or "must have" in lower


def gh_can_write():
    """Read-only probe of whether gh can write to this repo.

    `repos/{owner}/{repo}` returns the authenticated identity's `permissions`; a
    read-only token reports `push: false`. This is a GET, so it works even where
    writes are blocked. Returns True / False, or None when the answer is
    inconclusive (probe failed, field absent) — in which case the caller attempts
    the write and lets the result decide.
    """
    ok, out, _err = gh(["api", "repos/{owner}/{repo}", "--jq", ".permissions.push"])
    if not ok:
        return None
    value = out.strip().lower()
    if value == "true":
        return True
    if value == "false":
        return False
    return None


def repo_slug():
    """`owner/name` for the current repo, for building blob permalinks, or None."""
    ok, out, _err = gh(["repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"])
    slug = out.strip() if ok else ""
    return slug or None


def existing_issue_keys():
    """Map de-dup key → open issue for issues this skill previously raised.

    Lists open issues (a read, so it works on either path) and scans their bodies
    for the embedded marker. Listing rather than searching avoids the search
    index's lag, which matters when a routine re-runs seconds after creating an
    issue. Returns (keys, capped, error): `keys` is None if the read failed (the
    caller then creates without de-duping and says so); `capped` is True when more
    open issues exist than were scanned.
    """
    ok, out, err = gh([
        "issue", "list", "--state", "open",
        "--limit", str(ISSUE_SCAN_LIMIT), "--json", "number,title,body",
    ])
    if not ok:
        return None, False, err
    try:
        items = json.loads(out)
    except json.JSONDecodeError as exc:
        return None, False, f"could not parse `gh issue list` output: {exc}"
    keys = {}
    for item in items:
        for match in MARKER_RE.findall(item.get("body") or ""):
            keys[match] = {"number": item.get("number"), "title": item.get("title")}
    return keys, len(items) >= ISSUE_SCAN_LIMIT, None


# --- Finding helpers -------------------------------------------------------

def priority_of(finding):
    return (finding.get("priority") or "P2").upper()


def titleize(name):
    """Filename stem → readable name, mirroring plan_review.derive_title.

    Only a fallback: findings normally arrive with `review_title` set by the
    planner. This catches findings that have none (e.g. a combined run's
    general-purpose findings tagged `brief: "general"`).
    """
    words = [w for w in (name or "").replace("_", "-").split("-") if w]
    return " ".join(word.capitalize() for word in words) or (name or "review")


def review_name(finding):
    """The human-readable review a finding belongs to — never the kebab name."""
    return finding.get("review_title") or titleize(finding.get("brief", "review"))


def location(finding):
    loc = finding.get("file", "?")
    if finding.get("line"):
        loc += f":{finding['line']}"
    return loc


def permalink(slug, sha, finding):
    """A GitHub blob permalink to the finding's file/line at the reviewed commit.

    Pinned to the head SHA, not a branch, so the link keeps pointing at the code
    the review actually saw. None when we lack the repo slug, the SHA, or a file.
    """
    if not slug or not sha or not finding.get("file"):
        return None
    url = f"https://github.com/{slug}/blob/{sha}/{finding['file']}"
    if finding.get("line"):
        url += f"#L{finding['line']}"
    return url


def dedup_key(finding):
    """Stable short hash identifying a finding across runs, for issue de-duping.

    Built from the review, the file, and the quoted code (falling back to the
    title) — the things that stay the same when the same problem is re-found, but
    differ between distinct problems. Line numbers are deliberately excluded so an
    unrelated edit that shifts the line does not spawn a duplicate issue.
    """
    quote = finding.get("code_quote") or finding.get("title") or ""
    basis = "\n".join([
        (finding.get("brief") or "").strip(),
        (finding.get("file") or "").strip(),
        "\n".join(line.strip() for line in quote.splitlines()),
    ])
    return hashlib.sha1(basis.encode("utf-8")).hexdigest()[:12]


def html_escape(text):
    """Escape for an inline-HTML context (a <summary>), where markdown chars and
    raw angle brackets would break rendering."""
    return (text or "").replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def badge(priority):
    """Inline shields.io badge for a priority level (gh inline comments only)."""
    colour = PRIORITY_COLOUR.get(priority, "lightgrey")
    return f"![{priority}](https://img.shields.io/badge/{priority}-{colour}?style=flat)"


# --- Formatting: PR review (gh path) ---------------------------------------

def format_inline_comment(finding):
    """Body of a single inline review comment, anchored to its line by gh."""
    priority = priority_of(finding)
    title = (finding.get("title") or finding.get("brief", "review")).strip()
    # Double-subscripted so the badge sits small against the title.
    parts = [f"**<sub><sub>{badge(priority)}</sub></sub>  {title}**", ""]
    parts.append(finding.get("message", "").strip())
    suggestion = finding.get("suggestion")
    if suggestion:
        parts += ["", suggestion.strip()]
    # Quiet footer naming the review this came from — context without jargon.
    parts += ["", f"<sub>{html_escape(review_name(finding))}</sub>"]
    return "\n".join(parts)


def summary_callout(change_count, review_count, head_sha):
    """The `> [!NOTE]` banner that opens a review body or consolidated comment."""
    short_sha = head_sha[:7] if head_sha else None
    if change_count == 0:
        body = "No issues found in the changes."
    else:
        noun = "issue" if change_count == 1 else "issues"
        area = "area" if review_count == 1 else "areas"
        body = f"{change_count} {noun} across {review_count} review {area}."
    if short_sha:
        body += f" Reviewed `{short_sha}`."
    return f"> [!NOTE]\n> {body}"


def render_grouped(findings, slug, head_sha, collapsible):
    """Findings grouped under their review's readable title, sorted by priority.

    `collapsible` wraps each finding in a `<details>` (the consolidated comment's
    layout); otherwise each is a flat block (the review body's off-diff list). The
    blank lines are load-bearing: GitHub needs one after `</summary>` and around
    fenced code or the markdown inside won't render.
    """
    by_review = {}
    for finding in findings:
        by_review.setdefault(review_name(finding), []).append(finding)

    blocks = []
    for review in sorted(by_review):
        items = sorted(by_review[review], key=lambda f: PRIORITY_ORDER.get(priority_of(f), 9))
        section = [f"### {review}", ""]
        for finding in items:
            section.append(
                _collapsible_entry(finding, slug, head_sha)
                if collapsible
                else _flat_entry(finding, slug, head_sha)
            )
            section.append("")
        blocks.append("\n".join(section).rstrip())
    return "\n\n".join(blocks)


def _entry_body(finding, slug, head_sha):
    """The shared inner content of a finding: message, quote, suggestion, link."""
    lines = [finding.get("message", "").strip()]
    quote = finding.get("code_quote")
    if quote:
        lines += ["", "```", quote.rstrip(), "```"]
    suggestion = finding.get("suggestion")
    if suggestion:
        lines += ["", "**Suggested fix**", "", suggestion.strip()]
    link = permalink(slug, head_sha, finding)
    if link:
        lines += ["", f"[`{location(finding)}`]({link})"]
    return lines


def _collapsible_entry(finding, slug, head_sha):
    summary = (
        f"<strong>{priority_of(finding)} · "
        f"<code>{html_escape(location(finding))}</code> — "
        f"{html_escape((finding.get('title') or '').strip())}</strong>"
    )
    return "\n".join(
        ["<details>", f"<summary>{summary}</summary>", ""]
        + _entry_body(finding, slug, head_sha)
        + ["", "</details>"]
    )


def _flat_entry(finding, slug, head_sha):
    header = (
        f"**{priority_of(finding)}** · `{location(finding)}` — "
        f"{(finding.get('title') or '').strip()}"
    )
    return "\n".join([header, ""] + _entry_body(finding, slug, head_sha))


def format_review_body(title, summary, head_sha, change_count, review_count, off_diff, slug):
    """Top-level body of the gh PR review.

    Opens with a heading and a one-line callout. On-diff findings are attached as
    inline comments, so the body only enumerates the `off_diff` findings that
    could not be anchored — or, on the body-only fallback, every finding.
    """
    lines = [f"## {title}", "", summary_callout(change_count, review_count, head_sha)]
    if summary:
        lines += ["", summary]
    if off_diff:
        lines += ["", "---", "", render_grouped(off_diff, slug, head_sha, collapsible=False)]
    return "\n".join(lines).rstrip()


def format_consolidated_comment(title, summary, head_sha, change_findings, slug):
    """The single PR comment posted on the connector path (no inline available).

    Layout: heading, a one-line callout, then every change-introduced finding
    grouped under its review's title with one collapsible block each — scannable
    headers, detail tucked away. Returns None when there is nothing to post.
    """
    if not change_findings:
        return None
    review_count = len({review_name(f) for f in change_findings})
    lines = [f"## {title}", "", summary_callout(len(change_findings), review_count, head_sha)]
    if summary:
        lines += ["", summary]
    lines += ["", render_grouped(change_findings, slug, head_sha, collapsible=True)]
    return "\n".join(lines).rstrip()


# --- Formatting: issues (both paths) ---------------------------------------

def format_issue(finding, pr, slug, head_sha):
    """(title, body, dedup_key) for the issue a pre-existing finding becomes."""
    key = dedup_key(finding)
    review = review_name(finding)
    finding_title = (finding.get("title") or "").strip() or review
    # Prefix with the review for context, unless the title already says it.
    issue_title = finding_title if review.lower() in finding_title.lower() else f"{review}: {finding_title}"
    issue_title = issue_title[:240]

    body = [finding.get("message", "").strip(), ""]
    link = permalink(slug, head_sha, finding)
    loc = f"`{location(finding)}`" + (f" — [view source]({link})" if link else "")
    body += [f"**Location:** {loc}", f"**Priority:** {priority_of(finding)}"]
    quote = finding.get("code_quote")
    if quote:
        body += ["", "```", quote.rstrip(), "```"]
    suggestion = finding.get("suggestion")
    if suggestion:
        body += ["", "**Suggested fix**", "", suggestion.strip()]
    pr_ref = f" while reviewing #{pr}" if pr else ""
    body += [
        "",
        f"<sub>Raised by agent-review · {review}. Pre-existing — independent of the "
        f"change, noticed{pr_ref}.</sub>",
        f"<!-- agent-review-key: {key} -->",
    ]
    return issue_title, "\n".join(body), key


def rollup_key(brief_name):
    """Stable de-dup key for a full-extent brief's rollup issue — one per brief, so
    a re-run finds the existing rollup rather than opening another."""
    return hashlib.sha1(f"{brief_name}\nrollup".encode("utf-8")).hexdigest()[:12]


def format_rollup_issue(findings, pr, slug, head_sha):
    """(title, body, dedup_key) for the one rollup issue that collects a full-extent
    brief's pre-existing findings as a checklist.

    A full-extent brief audits its whole scope, so its pre-existing findings are the
    entire backlog of existing violations — turning each into its own issue would
    flood the tracker on the first run. They collapse into a single checklist issue
    per brief instead. All findings share a brief, so [0] names the review.
    """
    review = review_name(findings[0])
    brief_name = findings[0].get("brief") or "review"
    key = rollup_key(brief_name)
    count = len(findings)
    title = f"{review}: {count} pre-existing finding{'' if count == 1 else 's'}"[:240]

    body = [
        f"Pre-existing violations of the **{review}** review, found while auditing its "
        f"whole scope. They exist independently of the change"
        + (f" reviewed in #{pr}" if pr else "")
        + " — the change did not introduce them.",
        "",
    ]
    for finding in sorted(findings, key=lambda f: PRIORITY_ORDER.get(priority_of(f), 9)):
        link = permalink(slug, head_sha, finding)
        loc = f"[`{location(finding)}`]({link})" if link else f"`{location(finding)}`"
        ftitle = (finding.get("title") or "").strip() or review
        body.append(f"- [ ] **{priority_of(finding)}** {loc} — {ftitle}")
    body += [
        "",
        f"<sub>Raised by agent-review · {review} (full-scope audit). Created once; new "
        f"findings appear here only until this issue is closed.</sub>",
        f"<!-- agent-review-key: {key} -->",
    ]
    return title, "\n".join(body), key


def build_issue_payloads(preexisting, pr, slug, head_sha):
    """De-dup pre-existing findings against open issues and shape them into payloads.

    Two shapes, by the brief's extent — so a full audit can't flood the tracker:
      - **diff-extent** findings are incidental (noticed near the change) → one issue
        each;
      - **full-extent** findings are a whole-scope audit's backlog → one rollup
        checklist issue per brief.
    A finding with no `extent` defaults to diff (an individual issue), preserving the
    original per-finding behaviour for anything the workflow didn't tag.

    Returns (to_create, existing, dedup_note): `to_create` are payloads not already
    open, each {title, body, dedup_key}; `existing` are skipped duplicates with their
    issue number; `dedup_note` flags when de-duping could not run or was capped.
    """
    if not preexisting:
        return [], [], None

    known, capped, err = existing_issue_keys()
    dedup_note = None
    if known is None:
        known = {}
        dedup_note = f"could not list open issues to de-duplicate ({err}); creating without de-dup."
    elif capped:
        dedup_note = (
            f"only the {ISSUE_SCAN_LIMIT} most recent open issues were scanned for "
            f"duplicates; an older duplicate may be recreated."
        )

    to_create, existing, seen = [], [], set()

    def take(title, body, key):
        """Queue an issue payload unless an open issue (or this run) already has it."""
        if key in known:
            existing.append({"dedup_key": key, "number": known[key]["number"], "title": title})
            return
        if key in seen:
            return
        seen.add(key)
        to_create.append({"title": title, "body": body, "dedup_key": key})

    # Incidental findings from diff-extent briefs → one issue each.
    for finding in preexisting:
        if (finding.get("extent") or "diff") != "full":
            take(*format_issue(finding, pr, slug, head_sha))

    # Full-extent findings → one rollup checklist issue per brief.
    by_brief = {}
    for finding in preexisting:
        if (finding.get("extent") or "diff") == "full":
            by_brief.setdefault(finding.get("brief") or "review", []).append(finding)
    for brief_name in sorted(by_brief):
        take(*format_rollup_issue(by_brief[brief_name], pr, slug, head_sha))

    return to_create, existing, dedup_note


# --- Posting (gh path) -----------------------------------------------------

def post_review(pr, head_sha, body, comments):
    """Publish one PR review. Returns (ok, error_text)."""
    payload = {"commit_id": head_sha, "event": "COMMENT", "body": body}
    if comments:
        payload["comments"] = comments
    ok, _out, err = gh(
        ["api", "--method", "POST", f"repos/{{owner}}/{{repo}}/pulls/{pr}/reviews",
         "--input", "-"],
        payload=payload,
    )
    return ok, err


def create_issues(to_create):
    """Create each issue via gh. Returns (created, failed)."""
    created, failed = [], []
    for issue in to_create:
        ok, out, err = gh([
            "issue", "create", "--title", issue["title"], "--body", issue["body"],
        ])
        if ok:
            created.append({"title": issue["title"], "url": out.strip(), "dedup_key": issue["dedup_key"]})
        else:
            failed.append({"title": issue["title"], "error": err})
    return created, failed


def post_via_gh(doc, change_findings, preexisting, slug):
    """Post the review and create the issues with gh. Returns a result dict, or
    None if a write was rejected for lack of permission (caller falls back)."""
    pr = doc["pr"]
    head_sha = doc["head_sha"]
    title = doc.get("title") or "Review"
    summary = doc.get("summary")
    review_count = len({review_name(f) for f in change_findings})

    on_diff = [f for f in change_findings if f.get("file") and f.get("line")]
    off_diff = [f for f in change_findings if not (f.get("file") and f.get("line"))]
    comments = [
        {"path": f["file"], "line": f["line"], "side": f.get("side", "RIGHT"),
         "body": format_inline_comment(f)}
        for f in on_diff
    ]

    review_result = {"posted": False, "reason": "no change-introduced findings to post"}
    if change_findings:
        body = format_review_body(title, summary, head_sha, len(change_findings),
                                  review_count, off_diff, slug)
        ok, err = post_review(pr, head_sha, body, comments)
        if not ok:
            if is_permission_failure(err):
                return None  # gh can't write here — fall back to the connector payload
            if comments:
                # The review was rejected and we sent inline comments. The usual
                # cause is a comment on a line outside the diff, which fails the
                # whole review atomically (HTTP 422) — but retry body-only on any
                # non-permission rejection, so a single bad inline comment never
                # sinks the whole review (the long-standing fallback behaviour).
                body = format_review_body(title, summary, head_sha, len(change_findings),
                                          review_count, change_findings, slug)
                ok, err = post_review(pr, head_sha, body, [])
                if not ok and is_permission_failure(err):
                    return None
                review_result = {
                    "posted": ok, "posted_inline": 0,
                    "in_body_only": len(change_findings),
                    "fell_back_to_body_only": True,
                }
                if not ok:
                    review_result["error"] = err
            else:
                review_result = {"posted": False, "error": err}
        else:
            review_result = {
                "posted": True, "posted_inline": len(comments),
                "in_body_only": len(off_diff), "fell_back_to_body_only": False,
            }

    # Issues. De-dup uses a gh read (works here too); creation is a write.
    to_create, existing, dedup_note = build_issue_payloads(
        preexisting, pr, slug, head_sha
    )
    created, failed = create_issues(to_create)
    # A creation failing for lack of permission means gh writes are blocked after
    # all (the review somehow went through, or there were no change findings) —
    # surface it rather than pretending the issues were raised.
    if failed and all(is_permission_failure(f["error"]) for f in failed) and not created:
        return None

    return {
        "posting": "gh",
        "review": review_result,
        "issues": {
            "created": created, "failed": failed,
            "skipped_existing": existing,
            "dedup_note": dedup_note,
        },
        "stats": {
            "change_introduced": len(change_findings),
            "preexisting": len(preexisting),
            "issues_created": len(created),
            "issues_existing": len(existing),
        },
    }


# --- Render payload (connector path) ---------------------------------------

def build_render(doc, change_findings, preexisting, slug, reason):
    """The payload the calling skill posts via its GitHub connector tools.

    Carries the finished consolidated comment and the de-duplicated issue bodies,
    so the skill only makes the connector calls — it never composes outward text
    (which keeps process vocabulary out of GitHub).
    """
    pr = doc["pr"]
    head_sha = doc.get("head_sha")
    title = doc.get("title") or "Review"
    summary = doc.get("summary")
    comment = format_consolidated_comment(title, summary, head_sha, change_findings, slug)
    to_create, existing, dedup_note = build_issue_payloads(preexisting, pr, slug, head_sha)
    return {
        "reason": reason,
        "render": {
            "pr": pr,
            "comment_markdown": comment,
            "issues_to_create": to_create,
            "issues_existing": existing,
            "dedup_note": dedup_note,
            "stats": {
                "change_introduced": len(change_findings),
                "preexisting": len(preexisting),
                "issues_new": len(to_create),
                "issues_existing": len(existing),
            },
        },
    }


# --- Entry point -----------------------------------------------------------

def load_document(path):
    """Read the findings JSON from a file path if given, else stdin.

    A path argument lets the skill run `python3 …post_pr_comments.py <file>` — a
    command beginning with `python3`, so it matches the skill's allowed-tools
    pattern and posts without a permission prompt (and sidesteps shell-quoting a
    large JSON blob). Stdin is still supported for callers that pipe a document.
    """
    if path:
        try:
            with open(path, encoding="utf-8") as handle:
                return json.load(handle)
        except OSError as exc:
            print(f"Could not read findings file '{path}': {exc}", file=sys.stderr)
            sys.exit(1)
        except json.JSONDecodeError as exc:
            print(f"Invalid findings JSON in '{path}': {exc}", file=sys.stderr)
            sys.exit(1)
    try:
        return json.load(sys.stdin)
    except json.JSONDecodeError as exc:
        print(f"Invalid findings JSON on stdin: {exc}", file=sys.stderr)
        sys.exit(1)


def main():
    parser = argparse.ArgumentParser(description="Publish agent-review findings to GitHub.")
    parser.add_argument("file", nargs="?", help="Findings JSON file (else stdin).")
    parser.add_argument(
        "--render", action="store_true",
        help="Write nothing; print the connector payload (comment + issue bodies) "
             "for the skill to post itself. Forces the connector path.",
    )
    args = parser.parse_args()

    doc = load_document(args.file)
    pr = doc.get("pr")
    head_sha = doc.get("head_sha")
    if not pr or not head_sha:
        print("findings JSON must include 'pr' and 'head_sha'.", file=sys.stderr)
        sys.exit(1)

    findings = doc.get("findings", [])
    # Route: a finding the change did not introduce becomes an issue; everything
    # else goes on the PR.
    preexisting = [f for f in findings if f.get("preexisting") is True]
    change_findings = [f for f in findings if f.get("preexisting") is not True]

    # `gh` reads (repo slug, issue list) work even on the connector path, so
    # resolve the slug regardless — it makes permalinks possible everywhere.
    slug = repo_slug() if gh_present() else None

    # Forced connector payload, or no gh at all to write with.
    if args.render or not gh_present():
        reason = "forced --render" if args.render else "gh CLI not available"
        result = build_render(doc, change_findings, preexisting, slug, reason)
        result["posting"] = "render"
        print(json.dumps(result, indent=2))
        return

    # Up-front read-only probe: a read-only token reports push:false, letting us
    # skip a doomed write attempt. None (inconclusive) proceeds to attempt.
    if gh_can_write() is False:
        result = build_render(doc, change_findings, preexisting, slug,
                              "gh is read-only in this environment (cannot write)")
        result["posting"] = "gh_unavailable"
        print(json.dumps(result, indent=2))
        return

    posted = post_via_gh(doc, change_findings, preexisting, slug)
    if posted is None:
        # The probe said writable (or was unsure) but a write was rejected for
        # permission — gh genuinely can't write here. Fall back.
        result = build_render(doc, change_findings, preexisting, slug,
                              "gh write was rejected (no write access); use the connector")
        result["posting"] = "gh_unavailable"
        print(json.dumps(result, indent=2))
        return

    print(json.dumps(posted, indent=2))


if __name__ == "__main__":
    main()
