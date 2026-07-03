# Pump-19

Pump-19 is an agent-native PR review-and-fix daemon. It watches opted-in
repositories, launches independent review, judge, fix and finish runs, and keeps
the control logic that enforces independence in a deterministic Rust core.

The current deployable binary is `pump19-daemon`. It is configured with external
adaptation packs for trigger rules, prompt/workflow content and mechanical source
preparation. A complete deployment example lives in
[`examples/deployment/`](examples/deployment/), with the generated baseline packs
checked in under [`examples/deployment/adaptations/`](examples/deployment/adaptations/).

## Build

```sh
cargo build --release -p pump19-daemon
```

The release binary is written to `target/release/pump19-daemon`.

## Deploy

Start with the deployment guide:

- [Deploy `pump19-daemon`](docs/deployment.md)

The shortest local smoke check is:

```sh
cargo build --release -p pump19-daemon
target/release/pump19-daemon examples/deployment/pump19-daemon.toml
```

The example deliberately contains `REPLACE_*` values. An unedited copy should
fail before polling or creating workspaces, with the exact field that still needs
an operator value.

## Test

```sh
cargo fmt --check
cargo clippy --all-targets --all-features
cargo test
```

The checked-in deployment packs are generated from the baseline writers in
`pump19-adaptations`; the test suite regenerates them in a temporary directory
and compares the result with `examples/deployment/adaptations/`.
