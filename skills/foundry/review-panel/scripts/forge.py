#!/usr/bin/env python3
"""Which forge a repository lives on, and how to talk to it.

Shared by plan_review.py (PR detection) and post_pr_comments.py (publishing).
Two forges are recognised, keyed off the origin remote's host:

  - **github.com** → the GitHub path. Callers use the `gh` CLI for everything;
    this module only does the detection.
  - **anything else** → the Forgejo path. Forgejo (and Gitea, whose API it
    keeps) has no equivalent of `gh`'s ambient auth, so credentials come from a
    machine-local config file mapping each remote host to an instance URL and
    an API token:

        # ~/.config/forgejo/instances.toml
        ["forgejo-user"]                     # the remote's host or SSH alias —
        url = "http://192.0.2.10:3000"     # quote keys containing dots
        token = "..."

    The section key is matched against the host part of `git remote get-url
    origin` exactly as written — an SSH alias (`forgejo-user:owner/repo.git`)
    matches a `["forgejo-user"]` section, an HTTPS remote matches its domain.
    One instance reached through several aliases takes one section per alias
    (different aliases usually mean different accounts, hence different
    tokens).

Everything here is stdlib-only: these scripts run under whatever python3 the
machine has, with no virtualenv to install into.
"""

import json
import os
import re
import subprocess
import urllib.error
import urllib.parse
import urllib.request

CONFIG_PATH = os.path.expanduser("~/.config/forgejo/instances.toml")

# Hosts that take the GitHub path. Everything else is presumed Forgejo — the
# other self-hosted forges this skill might meet (Gitea) share the same API.
GITHUB_HOSTS = {"github.com"}

# scp-like remote: [user@]host:owner/repo[.git] — the host may be a bare SSH
# alias with no user or dots (`forgejo-user:example/repo.git`). Excludes URLs
# with an explicit scheme, which urlsplit handles below.
_SCP_RE = re.compile(r"^(?:(?P<user>[^@/]+)@)?(?P<host>[^:/]+):(?P<path>[^/].*)$")


class Remote:
    """The origin remote, decomposed: which forge, which instance, which repo."""

    def __init__(self, kind, host, owner, repo):
        self.kind = kind      # "github" | "forgejo"
        self.host = host      # host or SSH alias, as written in the remote URL
        self.owner = owner
        self.repo = repo

    @property
    def slug(self):
        return f"{self.owner}/{self.repo}"


def _parse_remote_url(url):
    """(host, owner, repo) from a git remote URL, or None if unrecognised."""
    url = (url or "").strip()
    if not url:
        return None
    if "://" in url:
        parts = urllib.parse.urlsplit(url)
        host, path = parts.hostname, parts.path
    else:
        match = _SCP_RE.match(url)
        if not match:
            return None
        host, path = match.group("host"), match.group("path")
    segments = [s for s in (path or "").split("/") if s]
    if not host or len(segments) < 2:
        return None
    owner, repo = segments[-2], segments[-1]
    if repo.endswith(".git"):
        repo = repo[: -len(".git")]
    return host, owner, repo


def detect_remote(root):
    """Classify the repo's origin remote. None when there is no usable remote."""
    try:
        url = subprocess.run(
            ["git", "remote", "get-url", "origin"],
            cwd=root, capture_output=True, text=True, check=True,
        ).stdout.strip()
    except (subprocess.CalledProcessError, FileNotFoundError):
        return None
    parsed = _parse_remote_url(url)
    if parsed is None:
        return None
    host, owner, repo = parsed
    kind = "github" if host.lower() in GITHUB_HOSTS else "forgejo"
    return Remote(kind, host, owner, repo)


