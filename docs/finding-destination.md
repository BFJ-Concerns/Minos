# Finding destination adaptation

Minos delivers verified findings below a repository's publication threshold to
one configured durable destination. The destination is a product record: a
create acknowledgement is not delivery proof. Minos accepts delivery only after
it discovers exactly one occurrence and reads the complete record back under
the configured authenticated principal.

## Installation boundary

Configure a named `[finding-destinations.<name>]` entry in `service.toml` with:

- `adaptation`: a trusted directory outside every PR workspace;
- `endpoint`: the provider endpoint passed to the adaptation;
- `credential-file`: a bounded service credential path; and
- `expected-principal`: the identity an authenticated read must report.

The adaptation directory contains three executable regular files named
`discover`, `read`, and `create`. Minos invokes them with that directory as the
working directory, a JSON request on standard input, and only these environment
variables:

```text
MINOS_FINDING_DESTINATION
MINOS_FINDING_TARGET
MINOS_FINDING_ENDPOINT
MINOS_FINDING_CREDENTIAL_FILE
```

The process must emit exactly one JSON value on standard output. Unknown fields,
trailing JSON, malformed output, duplicate discovery matches, an unauthenticated
read, or a full-record mismatch are integrity failures.

## Operations

`discover` receives the destination name, target, and `occurrence_id`. It returns
one of:

- `found` with exactly one `provider_record_id`, matching `occurrence_id`, and
  `payload_sha256`;
- `not-found` with no matches;
- `retryable` or `uncertain` with a stable `reason_code`.

`read` receives the provider record and occurrence identities. A `found` result
contains the complete durable record, `authenticated_principal`, and an RFC 3339
UTC `observed_at` time. Other outcomes contain no record evidence.

`create` receives the complete durable record. It returns `created` or
`already-exists` with a provider record identity, or `rejected`, `retryable`, or
`uncertain` with a stable reason code. Minos always discovers again and performs
an authenticated full read after create, including an uncertain create result.

Minos calls `create` only after a definitive `not-found` discovery and a fresh
configuration-and-lease authority check. An uncertain discovery never permits a
blind create. Retries begin with discovery, so one occurrence remains
idempotent even when the provider response was lost.

## Durable record and receipt

The complete record contains the verified finding, material and repair-policy
fields, publication and repair dispositions, and confirmed delivery state. Its
receipt binds:

```text
destination, target, occurrence_id, SHA-256(complete payload)
```

The service compares the authenticated read-back to this entire typed record.
Neither a provider ID, a create response, the attempt-local manifest, nor the
compact forge index is sufficient proof by itself.

Provider diagnostics must use stable reason codes and must not print secrets.
The adaptation runs outside the untrusted workspace, but its credential should
still be scoped to only the configured destination and operations.
