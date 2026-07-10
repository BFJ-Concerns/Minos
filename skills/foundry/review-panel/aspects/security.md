---
title: Security
relevance: The change touches input handling or parsing of external data, authentication or authorisation, sessions or tokens, secrets or credentials, cryptography, file paths derived from input, SQL or shell or template construction, network exposure or service configuration, CORS or permissions, debug or admin surfaces, logging or error output that could disclose internal state, deserialisation, or dependency versions. Clearly irrelevant when the diff touches none of these surfaces.
---

# Security

Review the changed code for weaknesses that would let it be misused or would
expose what it should protect. This is a defensive review of the project's own
robustness: the question for every finding is "what input or circumstance makes
this code do something its author did not intend, and what is the
consequence?" Answer it concretely, in the same calm engineering terms as any
other defect.

## What to examine

- **Untrusted input reaching sensitive operations**: data from users, networks,
  files, or other processes that flows into SQL, shell commands, file paths,
  templates, HTML, or queries without validation or escaping appropriate to
  that destination. Trace the flow — the finding is the path from input to
  operation.
- **Authentication and authorisation**: new endpoints, commands, or operations
  reachable without the checks their neighbours enforce; authorisation checked
  in the UI but not at the operation itself; identity or role decisions made
  from client-supplied values.
- **Secrets**: credentials, tokens, or keys written into code, configuration
  headed for version control, logs, error messages, or URLs.
- **Cryptography**: home-built constructions where the platform provides a
  primitive; obsolete algorithms or modes; keys or nonces generated, stored, or
  reused wrongly; comparisons that leak timing where it matters.
- **Deserialisation and parsing**: formats that can encode object construction
  or code execution, parsed from external data with a permissive loader when a
  safe one exists.
- **Exposure defaults**: services bound wider than needed, permissive CORS or
  file permissions, debug surfaces left reachable, errors that echo internal
  state to the outside.
- **Dependencies**: a dependency change that introduces a version with a known
  weakness, or swaps a maintained library for an unmaintained one on a
  sensitive path.

## What qualifies as a finding

Name the concrete input or condition, the operation it reaches, and the
consequence — "a filename of `../../etc/passwd` here reads outside the upload
directory because the path is joined before validation" — and quote the code
that permits it. Severity follows consequence: data exposure or destructive
reach is P0/P1; a hardening gap with no direct path is P2/P3. A generic
observation that code "should sanitise input", with no path to a consequence,
is below the bar.

Judge the change, not the whole system: pre-existing weaknesses you notice
nearby are `preexisting: true` findings, valuable but distinct from what this
change introduces.