def load_instance(host):
    """The configured {url, token} for a Forgejo host, or (None, reason).

    The reason is written for the run report: it says what is missing and where
    to put it, so a failed lookup is actionable rather than silent.
    """
    # Imported lazily: tomllib is stdlib only from Python 3.11, and the GitHub
    # path never reads this config — an older interpreter should still run the
    # gh flow rather than die at import time.
    try:
        import tomllib
    except ImportError:
        return None, (
            f"this python3 has no tomllib (stdlib from Python 3.11), so "
            f"{CONFIG_PATH} cannot be read; run the skill with Python 3.11+."
        )
    if not os.path.isfile(CONFIG_PATH):
        return None, (
            f"no Forgejo credentials file at {CONFIG_PATH} — create it with a "
            f'["{host}"] section carrying `url` and `token`.'
        )
    try:
        with open(CONFIG_PATH, "rb") as handle:
            config = tomllib.load(handle)
    except (OSError, tomllib.TOMLDecodeError) as exc:
        return None, f"could not read {CONFIG_PATH}: {exc}"
    entry = config.get(host)
    if not isinstance(entry, dict) or not entry.get("url") or not entry.get("token"):
        return None, (
            f'no usable ["{host}"] section in {CONFIG_PATH} — it needs `url` '
            f"and `token` for the instance this remote points at."
        )
    return {"url": entry["url"].rstrip("/"), "token": entry["token"]}, None


class ForgejoClient:
    """Minimal authenticated client for one repo on one Forgejo instance."""

    def __init__(self, instance, owner, repo):
        self.base = instance["url"]           # instance root, no trailing slash
        self.token = instance["token"]
        self.owner = owner
        self.repo = repo

    def api(self, method, path, payload=None, params=None):
        """One api/v1 call. Returns (ok, data, status, error_text).

        `path` is relative to the repo (`issues`, `pulls/3/reviews`) unless it
        starts with '/' (then relative to api/v1). Errors never raise: callers
        fall back or report, mirroring how gh failures are handled.
        """
        if path.startswith("/"):
            url = f"{self.base}/api/v1{path}"
        else:
            # An empty path means the repo record itself; avoid a trailing slash.
            url = f"{self.base}/api/v1/repos/{self.owner}/{self.repo}/{path}".rstrip("/")
        if params:
            url += "?" + urllib.parse.urlencode(params)
        body = json.dumps(payload).encode("utf-8") if payload is not None else None
        request = urllib.request.Request(
            url, data=body, method=method,
            headers={
                "Authorization": f"token {self.token}",
                "Content-Type": "application/json",
                "Accept": "application/json",
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                raw = response.read().decode("utf-8")
                status = response.status
        except urllib.error.HTTPError as exc:
            detail = ""
            try:
                detail = exc.read().decode("utf-8", "replace").strip()
            except OSError:
                pass
            return False, None, exc.code, f"HTTP {exc.code}: {detail or exc.reason}"
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            return False, None, None, f"could not reach {self.base}: {exc}"
        if not raw:
            return True, None, status, ""
        try:
            return True, json.loads(raw), status, ""
        except json.JSONDecodeError as exc:
            return False, None, status, f"unparseable API response: {exc}"

    def paged(self, path, params=None, cap=200):
        """GET every page of a list endpoint, up to `cap` items.

        Returns (items, capped, error_text). Forgejo caps `limit` per page at
        the instance's max (50 by default), so the cap is walked page by page.
        """
        items, page = [], 1
        params = dict(params or {})
        while len(items) < cap:
            params.update({"page": page, "limit": min(50, cap - len(items))})
            ok, data, _status, err = self.api("GET", path, params=params)
            if not ok:
                return None, False, err
            if not isinstance(data, list) or not data:
                return items, False, None
            items.extend(data)
            if len(data) < params["limit"]:
                return items, False, None
            page += 1
        return items, True, None

    def blob_url(self, sha, file, line=None):
        """A web permalink to a file at a commit, Forgejo's URL shape."""
        url = f"{self.base}/{self.owner}/{self.repo}/src/commit/{sha}/{file}"
        if line:
            url += f"#L{line}"
        return url


def forgejo_client(root):
    """Detect + configure in one step: (client, reason).

    A GitHub remote, a missing remote, or missing credentials all return
    (None, reason) — reason is None only for the GitHub/no-remote cases, where
    the caller's gh path or chat fallback takes over silently.
    """
    remote = detect_remote(root)
    if remote is None or remote.kind != "forgejo":
        return None, None
    instance, reason = load_instance(remote.host)
    if instance is None:
        return None, reason
    return ForgejoClient(instance, remote.owner, remote.repo), None
