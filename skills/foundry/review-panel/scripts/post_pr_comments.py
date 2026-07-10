#!/usr/bin/env python3
"""Publish collated review-panel findings to the repo's pull request.

Reads one findings JSON document — from a file-path argument when given, else
stdin — and posts the **change-introduced findings** (no `preexisting: true`)
to the pull request: one PR review with an inline comment per finding, or one
consolidated comment on the connector path.

**Pre-existing findings** (`preexisting: true`) are never posted to the forge.
They are counted and returned in the result for the calling skill to route —
to the project annexe's ISSUES.md where the project has an annexe, otherwise
into the run's chat report.

The forge is keyed off the origin remote (see forge.py):

  - **GitHub remotes** post through the `gh` CLI, with two write paths chosen
    automatically. When `gh` can write, this script posts everything itself.
    When it cannot — the Claude Code Web routine environment, where `gh` is
    read-only and writes go through the GitHub connector — the script writes
    nothing and instead prints a `render` payload: the finished comment
    markdown, for the calling skill to post with its GitHub connector tools.
    Write capability is detected up front with
    a read-only permissions probe, with a write-failure check as the backstop
    (the precise way web blocks writes is undocumented, so the attempt is the
    only fully reliable signal).
  - **Every other remote is a Forgejo/Gitea instance**, reached directly over
    its API with the per-host credentials in ~/.config/forgejo/instances.toml.
    There is no connector environment on this path: when the credentials are
    missing or the API refuses, the script prints the same `render` payload with
    a reason, and the calling skill reports the failure (and the fix) instead of
    posting.

`--render` forces the render payload on either forge without attempting any
write — used for tests and when the caller already knows it cannot post.

All outward-facing text is generated here, verbatim, from the findings — never
by the model — so internal process vocabulary (the fan-out, file slices, reviewer
indices, kebab-case concern filenames) cannot leak into a PR. Findings are
labelled by each concern's human-readable `review_title`.

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
import json
import os
import shutil
import subprocess
import sys

import forge as forge_mod

# Lower number sorts first (most urgent). Anything unrecognised sorts last.
PRIORITY_ORDER = {"P0": 0, "P1": 1, "P2": 2, "P3": 3}

# shields.io badge colour per priority. Used only on GitHub inline comments —
# one badge per anchored comment. The consolidated comment uses
# plain priority text instead: dozens of badge images in one body all fetch
# through GitHub's image proxy, which is slow and flaky at that volume. Forgejo
# inline comments use plain text too — a self-hosted instance (and its readers)
# should not depend on an external image service at all.
PRIORITY_COLOUR = {"P0": "red", "P1": "orange", "P2": "yellow", "P3": "blue"}

# The run's forge context, set at entry (run_forgejo) before any formatting
# happens. Threading it through every formatting helper would churn eight
# signatures for one script-lifetime constant; a single module-level record is
# the lesser evil. "kind" picks the permalink shape and badge style; "web_base"
# is the instance root for Forgejo permalinks (GitHub's is fixed) — None when
# the instance is unknown (missing credentials), in which case permalinks are
# omitted rather than fabricated.
RUN_FORGE = {"kind": "github", "web_base": "https://github.com"}

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
    """True when a write failed because this identity cannot write here at all —
    a read-only token or the read-only web routine — as opposed to a content
    rejection (e.g. an inline comment off the diff) or a transient error.

    Only this distinction drives control flow: a permission failure means fall
    back to the render payload, where any other failure with inline comments is
    retried body-only (see post_via_forge). The auth/permission family is the
    4xx access codes plus GitHub's stock "not accessible" / "requires
    authentication" phrasing. Shared by both forges: gh and forge.ForgejoClient
    both put the HTTP status in their error text.
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


# --- Forge adapters ---------------------------------------------------------
# One object per forge with the same five operations, so the posting logic
# (post_via_forge) is written once. The GitHub adapter wraps the gh helpers
# above; the Forgejo adapter wraps forge.ForgejoClient. is_permission_failure
# is shared: both surfaces put the HTTP status in their error text.

class GitHubForge:
    """Posting operations over the gh CLI."""

    name = "gh"

    def slug(self):
        return repo_slug()

    def can_write(self):
        return gh_can_write()

    def inline_comment(self, finding):
        return {
            "path": finding["file"], "line": finding["line"],
            "side": finding.get("side", "RIGHT"),
            "body": format_inline_comment(finding),
        }

    def post_review(self, pr, head_sha, body, comments):
        return post_review(pr, head_sha, body, comments)

