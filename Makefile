.PHONY: test build check scripts shellcheck e2e deployment-smoke

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
	@sh -n scripts/e2e/review-engine-standin scripts/e2e/fix-engine-standin scripts/e2e/finish-engine-standin scripts/e2e/flaky-engine-standin
	@./scripts/e2e/resource-isolation-test.sh

shellcheck:
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck -x -s sh scripts/run-body/* \
			scripts/adaptations/forgejo/list-review-comments \
			scripts/adaptations/forgejo/list-reviews \
			scripts/adaptations/forgejo/post-review \
			scripts/adaptations/forgejo/post-comment \
			scripts/adaptations/forgejo/post-review-comment \
			scripts/adaptations/forgejo/update-comment \
			scripts/adaptations/forgejo/commit-push \
			scripts/adaptations/forgejo/merge \
			scripts/adaptations/forgejo/get-pr-facts \
			scripts/adaptations/forgejo/get-combined-status \
			scripts/adaptations/forgejo/add-reaction \
			scripts/adaptations/forgejo/add-label \
			scripts/adaptations/forgejo/ensure-label-vocabulary \
			scripts/adaptations/forgejo/remove-reaction \
			scripts/adaptations/forgejo/assign-if-missing \
			scripts/e2e/materialise-calibration-case.sh \
			scripts/e2e/review-engine-standin \
			scripts/e2e/fix-engine-standin \
			scripts/e2e/finish-engine-standin \
			scripts/e2e/flaky-engine-standin; \
		for script in scripts/review/*; do \
			[ -f "$$script" ] || continue; \
			case "$$(head -n 1 "$$script")" in *sh) shellcheck -x -s sh "$$script" ;; esac; \
		done; \
	else \
		echo "shellcheck unavailable; skipping static shell analysis"; \
	fi

e2e: build
	./scripts/e2e/forgejo-smoke.sh

deployment-smoke: build
	./scripts/e2e/deployment-smoke.sh
