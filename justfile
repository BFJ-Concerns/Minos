# Minos task runner.
#
# `just` is the front door — run `just` (or `just --list`) to see every recipe.
# Don't have just? `cargo install just`, or your distro's package.

# List available recipes.
default:
    @just --list

# CI runs this same recipe, so the local gate and CI cannot drift apart.
# The gate: formatting, shell syntax, vet, tests and build.
verify: fmt-check shell-check
    go vet ./...
    go test ./...
    node --test workflows/*_test.mjs
    go build ./...

# Alias for `verify`, kept for muscle memory.
ci: verify

# Go sources are gofmt-clean. Names the offenders rather than failing bare.
[private]
fmt-check:
    #!/usr/bin/env bash
    set -euo pipefail
    unformatted="$(gofmt -l $(find cmd internal -name '*.go' -type f))"
    if [[ -n "$unformatted" ]]; then
        echo "gofmt would change these files:" >&2
        echo "$unformatted" >&2
        exit 1
    fi

# Every shipped shell script parses. Checked one at a time: `sh -n a b` stops
# at the first file, so a batch would hide a break in any later script.
[private]
shell-check:
    #!/usr/bin/env bash
    set -euo pipefail
    for script in scripts/install-review-runtime scripts/adaptations/forgejo/* scripts/run-body/*; do
        sh -n "$script" || exit 1
    done