class ForgejoForge:
    """Posting operations over a Forgejo instance's API (forge.ForgejoClient)."""

    name = "forgejo"

    def __init__(self, client):
        self.client = client

    def slug(self):
        return f"{self.client.owner}/{self.client.repo}"

    def can_write(self):
        """permissions.push for the token's identity — the same probe gh makes."""
        ok, data, _status, _err = self.client.api("GET", "")
        if not ok or not isinstance(data, dict):
            return None
        value = (data.get("permissions") or {}).get("push")
        return value if isinstance(value, bool) else None

    def inline_comment(self, finding):
        # Forgejo anchors a review comment by file position per side:
        # new_position for the new file (RIGHT), old_position for the old (LEFT).
        comment = {"path": finding["file"], "body": format_inline_comment(finding)}
        if finding.get("side", "RIGHT") == "LEFT":
            comment["old_position"] = finding["line"]
        else:
            comment["new_position"] = finding["line"]
        return comment

    def post_review(self, pr, head_sha, body, comments):
        payload = {"commit_id": head_sha, "event": "COMMENT", "body": body}
        if comments:
            payload["comments"] = comments
        ok, _data, _status, err = self.client.api("POST", f"pulls/{pr}/reviews", payload)
        return ok, err

# --- Finding helpers -------------------------------------------------------

def priority_of(finding):
    return (finding.get("priority") or "P2").upper()


def collapse_duplicates(findings):
    """Merge findings that cite exactly the same code for the same defect site.

    Several concerns can legitimately raise one underlying defect (an unlogged
    failure is a correctness, error-handling, and security matter at once), and
    each copy has already passed verification. Posting them all as separate
    comments buries the signal, so exact duplicates — same file, same line, same
    normalised quote — collapse to one finding: the highest-priority copy, with
    the other concerns' review titles recorded in `also_raised_by` so the posted
    footer still credits every concern that raised it. Near-duplicates with
    different quotes or lines are left alone; merging judgement calls is not
    this script's job.
    """
    kept, by_site = [], {}
    for finding in findings:
        quote = "\n".join(
            line.strip() for line in (finding.get("code_quote") or "").splitlines()
        ).strip()
        site = (finding.get("file"), finding.get("line"), quote)
        if not all(site[:2]) or not quote:
            kept.append(finding)
            continue
        prior = by_site.get(site)
        if prior is None:
            finding = dict(finding)
            by_site[site] = finding
            kept.append(finding)
            continue
        names = prior.setdefault("also_raised_by", [])
        name = review_name(finding)
        if name != review_name(prior) and name not in names:
            names.append(name)
        if PRIORITY_ORDER.get(priority_of(finding), 9) < PRIORITY_ORDER.get(priority_of(prior), 9):
            prior["priority"] = priority_of(finding)
    return kept


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


def review_names_line(finding):
    """The footer credit: the finding's review plus any concerns it was
    collapsed with (see collapse_duplicates)."""
    return " · ".join([review_name(finding)] + finding.get("also_raised_by", []))


def location(finding):
    loc = finding.get("file", "?")
    if finding.get("line"):
        loc += f":{finding['line']}"
    return loc


def permalink(slug, sha, finding):
    """A web permalink to the finding's file/line at the reviewed commit.

    Pinned to the head SHA, not a branch, so the link keeps pointing at the code
    the review actually saw. The URL shape is the run's forge's (RUN_FORGE):
    GitHub serves blobs at `/blob/<sha>/`, Forgejo at `/src/commit/<sha>/`.
    None when we lack the repo slug, the SHA, or a file.
    """
    if not slug or not sha or not finding.get("file") or not RUN_FORGE.get("web_base"):
        return None
    if RUN_FORGE["kind"] == "forgejo":
        url = f"{RUN_FORGE['web_base']}/{slug}/src/commit/{sha}/{finding['file']}"
    else:
        url = f"{RUN_FORGE['web_base']}/{slug}/blob/{sha}/{finding['file']}"
    if finding.get("line"):
        url += f"#L{finding['line']}"
    return url


def html_escape(text):
    """Escape for an inline-HTML context (a <summary>), where markdown chars and
    raw angle brackets would break rendering."""
    return (text or "").replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def badge(priority):
    """Inline priority marker for an anchored review comment.

    A shields.io badge on GitHub; plain text on Forgejo, where the instance is
    typically self-hosted and an external image dependency would break (or leak
    the review's existence) the moment the network does not oblige.
    """
    if RUN_FORGE["kind"] == "forgejo":
        return f"`{priority}`"
    colour = PRIORITY_COLOUR.get(priority, "lightgrey")
    return f"![{priority}](https://img.shields.io/badge/{priority}-{colour}?style=flat)"


