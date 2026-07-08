.PHONY: test build check scripts e2e

test:
	go test ./...

build:
	go build ./cmd/pump19

check: scripts test build

scripts:
	find scripts/adaptations/forgejo -maxdepth 1 -type f -exec sh -n {} \;

e2e: build
	./scripts/e2e/forgejo-smoke.sh
