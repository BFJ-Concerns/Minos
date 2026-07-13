.PHONY: test build check scripts shellcheck e2e forgejo-conformance deployment-smoke

test:
	go test ./...

build:
	go build ./cmd/minos

check: scripts shellcheck test build

scripts:
	@set -eu; \
	for script in scripts/adaptations/forgejo/* scripts/run-body/* scripts/review/*; do \
		[ -f "$$script" ] || continue; \
		case "$$(head -n 1 "$$script")" in *sh) sh -n "$$script" ;; esac; \
	done
	@bash -n scripts/e2e/*.sh
	@PYTHONDONTWRITEBYTECODE=1 python3 -m unittest scripts/e2e/test_capture_forward.py scripts/review/test_soundness.py
	@./scripts/e2e/resource-isolation-test.sh

shellcheck:
	@set -eu; \
	if command -v shellcheck >/dev/null 2>&1; then \
		for script in scripts/run-body/* scripts/adaptations/forgejo/* scripts/review/* scripts/e2e/*.sh; do \
			[ -f "$$script" ] || continue; \
			# lifecycle-recovery is a sourced fragment whose caller-owned inputs \
			# are visible when lifecycle-smoke is checked with source following. \
			[ "$$script" != scripts/e2e/lifecycle-recovery.sh ] || continue; \
			case "$$(head -n 1 "$$script")" in *sh) shellcheck -x -P SCRIPTDIR "$$script" ;; esac; \
		done; \
	else \
		echo "shellcheck unavailable; skipping static shell analysis"; \
	fi

e2e: build
	./scripts/e2e/lifecycle-smoke.sh

forgejo-conformance:
	./scripts/e2e/forgejo-conformance.sh

deployment-smoke: build
	./scripts/e2e/deployment-smoke.sh
