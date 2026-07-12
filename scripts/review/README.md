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
    "name": "FunctionName"
  }]
}
```

References may be `definition` or `call_site`, and may point to unchanged files.
The script verifies that the path resolves to the claimed blob at the stated
base or head commit and that the named location exists on the stated line. It
does not infer program semantics from the name.

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
      "family": "gpt"
    },
    "inventory": {"guarantee": false}
  }
}
```

`unresolved_model` is `stop` unless repository policy explicitly sets
`limited`. A requested or resolved pin mismatch is always inadmissible. An exact
pin is `full` only when `--counterpart-family` is known and different; the same,
unknown, or omitted counterpart family is honestly `limited`. Mechanical roles
return `mechanical` without treating their record as guarantee evidence.