# --- Formatting: PR review (direct-write paths) -----------------------------

def format_inline_comment(finding):
    """Body of a single inline review comment, anchored to its line."""
    priority = priority_of(finding)
    title = (finding.get("title") or finding.get("brief", "review")).strip()
    # Double-subscripted so the badge sits small against the title.
    parts = [f"**<sub><sub>{badge(priority)}</sub></sub>  {title}**", ""]
    parts.append(finding.get("message", "").strip())
    suggestion = finding.get("suggestion")
    if suggestion:
        parts += ["", suggestion.strip()]
    # Quiet footer naming the review(s) this came from — context without jargon.
    parts += ["", f"<sub>{html_escape(review_names_line(finding))}</sub>"]
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
    headers, detail tucked away. With zero findings it is just the heading and
    the clean callout — a clean run still posts one review.
    """
    review_count = len({review_name(f) for f in change_findings})
    lines = [f"## {title}", "", summary_callout(len(change_findings), review_count, head_sha)]
    if summary:
        lines += ["", summary]
    lines += ["", render_grouped(change_findings, slug, head_sha, collapsible=True)]
    return "\n".join(lines).rstrip()


# --- Posting (direct-write paths) -------------------------------------------

def post_review(pr, head_sha, body, comments):
    """Publish one PR review via gh. Returns (ok, error_text)."""
    payload = {"commit_id": head_sha, "event": "COMMENT", "body": body}
    if comments:
        payload["comments"] = comments
    ok, _out, err = gh(
        ["api", "--method", "POST", f"repos/{{owner}}/{{repo}}/pulls/{pr}/reviews",
         "--input", "-"],
        payload=payload,
    )
    return ok, err


def post_via_forge(forge_impl, doc, change_findings, preexisting, slug):
    """Post the review to the PR. Returns a result dict, or None if a write was
    rejected for lack of permission (caller falls back). Pre-existing findings
    are counted, never posted — the calling skill routes them (annexe or chat)."""
    pr = doc["pr"]
    head_sha = doc["head_sha"]
    title = doc.get("title") or "Review"
    summary = doc.get("summary")
    review_count = len({review_name(f) for f in change_findings})

    on_diff = [f for f in change_findings if f.get("file") and f.get("line")]
    off_diff = [f for f in change_findings if not (f.get("file") and f.get("line"))]
    comments = [forge_impl.inline_comment(f) for f in on_diff]

    # A clean run still posts one review: "no issues found" on the PR is a
    # result, and its absence would be indistinguishable from a run that never
    # happened. format_review_body renders the zero-findings callout itself.
    body = format_review_body(title, summary, head_sha, len(change_findings),
                              review_count, off_diff, slug)
    ok, err = forge_impl.post_review(pr, head_sha, body, comments)
    if not ok:
        if is_permission_failure(err):
            return None  # the forge won't take writes — fall back to the render payload
        if comments:
            # The review was rejected and we sent inline comments. The usual
            # cause is a comment on a line outside the diff, which fails the
            # whole review atomically (HTTP 422) — but retry body-only on any
            # non-permission rejection, so a single bad inline comment never
            # sinks the whole review (the long-standing fallback behaviour).
            body = format_review_body(title, summary, head_sha, len(change_findings),
                                      review_count, change_findings, slug)
            ok, err = forge_impl.post_review(pr, head_sha, body, [])
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

    return {
        "posting": forge_impl.name,
        "review": review_result,
        "stats": {
            "change_introduced": len(change_findings),
            # Not posted: returned for the calling skill to route to the project
            # annexe's ISSUES.md, or into the chat report when there is no annexe.
            "preexisting_for_routing": len(preexisting),
        },
    }


# --- Render payload (connector / no-write fallback) -------------------------

def build_render(doc, change_findings, preexisting, slug, reason):
    """The payload printed when this script cannot (or must not) write.

    On GitHub it is what the calling skill posts via its connector tools; on
    Forgejo, where no connector exists, it documents what would have been posted
    while the skill reports the blocking reason. Either way it carries the
    finished consolidated comment, so the model never composes outward text
    (which keeps process vocabulary off the forge).
    """
    pr = doc["pr"]
    head_sha = doc.get("head_sha")
    title = doc.get("title") or "Review"
    summary = doc.get("summary")
    comment = format_consolidated_comment(title, summary, head_sha, change_findings, slug)
    return {
        "reason": reason,
        "render": {
            "pr": pr,
            "comment_markdown": comment,
            "stats": {
                "change_introduced": len(change_findings),
                "preexisting_for_routing": len(preexisting),
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
    parser = argparse.ArgumentParser(
        description="Publish review-panel findings to the repo's pull request."
    )
    parser.add_argument("file", nargs="?", help="Findings JSON file (else stdin).")
    parser.add_argument(
        "--render", action="store_true",
        help="Write nothing; print the render payload (the consolidated comment) "
             "instead of posting. Forces the connector path on GitHub.",
    )
    args = parser.parse_args()

    doc = load_document(args.file)
    pr = doc.get("pr")
    head_sha = doc.get("head_sha")
    if not pr or not head_sha:
        print("findings JSON must include 'pr' and 'head_sha'.", file=sys.stderr)
        sys.exit(1)

    findings = collapse_duplicates(doc.get("findings", []))
    # Split: change-introduced findings go on the PR; pre-existing ones are only
    # counted here — the calling skill routes them to the annexe or the chat.
    preexisting = [f for f in findings if f.get("preexisting") is True]
    change_findings = [f for f in findings if f.get("preexisting") is not True]

    remote = forge_mod.detect_remote(os.getcwd())
    if remote is not None and remote.kind == "forgejo":
        run_forgejo(args, doc, change_findings, preexisting, remote)
    else:
        run_github(args, doc, change_findings, preexisting)


def run_github(args, doc, change_findings, preexisting):
    """The GitHub flow: gh writes, with the connector render as the fallback."""
    # `gh` reads (the repo slug) work even on the connector path, so resolve
    # the slug regardless — it makes permalinks possible everywhere.
    slug = repo_slug() if gh_present() else None
    github = GitHubForge()

    # Forced connector payload, or no gh at all to write with.
    if args.render or not gh_present():
        reason = "forced --render" if args.render else "gh CLI not available"
        result = build_render(doc, change_findings, preexisting, slug, reason)
        result["posting"] = "render"
        print(json.dumps(result, indent=2))
        return

    # Up-front read-only probe: a read-only token reports push:false, letting us
    # skip a doomed write attempt. None (inconclusive) proceeds to attempt.
    if github.can_write() is False:
        result = build_render(doc, change_findings, preexisting, slug,
                              "gh is read-only in this environment (cannot write)")
        result["posting"] = "gh_unavailable"
        print(json.dumps(result, indent=2))
        return

    posted = post_via_forge(github, doc, change_findings, preexisting, slug)
    if posted is None:
        # The probe said writable (or was unsure) but a write was rejected for
        # permission — gh genuinely can't write here. Fall back.
        result = build_render(doc, change_findings, preexisting, slug,
                              "gh write was rejected (no write access); use the connector")
        result["posting"] = "gh_unavailable"
        print(json.dumps(result, indent=2))
        return

    print(json.dumps(posted, indent=2))


def run_forgejo(args, doc, change_findings, preexisting, remote):
    """The Forgejo flow: API writes with per-host credentials, no connector.

    A failure here (missing credentials, rejected write) still prints the render
    payload — not for a connector to post, but so the run's report can say
    exactly what was withheld — with posting "forgejo_unavailable" and the
    actionable reason.
    """
    slug = remote.slug
    # Formatting takes Forgejo shapes from here on — before the credential
    # lookup, so even the failure path below never renders github.com links for
    # a Forgejo repo. web_base stays None until the instance URL is known;
    # permalink() omits links rather than invent them.
    RUN_FORGE.update({"kind": "forgejo", "web_base": None})
    instance, reason = forge_mod.load_instance(remote.host)

    if instance is None:
        result = build_render(doc, change_findings, preexisting, slug, reason)
        result["posting"] = "forgejo_unavailable"
        print(json.dumps(result, indent=2))
        return

    client = forge_mod.ForgejoClient(instance, remote.owner, remote.repo)
    RUN_FORGE.update({"web_base": instance["url"]})
    forgejo = ForgejoForge(client)

    if args.render:
        result = build_render(doc, change_findings, preexisting, slug,
                              "forced --render")
        result["posting"] = "render"
        print(json.dumps(result, indent=2))
        return

    if forgejo.can_write() is False:
        result = build_render(doc, change_findings, preexisting, slug,
                              f"the configured token cannot write to {slug}")
        result["posting"] = "forgejo_unavailable"
        print(json.dumps(result, indent=2))
        return

    posted = post_via_forge(forgejo, doc, change_findings, preexisting, slug)
    if posted is None:
        result = build_render(doc, change_findings, preexisting, slug,
                              "the Forgejo API rejected the write (no write access)")
        result["posting"] = "forgejo_unavailable"
        print(json.dumps(result, indent=2))
        return

    print(json.dumps(posted, indent=2))


if __name__ == "__main__":
    main()
