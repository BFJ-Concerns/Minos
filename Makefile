.PHONY: test build check scripts e2e deployment-smoke

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
	@bash -n scripts/e2e/forgejo-smoke.sh scripts/e2e/deployment-smoke.sh scripts/e2e/resource-isolation-test.sh scripts/e2e/resources.sh
	@PYTHONDONTWRITEBYTECODE=1 python3 -m unittest scripts/e2e/test_capture_forward.py
	@./scripts/e2e/resource-isolation-test.sh

e2e: build
	./scripts/e2e/forgejo-smoke.sh

deployment-smoke: build
	./scripts/e2e/deployment-smoke.sh
