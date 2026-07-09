.PHONY: test build check scripts e2e

test:
	go test ./...

build:
	go build ./cmd/pump19

check: scripts test build

scripts:
	@set -eu; \
	for script in scripts/adaptations/forgejo/*; do \
		[ -f "$$script" ] || continue; \
		sh -n "$$script"; \
	done

e2e: build
	./scripts/e2e/forgejo-smoke.sh
