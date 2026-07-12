# Review scripts

These standalone scripts reduce mechanical review facts to JSON for the lead
and deployment evidence. Exit `0` means the result is admissible or coverage is
complete, exit `1` means a well-formed check found a gap, and exit `2` means the
input itself could not be checked. They do not judge whether a review understood
the code.

## Coverage accounting

`account-coverage` inventories or checks the changed files and zero-context Git
hunks between two commits:

```sh
scripts/review/account-coverage \
  --repo /path/to/repo --base BASE --head HEAD --inventory

scripts/review/account-coverage \
  --repo /path/to/repo --base BASE --head HEAD --record inspection.json
```

Use the inventory to initialise the inspection record, then add `account` to
every file and hunk with the value `read` or `omitted`. The record has this
shape:

```json
{
  "schema_version": 1,
  "base_commit": "full base commit ID",
  "head_commit": "full head commit ID",
  "files": [{
    "path": "relative/path.go",
    "blob": "immutable blob ID from the inventory",
    "account": "read",
    "hunks": [{
      "old_start": 10,
      "old_lines": 2,
      "new_start": 10,
      "new_lines": 3,
      "account": "read"
    }]
  }],
  "references": [{
    "kind": "definition",
    "path": "relative/path.go",
    "commit": "full base or head commit ID",
    "blob": "immutable blob ID from the inventory",
    "line": 27,
    "name": "FunctionName",
    "line_text": "func FunctionName() error {"
  }]
}
```

References may be `definition` or `call_site`, and may point to unchanged files.
The script verifies that the path resolves to the claimed blob at the stated
base or head commit, that `line_text` exactly matches the stated line, and that
`name` occurs there as a complete identifier rather than a substring of another
identifier. It does not infer program semantics from the name.

## Worker model admission

`admit-worker-model` checks one Ensemble `agent_record` before its judgement is
used:

```sh
scripts/review/admit-worker-model \
  --policy worker-models.json --role verifier \
  --record /run/agents/000007/agent.json \
  --counterpart-family claude
```

The policy names exact pins and which roles carry a guarantee:

```json
{
  "schema_version": 1,
  "unresolved_model": "stop",
  "roles": {
    "verifier": {
      "guarantee": true,
      "engine": "codex",
      "model": "gpt-5.6-sol",
      "family": "gpt",
      "request_models": ["gpt-5.6-sol"],
      "label_prefixes": ["verify:"]
    },
    "claude-verifier": {
      "guarantee": true,
      "engine": "claude",
      "model": "claude-opus-4-8",
      "family": "claude",
      "request_models": ["opus", "claude-opus-4-8"],
      "label_prefixes": ["verify:"]
    },
    "inventory": {
      "guarantee": false,
      "label_prefixes": ["inventory:"]
    }
  }
}
```

`model` is always the exact concrete resolved-model pin. `request_models` is the
explicit allow-list of concrete names or aliases the workflow may request for
that pin; this accommodates records such as requested `opus`, resolved
`claude-opus-4-8`, without treating the alias as served-model evidence. The
record's own label must match one of the asserted role's `label_prefixes`.

`unresolved_model` is `stop` unless repository policy explicitly sets
`limited`. An unexpected engine, request form, role label, or resolved model is
inadmissible. An exact resolved pin is `full` only when
`--counterpart-family` is known and different; the same, unknown, or omitted
counterpart family is honestly `limited`. Mechanical roles still bind their
record label, then return `mechanical` without treating model fields as
guarantee evidence.
