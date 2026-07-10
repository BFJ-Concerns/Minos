.PHONY: test build check scripts shellcheck e2e deployment-smoke

test:
	go test ./...

build:
	go build ./cmd/pump19

check: scripts shellcheck test build

scripts:
	@set -eu; \
	for script in scripts/adaptations/forgejo/* scripts/run-body/* scripts/review/*; do \
		[ -f "$$script" ] || continue; \
		sh -n "$$script"; \
	done
	@bash -n scripts/e2e/forgejo-smoke.sh scripts/e2e/deployment-smoke.sh scripts/e2e/resource-isolation-test.sh scripts/e2e/resources.sh
	@PYTHONDONTWRITEBYTECODE=1 python3 -m unittest scripts/e2e/test_capture_forward.py
	@sh -n scripts/e2e/review-engine-standin scripts/e2e/fix-engine-standin scripts/e2e/finish-engine-standin scripts/e2e/flaky-engine-standin
	@./scripts/e2e/resource-isolation-test.sh

shellcheck:
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck -x -s sh \
			scripts/run-body/* scripts/review/* \
			scripts/adaptations/forgejo/list-review-comments \
			scripts/adaptations/forgejo/list-reviews \
			scripts/adaptations/forgejo/post-review \
			scripts/adaptations/forgejo/post-comment \
			scripts/adaptations/forgejo/update-comment \
			scripts/adaptations/forgejo/commit-push \
			scripts/adaptations/forgejo/merge \
			scripts/adaptations/forgejo/get-pr-facts \
			scripts/adaptations/forgejo/add-reaction \
			scripts/adaptations/forgejo/remove-reaction \
			scripts/adaptations/forgejo/assign-if-missing \
			scripts/e2e/review-engine-standin \
			scripts/e2e/fix-engine-standin \
			scripts/e2e/finish-engine-standin \
			scripts/e2e/flaky-engine-standin; \
	else \
		echo "shellcheck unavailable; skipping static shell analysis"; \
	fi

e2e: build
	./scripts/e2e/forgejo-smoke.sh

deployment-smoke: build
	./scripts/e2e/deployment-smoke.sh
