package shell

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/product"
)

func TestForgejoAdmissionUsesFreshPullRequestSnapshot(t *testing.T) {
	t.Run("draft pull request is not started", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.changePullRequest(func(pullRequest map[string]any) {
			pullRequest["draft"] = true
			pullRequest["head"].(map[string]any)["ref"] = "structural/draft"
		})
		cfg, repo, facts := state.service(t)
		repo.StructuralBranchPrefixes = []string{"structural/"}

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Fatalf("draft pull request reached %s", name)
			return nil, nil
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "nothing" {
			t.Fatalf("result = %q, want nothing", result)
		}
	})

	t.Run("configured work-in-progress branch is deferred", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.changePullRequest(func(pullRequest map[string]any) {
			pullRequest["head"].(map[string]any)["ref"] = "structural/rework"
		})
		cfg, repo, facts := state.service(t)
		repo.WorkInProgressBranchPrefixes = []string{"structural/"}

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Fatalf("work-in-progress pull request reached %s", name)
			return nil, nil
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != `deferred: work-in-progress branch "structural/rework"` {
			t.Fatalf("result = %q", result)
		}
	})

	for _, test := range []struct {
		name      string
		branch    string
		prefixes  []string
		wantClass string
	}{
		{name: "structural branch starts a maintenance run", branch: "structural/rework", prefixes: []string{"structural/"}, wantClass: "maintenance"},
		{name: "ordinary branch starts a review run", branch: "feature/rework", prefixes: []string{"structural/"}, wantClass: "review"},
		{name: "empty head branch starts a review run", branch: "", prefixes: []string{"structural/"}, wantClass: "review"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			state.changePullRequest(func(pullRequest map[string]any) {
				pullRequest["head"].(map[string]any)["ref"] = test.branch
			})
			cfg, repo, facts := state.service(t)
			repo.StructuralBranchPrefixes = test.prefixes

			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			var commands, systemdArgs []string
			commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
				commands = append(commands, name)
				if name == "systemd-run" {
					systemdArgs = append([]string(nil), args...)
				}
				return nil, nil
			}

			result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
			if err != nil {
				t.Fatal(err)
			}
			if result.Decision != "started" || !slices.Equal(commands, []string{"systemctl", "systemd-run"}) {
				t.Fatalf("result = %q, commands = %v", result, commands)
			}
			if got := systemdEnvironment(t, systemdArgs)["MINOS_RUN_CLASS"]; got != test.wantClass {
				t.Fatalf("MINOS_RUN_CLASS = %q, want %q", got, test.wantClass)
			}
		})
	}

	t.Run("open dependency defers until the dependency closes", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.setDependencies([]map[string]any{{
			"number": 7, "state": "open",
			"repository": map[string]any{"full_name": "minos-e2e-owner/prerequisite"},
		}})
		cfg, repo, facts := state.service(t)
		repo.StructuralBranchPrefixes = []string{"structural/"}
		state.changePullRequest(func(pullRequest map[string]any) {
			pullRequest["head"].(map[string]any)["ref"] = "structural/blocked"
		})

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		var commands, systemdArgs []string
		commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
			commands = append(commands, name)
			if name == "systemd-run" {
				systemdArgs = append([]string(nil), args...)
			}
			return nil, nil
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "deferred: open dependencies: minos-e2e-owner/prerequisite#7" {
			t.Fatalf("result = %q", result)
		}
		if len(commands) != 0 {
			t.Fatalf("deferred pull request ran commands: %v", commands)
		}

		state.setDependencies([]map[string]any{{
			"number": 7, "state": "closed",
			"repository": map[string]any{"full_name": "minos-e2e-owner/prerequisite"},
		}})
		result, err = reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "started" || !slices.Equal(commands, []string{"systemctl", "systemd-run"}) {
			t.Fatalf("result after close = %q, commands = %v", result, commands)
		}
		if got := systemdEnvironment(t, systemdArgs)["MINOS_RUN_CLASS"]; got != "maintenance" {
			t.Fatalf("MINOS_RUN_CLASS = %q, want maintenance", got)
		}
	})

	t.Run("cross-repository dependency is carried by the snapshot and deferred", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.setDependencies([]map[string]any{{
			"number": 42, "state": "open",
			"repository": map[string]any{"full_name": "another-owner/another-repo"},
		}})
		cfg, repo, facts := state.service(t)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Fatalf("dependency-blocked pull request reached %s", name)
			return nil, nil
		}

		snapshot, err := currentSnapshot(t.Context(), cfg, facts)
		if err != nil {
			t.Fatal(err)
		}
		if !snapshot.DependenciesAvailable || len(snapshot.OpenDependencies) != 1 ||
			snapshot.OpenDependencies[0].Repository != "another-owner/another-repo" ||
			snapshot.OpenDependencies[0].Number != 42 {
			t.Fatalf("dependency snapshot = %#v", snapshot)
		}
		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "deferred: open dependencies: another-owner/another-repo#42" {
			t.Fatalf("result = %q", result)
		}
	})

	t.Run("open dependency on a later page is deferred", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.setDependencyPages([][]map[string]any{
			{{
				"number": 7, "state": "closed",
				"repository": map[string]any{"full_name": "minos-e2e-owner/closed-first"},
			}},
			{{
				"number": 8, "state": "open",
				"repository": map[string]any{"full_name": "minos-e2e-owner/open-second"},
			}},
		})
		cfg, repo, facts := state.service(t)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Fatalf("dependency-blocked pull request reached %s", name)
			return nil, nil
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "deferred: open dependencies: minos-e2e-owner/open-second#8" {
			t.Fatalf("result = %q", result)
		}
	})

	t.Run("unreadable dependencies defer with visible uncertainty", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.setDependenciesFailure(http.StatusServiceUnavailable)
		cfg, repo, facts := state.service(t)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Fatalf("dependency-uncertain pull request reached %s", name)
			return nil, nil
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "deferred: dependency state unavailable: forge returned HTTP 503" {
			t.Fatalf("result = %q", result)
		}
	})

	t.Run("Minos review with a missing terminal status is repaired without a new run", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.setDependencies([]map[string]any{{
			"number": 7, "state": "open",
			"repository": map[string]any{"full_name": "minos-e2e-owner/prerequisite"},
		}})
		state.setReviews([]map[string]any{{
			"id": 41, "state": "APPROVED", "commit_id": state.headSHA(),
			"body": "ordinary review", "user": map[string]any{"login": "Minos"},
		}})
		cfg, repo, facts := state.service(t)
		state.setStatuses([]map[string]any{{
			"id": 7, "context": "Minos", "status": "success", "description": "Changes approved",
			"target_url": state.server.URL + "/minos-e2e-owner/subject/pulls/2#minos-target-elsewhere",
			"creator":    map[string]any{"login": "Minos"},
		}})

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Fatalf("reviewed pull request reached %s", name)
			return nil, nil
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "recovered" {
			t.Fatalf("result = %q, want recovered", result)
		}
		result, err = reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "nothing" {
			t.Fatalf("result after repair = %q, want nothing", result)
		}
		statusWrites, repairedTarget := state.statusWriteFacts()
		if statusWrites != 1 {
			t.Fatalf("status writes = %d, want one", statusWrites)
		}
		facts.BaseSHA = state.targetSHA()
		if repairedTarget != statusTargetURL(cfg.Forges["forgejo"].APIBase, facts) {
			t.Fatalf("repaired status target = %q, want this pull request and target", repairedTarget)
		}
	})

	t.Run("another pull request status on the same head does not suppress this pull request", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, facts := state.service(t)
		state.setStatuses([]map[string]any{{
			"id": 7, "context": "Minos", "status": "success", "description": "Changes approved",
			"target_url": state.server.URL + "/minos-e2e-owner/subject/pulls/2#minos-target-elsewhere",
			"creator":    map[string]any{"login": "Minos"},
		}})

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		var started bool
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			switch name {
			case "systemctl":
				return nil, nil
			case "systemd-run":
				started = true
				return nil, nil
			default:
				t.Fatalf("unexpected command %q", name)
				return nil, nil
			}
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "started" || !started {
			t.Fatalf("result = %q, started = %t; want an actual start", result, started)
		}
	})

	t.Run("this pull request status on the current target suppresses a new run", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, facts := state.service(t)
		state.setStatuses([]map[string]any{{
			"id": 7, "context": "Minos", "status": "success", "description": "Changes approved",
			"target_url": statusTargetURL(cfg.Forges["forgejo"].APIBase, Facts{
				Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo, PR: facts.PR, BaseSHA: state.targetSHA(),
			}),
			"creator": map[string]any{"login": "Minos"},
		}, {
			"id": 8, "context": "Minos", "status": "pending", "description": product.Working().Description(),
			"target_url": state.server.URL + "/minos-e2e-owner/subject/pulls/2#minos-target-" + state.targetSHA(),
			"creator":    map[string]any{"login": "Minos"},
		}})

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Fatalf("completed pull request reached %s", name)
			return nil, nil
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "nothing" {
			t.Fatalf("result = %q, want nothing", result)
		}
	})

	t.Run("member terminal publication leaves the next pass completed", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		configureForgeCommandFixture(t, state)
		cfg, repo, facts := state.service(t)
		directory := t.TempDir()
		bodyPath := filepath.Join(directory, "body.md")
		commentsPath := filepath.Join(directory, "comments.json")
		if err := os.WriteFile(bodyPath, []byte("Confirmed findings in the reviewed code.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(commentsPath, []byte("[]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		prefix := []string{"--member", facts.Owner, facts.Repo, facts.PR}
		if err := ForgeCommand(t.Context(), append(prefix,
			"review", state.headSHA(), state.targetSHA(), "request-changes", bodyPath, commentsPath,
		), &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if err := ForgeCommand(t.Context(), append(prefix,
			"status", state.headSHA(), state.targetSHA(), "attention",
		), &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if writes, payload := state.reviewWriteFacts(); writes != 1 || payload["event"] != "REQUEST_CHANGES" {
			t.Fatalf("review writes = %d, payload = %#v", writes, payload)
		}
		if writes, _ := state.statusWriteFacts(); writes != 1 {
			t.Fatalf("status writes = %d, want one terminal status", writes)
		}

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Fatalf("completed member-shaped pull request reached %s", name)
			return nil, nil
		}
		for pass := 1; pass <= 2; pass++ {
			result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
			if err != nil {
				t.Fatal(err)
			}
			if result.Decision != "nothing" {
				t.Fatalf("pass %d result = %q, want nothing", pass, result)
			}
		}
	})

	t.Run("incomplete pull request with a comment review starts a fresh attempt", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.setReviews([]map[string]any{{
			"id": 41, "state": "COMMENT", "commit_id": state.headSHA(),
			"body": "Implemented repairs for confirmed findings.",
			"user": map[string]any{"login": "Minos"},
		}})
		state.setStatuses([]map[string]any{{
			"id": 7, "context": "Minos", "status": "error", "description": "Review incomplete",
			"target_url": state.server.URL + "/minos-e2e-owner/subject/pulls/1#minos-target-" + state.targetSHA(),
			"creator":    map[string]any{"login": "Minos"},
		}})
		cfg, repo, facts := state.service(t)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		var started bool
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			switch name {
			case "systemctl":
				return nil, nil
			case "systemd-run":
				started = true
				return nil, nil
			default:
				t.Fatalf("unexpected command %q", name)
				return nil, nil
			}
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "started" || !started {
			t.Fatalf("result = %q, started = %t; want a fresh attempt", result, started)
		}
	})

	t.Run("continuation status leaves the pull request eligible", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.setStatuses([]map[string]any{{
			"id": 7, "context": "Minos", "status": "pending", "description": product.Continuation().Description(),
			"target_url": state.server.URL + "/minos-e2e-owner/subject/pulls/1#minos-target-" + state.targetSHA(),
			"creator":    map[string]any{"login": "Minos"},
		}})
		cfg, repo, facts := state.service(t)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		var started bool
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			switch name {
			case "systemctl":
				return nil, nil
			case "systemd-run":
				started = true
				return nil, nil
			default:
				t.Fatalf("unexpected command %q", name)
				return nil, nil
			}
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "started" || !started {
			t.Fatalf("result = %q, started = %t; want a successor start", result, started)
		}
	})

	t.Run("virtual pull ref starts branchless", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.changePullRequest(func(pullRequest map[string]any) {
			pullRequest["head"].(map[string]any)["ref"] = "refs/pull/1/head"
		})
		cfg, repo, facts := state.service(t)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		var systemdArgs []string
		commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "systemctl" {
				return nil, nil
			}
			if name == "systemd-run" {
				systemdArgs = append([]string(nil), args...)
				return nil, nil
			}
			t.Fatalf("unexpected command %q", name)
			return nil, nil
		}

		snapshot, err := currentSnapshot(t.Context(), cfg, facts)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.HeadBranch != "" {
			t.Fatalf("head branch = %q, want virtual ref to be branchless", snapshot.HeadBranch)
		}
		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "started" {
			t.Fatalf("result = %q, want started", result)
		}
		assertArgument(t, systemdArgs, "MINOS_HEAD_BRANCH=")
		if state.virtualBranchReads() != 0 {
			t.Fatalf("virtual pull ref caused %d branch reads", state.virtualBranchReads())
		}
	})

	t.Run("active pull request unit is suppressed", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, facts := state.service(t)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		var commands []string
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			commands = append(commands, name)
			return []byte("minos-run-other-repo-pr9.service loaded active running Minos lead\n"), nil
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "suppressed" {
			t.Fatalf("result = %q, want suppressed", result)
		}
		if result.BlockingUnit != "minos-run-other-repo-pr9.service" {
			t.Fatalf("blocking unit = %q", result.BlockingUnit)
		}
		if !slices.Equal(commands, []string{"systemctl"}) {
			t.Fatalf("commands = %v, want only active-unit check", commands)
		}
	})

	t.Run("a pull request beyond the configured concurrency waits for a lead to exit", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, firstFacts := state.service(t)
		cfg.Runs.MaxConcurrent = 2

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		var activeUnits []string
		var startedUnits []string
		commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "systemctl":
				listing := ""
				for _, unit := range activeUnits {
					listing += unit + " loaded active running Minos lead\n"
				}
				return []byte(listing), nil
			case "systemd-run":
				unitFlag := slices.Index(args, "--unit")
				if unitFlag < 0 || unitFlag+1 >= len(args) {
					t.Fatalf("systemd-run arguments omit unit: %v", args)
				}
				activeUnits = append(activeUnits, args[unitFlag+1])
				startedUnits = append(startedUnits, args[unitFlag+1])
				return nil, nil
			default:
				t.Fatalf("unexpected command %q", name)
				return nil, nil
			}
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, firstFacts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "started" {
			t.Fatalf("first result = %q, want started", result)
		}

		state.changePullRequest(func(pullRequest map[string]any) { pullRequest["number"] = float64(2) })
		_, _, secondFacts := state.service(t)
		secondFacts.HeadSHA = ""
		result, err = reconcilePullRequest(t.Context(), cfg, repo, secondFacts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "started" {
			t.Fatalf("second result within the configured concurrency = %q, want started", result)
		}

		state.changePullRequest(func(pullRequest map[string]any) { pullRequest["number"] = float64(3) })
		_, _, thirdFacts := state.service(t)
		thirdFacts.HeadSHA = ""
		result, err = reconcilePullRequest(t.Context(), cfg, repo, thirdFacts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "suppressed" {
			t.Fatalf("third result while both leads active = %q, want suppressed", result)
		}
		if result.BlockingUnit != activeUnits[0] {
			t.Fatalf("blocking unit = %q, want %q", result.BlockingUnit, activeUnits[0])
		}
		if len(startedUnits) != 2 {
			t.Fatalf("started units while both leads active = %v, want two", startedUnits)
		}

		activeUnits = activeUnits[1:]
		result, err = reconcilePullRequest(t.Context(), cfg, repo, thirdFacts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != "started" {
			t.Fatalf("third result after a lead exited = %q, want started", result)
		}
		if len(startedUnits) != 3 || !strings.Contains(startedUnits[2], "pr3") {
			t.Fatalf("started units = %v, want third pull request unit", startedUnits)
		}
	})
}

func TestForgejoOperatorListsCurrentHoldsAndBlockedVerdicts(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, _ := state.service(t)
	writeOperatorFixtureConfig(t, cfg)
	state.setOperatorPullRequests([]operatorFixturePullRequest{
		{
			pullRequest: state.operatorPullRequest(1, "held-head"),
			statuses:    []map[string]any{heldFixtureStatus(1, state.server.URL, "held-head", state.targetSHA(), currentEnvironmentStamp(cfg))},
			comments: []map[string]any{{
				"id": float64(1), "body": "Held at: finishing\nTarget checks are failing.", "user": map[string]any{"login": "Minos"},
			}},
		},
		{
			pullRequest: state.operatorPullRequest(2, "blocked-head"),
			reviews: []map[string]any{{
				"id": float64(2), "state": "REQUEST_CHANGES", "commit_id": "blocked-head", "user": map[string]any{"login": "Minos"},
				"body": "Required checks remain red.\n\n<!-- Minos: cause=required-checks head=blocked-head target=" + state.targetSHA() + " -->",
			}},
		},
		{pullRequest: state.operatorPullRequest(3, "ordinary-head")},
		{
			pullRequest: state.operatorPullRequest(4, "released-hold-head"),
			statuses:    []map[string]any{heldFixtureStatus(4, state.server.URL, "released-hold-head", "superseded-target", currentEnvironmentStamp(cfg))},
			comments: []map[string]any{{
				"id": float64(4), "body": "Held at: review\nThe target changed after this hold.", "user": map[string]any{"login": "Minos"},
			}},
		},
		{
			pullRequest: state.operatorPullRequest(5, "released-verdict-head"),
			reviews: []map[string]any{{
				"id": float64(5), "state": "REQUEST_CHANGES", "commit_id": "released-verdict-head", "user": map[string]any{"login": "Minos"},
				"body": "Required checks were red before the target moved.\n\n<!-- Minos: cause=required-checks head=released-verdict-head target=superseded-target -->",
			}},
		},
	})

	var output bytes.Buffer
	if err := OperatorCommand(t.Context(), []string{"held", "--config", cfg.Root}, &output); err != nil {
		t.Fatal(err)
	}
	var listed []HeldPullRequest
	if err := json.Unmarshal(output.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	want := []HeldPullRequest{
		{Forge: "forgejo", Owner: "minos-e2e-owner", Repo: "subject", PullRequest: "1", Class: "held", Stage: "finishing", Reason: "Target checks are failing."},
		{Forge: "forgejo", Owner: "minos-e2e-owner", Repo: "subject", PullRequest: "2", Class: "required-checks", Reason: "Required checks remain red."},
	}
	if !reflect.DeepEqual(listed, want) {
		t.Fatalf("held pull requests = %#v, want %#v", listed, want)
	}

	binary := filepath.Join(t.TempDir(), "minos")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/minos")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build minos CLI: %v\n%s", err, output)
	}
	binaryContent, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	binaryStamp := fmt.Sprintf("%x", sha256.Sum256(binaryContent))[:12]
	target := state.targetSHA()
	state.mu.Lock()
	state.operatorPullRequests[0].statuses = []map[string]any{heldFixtureStatus(1, state.server.URL, "held-head", target, binaryStamp)}
	state.mu.Unlock()
	cliOutput, err := exec.Command(binary, "operator", "held", "--config", cfg.Root).CombinedOutput()
	if err != nil {
		t.Fatalf("run minos operator held: %v\n%s", err, cliOutput)
	}
	listed = nil
	if err := json.Unmarshal(cliOutput, &listed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(listed, want) {
		t.Fatalf("CLI held pull requests = %#v, want %#v", listed, want)
	}
}

func TestForgejoOperatorDoesNotListReleasedHold(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, _ := state.service(t)
	writeOperatorFixtureConfig(t, cfg)
	state.setOperatorPullRequests([]operatorFixturePullRequest{{
		pullRequest: state.operatorPullRequest(1, "released-hold-head"),
		statuses:    []map[string]any{heldFixtureStatus(1, state.server.URL, "released-hold-head", "superseded-target", currentEnvironmentStamp(cfg))},
		comments: []map[string]any{{
			"id": float64(1), "body": "Held at: review\nThe target changed after this hold.", "user": map[string]any{"login": "Minos"},
		}},
	}})

	var output bytes.Buffer
	if err := OperatorCommand(t.Context(), []string{"held", "--config", cfg.Root}, &output); err != nil {
		t.Fatal(err)
	}
	var listed []HeldPullRequest
	if err := json.Unmarshal(output.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(listed, []HeldPullRequest{}) {
		t.Fatalf("released hold listed = %#v, want empty", listed)
	}
}

func TestForgejoOperatorDoesNotListStaleEnvironmentHold(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, _ := state.service(t)
	writeOperatorFixtureConfig(t, cfg)
	state.setOperatorPullRequests([]operatorFixturePullRequest{{
		pullRequest: state.operatorPullRequest(1, "stale-environment-hold-head"),
		statuses:    []map[string]any{heldFixtureStatus(1, state.server.URL, "stale-environment-hold-head", state.targetSHA(), "stale-environment-stamp")},
		comments: []map[string]any{{
			"id": float64(1), "body": "Held at: review\nThe run environment changed after this hold.", "user": map[string]any{"login": "Minos"},
		}},
	}})

	var output bytes.Buffer
	if err := OperatorCommand(t.Context(), []string{"held", "--config", cfg.Root}, &output); err != nil {
		t.Fatal(err)
	}
	var listed []HeldPullRequest
	if err := json.Unmarshal(output.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(listed, []HeldPullRequest{}) {
		t.Fatalf("stale environment hold listed = %#v, want empty", listed)
	}
}

func TestForgejoOperatorForceUsesSpawnRunAdmission(t *testing.T) {
	for _, test := range []struct {
		name       string
		activeUnit string
		maxRuns    int
		wantUnit   string
		wantStart  bool
	}{
		{name: "fresh run starts through ordinary admission", maxRuns: 1, wantStart: true},
		{name: "duplicate unit is refused", activeUnit: "minos-run-minos-e2e-owner-subject-pr1.service", maxRuns: 2, wantUnit: "minos-run-minos-e2e-owner-subject-pr1.service"},
		{name: "full concurrency cap is refused", activeUnit: "minos-run-other-repository-pr9.service", maxRuns: 1, wantUnit: "minos-run-other-repository-pr9.service"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			cfg, _, facts := state.service(t)
			cfg.Runs.MaxConcurrent = test.maxRuns
			writeOperatorFixtureConfig(t, cfg)

			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			starts := 0
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				switch name {
				case "systemctl":
					if test.activeUnit == "" {
						return nil, nil
					}
					return []byte(test.activeUnit + " loaded active running Minos lead\n"), nil
				case "systemd-run":
					starts++
					return nil, nil
				default:
					t.Fatalf("unexpected command %q", name)
					return nil, nil
				}
			}

			var output bytes.Buffer
			err := OperatorCommand(t.Context(), []string{"force", "--config", cfg.Root, facts.Forge, facts.Owner, facts.Repo, facts.PR}, &output)
			if test.wantStart {
				if err != nil || starts != 1 || !strings.Contains(output.String(), `"Outcome":"started"`) {
					t.Fatalf("force result = %v, starts = %d, output = %s", err, starts, output.String())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantUnit) {
				t.Fatalf("force error = %v, want refusal naming %s", err, test.wantUnit)
			}
			if starts != 0 {
				t.Fatalf("force started %d units despite admission refusal", starts)
			}
		})
	}
}

func TestForgejoOperatorForcePreservesEligibilityDeferrals(t *testing.T) {
	t.Run("work-in-progress branch", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.changePullRequest(func(pullRequest map[string]any) {
			pullRequest["head"].(map[string]any)["ref"] = "wip/rework"
		})
		cfg, _, facts := state.service(t)
		writeOperatorFixtureConfig(t, cfg)
		repoConfigPath := filepath.Join(cfg.Root, "repos", "subject.toml")
		repoConfig, err := os.ReadFile(repoConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		repoConfig = bytes.Replace(repoConfig, []byte("repo = \"subject\"\n"), []byte("repo = \"subject\"\nwork-in-progress-branch-prefixes = [\"wip/\"]\n"), 1)
		if err := os.WriteFile(repoConfigPath, repoConfig, 0o600); err != nil {
			t.Fatal(err)
		}

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Fatalf("work-in-progress pull request reached %s", name)
			return nil, nil
		}

		var output bytes.Buffer
		err = OperatorCommand(t.Context(), []string{"force", "--config", cfg.Root, facts.Forge, facts.Owner, facts.Repo, facts.PR}, &output)
		if err == nil || !strings.Contains(err.Error(), `force refused: work-in-progress branch "wip/rework"`) {
			t.Fatalf("force error = %v", err)
		}
	})

	for _, test := range []struct {
		name       string
		configure  func(*forgejoFixtureState)
		wantReason string
	}{
		{
			name: "open dependency",
			configure: func(state *forgejoFixtureState) {
				state.setDependencies([]map[string]any{{
					"number": 7, "state": "open",
					"repository": map[string]any{"full_name": "minos-e2e-owner/prerequisite"},
				}})
			},
			wantReason: "force refused: open dependencies: minos-e2e-owner/prerequisite#7",
		},
		{
			name: "unavailable dependency state",
			configure: func(state *forgejoFixtureState) {
				state.setDependenciesFailure(http.StatusServiceUnavailable)
			},
			wantReason: "force refused: dependency state unavailable: forge returned HTTP 503",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			test.configure(state)
			cfg, _, facts := state.service(t)
			writeOperatorFixtureConfig(t, cfg)

			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				t.Fatalf("dependency-deferred pull request reached %s", name)
				return nil, nil
			}

			var output bytes.Buffer
			err := OperatorCommand(t.Context(), []string{"force", "--config", cfg.Root, facts.Forge, facts.Owner, facts.Repo, facts.PR}, &output)
			if err == nil || !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("force error = %v", err)
			}
		})
	}
}

func TestForgejoOperatorForceCarriesReleasedHoldContext(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, facts := state.service(t)
	state.setStatuses([]map[string]any{heldFixtureStatus(7, state.server.URL, state.headSHA(), "earlier-target", currentEnvironmentStamp(cfg))})
	state.setIssueComments([]map[string]any{{
		"id": float64(9), "body": "Held at: finishing\nTarget tests fail.", "user": map[string]any{"login": "Minos"},
	}})
	writeOperatorFixtureConfig(t, cfg)

	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	var systemdArgs []string
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemd-run" {
			systemdArgs = append([]string(nil), args...)
		}
		return nil, nil
	}

	var output bytes.Buffer
	if err := OperatorCommand(t.Context(), []string{"force", "--config", cfg.Root, facts.Forge, facts.Owner, facts.Repo, facts.PR}, &output); err != nil {
		t.Fatal(err)
	}
	environment := systemdEnvironment(t, systemdArgs)
	for key, want := range map[string]string{
		"MINOS_RELEASED_HOLD_HEAD":      state.headSHA(),
		"MINOS_RELEASED_HOLD_STAGE":     "finishing",
		"MINOS_RELEASED_HOLD_DIAGNOSIS": "Target tests fail.",
	} {
		if got := environment[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestForgejoCheckCausedVerdictSpending(t *testing.T) {
	for _, test := range []struct {
		name         string
		cause        string
		moveTarget   bool
		seedStatus   bool
		wantDecision ReconcileDecision
		wantStarts   int
		wantStatuses int
	}{
		{name: "moved target spends check-caused verdict", cause: product.RecordCauseRequiredChecks, moveTarget: true, seedStatus: true, wantDecision: SpawnStarted, wantStarts: 1},
		{name: "unchanged target preserves check-caused verdict", cause: product.RecordCauseRequiredChecks, wantDecision: ReconcileRecovered, wantStatuses: 1},
		{name: "moved target preserves findings-caused verdict", moveTarget: true, seedStatus: true, wantDecision: ReconcileRecovered, wantStatuses: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			reviewedTarget := state.targetSHA()
			if test.moveTarget {
				state.changePullRequest(func(pullRequest map[string]any) {
					pullRequest["base"].(map[string]any)["sha"] = "moved-target"
				})
			}
			recordValues := map[string]string{"head": state.headSHA(), "target": reviewedTarget}
			if test.cause != "" {
				recordValues[product.RecordCauseKey] = test.cause
			}
			record, err := product.FormatRecord(recordValues)
			if err != nil {
				t.Fatal(err)
			}
			state.setReviews([]map[string]any{{
				"id": 41, "state": "REQUEST_CHANGES", "commit_id": state.headSHA(),
				"body": "Terminal review.\n\n" + record, "user": map[string]any{"login": "Minos"},
			}})
			if test.seedStatus {
				state.setStatuses([]map[string]any{{
					"id": 7, "context": "Minos", "status": "failure", "description": product.Attention().Description(),
					"target_url": state.server.URL + "/minos-e2e-owner/subject/pulls/1#minos-target-" + reviewedTarget,
					"creator":    map[string]any{"login": "Minos"},
				}})
			}
			cfg, repo, facts := state.service(t)

			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			starts := 0
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name == "systemd-run" {
					starts++
				}
				return nil, nil
			}

			result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
			if err != nil {
				t.Fatal(err)
			}
			if result.Decision != test.wantDecision || starts != test.wantStarts {
				t.Fatalf("result = %q, starts = %d; want %q and %d", result.Decision, starts, test.wantDecision, test.wantStarts)
			}
			statusWrites, _ := state.statusWriteFacts()
			if statusWrites != test.wantStatuses {
				t.Fatalf("status writes = %d, want %d", statusWrites, test.wantStatuses)
			}
		})
	}
}

func TestForgejoAdmissionCarriesReleasedHoldContextIntoSpawn(t *testing.T) {
	for _, test := range []struct {
		name          string
		comment       string
		moveHead      bool
		wantHead      bool
		wantStage     string
		wantDiagnosis string
	}{
		{
			name:     "stale target hold resumes finishing",
			comment:  "Held at: finishing\nTarget tests fail because the base branch lacks the fixture.",
			wantHead: true, wantStage: "finishing",
			wantDiagnosis: "Target tests fail because the base branch lacks the fixture.",
		},
		{name: "absent held comment starts fresh"},
		{name: "malformed held stage starts fresh", comment: "Held at: merge\nTarget tests fail."},
		{name: "moved admitted head starts fresh", comment: "Held at: review\nTarget tests fail.", moveHead: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			heldHead := state.headSHA()
			state.setStatuses([]map[string]any{heldFixtureStatus(7, state.server.URL, heldHead, "earlier-target", "")})
			if test.comment != "" {
				state.setIssueComments([]map[string]any{{"id": float64(9), "body": test.comment, "user": map[string]any{"login": "Minos"}}})
			}
			if test.moveHead {
				state.changePullRequest(func(pullRequest map[string]any) {
					pullRequest["head"].(map[string]any)["sha"] = "moved-admitted-head"
				})
			}
			cfg, repo, facts := state.service(t)

			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			var systemdArgs []string
			commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "systemd-run" {
					systemdArgs = append([]string(nil), args...)
				}
				return nil, nil
			}

			result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
			if err != nil {
				t.Fatal(err)
			}
			if result.Decision != SpawnStarted {
				t.Fatalf("decision = %q, want started", result.Decision)
			}
			environment := systemdEnvironment(t, systemdArgs)
			wantHead := ""
			if test.wantHead {
				wantHead = heldHead
			}
			for key, want := range map[string]string{
				"MINOS_RELEASED_HOLD_HEAD":      wantHead,
				"MINOS_RELEASED_HOLD_STAGE":     test.wantStage,
				"MINOS_RELEASED_HOLD_DIAGNOSIS": test.wantDiagnosis,
			} {
				got, present := environment[key]
				if !present || got != want {
					t.Fatalf("%s = %q, want %q", key, got, want)
				}
			}
			if test.moveHead && !slices.Equal(state.statusReadFacts(), []string{"moved-admitted-head"}) {
				t.Fatalf("status reads = %v, want only moved admitted head", state.statusReadFacts())
			}
		})
	}
}

func TestForgejoCurrentTargetHoldIsNotAReleasedHold(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, facts := state.service(t)
	stamp := currentEnvironmentStamp(cfg)
	state.setStatuses([]map[string]any{heldFixtureStatus(7, state.server.URL, state.headSHA(), state.targetSHA(), stamp)})
	state.setIssueComments([]map[string]any{{
		"id": float64(9), "body": "Held at: finishing\nTarget tests fail.", "user": map[string]any{"login": "Minos"},
	}})
	adapter, snapshot, err := currentForgeSnapshot(t.Context(), cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	commits, err := adapter.PullRequestCommits(t.Context(), forge.Repository{Owner: facts.Owner, Name: facts.Repo}, 1)
	if err != nil {
		t.Fatal(err)
	}
	context := releasedHoldContext(t.Context(), adapter, snapshot, forge.Repository{Owner: facts.Owner, Name: facts.Repo}, 1, cfg.Service.BotLogin, commits, stamp)
	if context != (AdmissionContext{}) {
		t.Fatalf("released hold context = %#v, want empty", context)
	}
}

func TestForgejoStaleEnvironmentHoldIsAReleasedHold(t *testing.T) {
	for _, test := range []struct {
		name  string
		stamp string
	}{
		{name: "hold bound to an older deploy", stamp: "aaaaaaaaaaaa"},
		{name: "legacy hold with no environment binding", stamp: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			cfg, _, facts := state.service(t)
			state.setStatuses([]map[string]any{heldFixtureStatus(7, state.server.URL, state.headSHA(), state.targetSHA(), test.stamp)})
			state.setIssueComments([]map[string]any{{
				"id": float64(9), "body": "Held at: finishing\nSocket path overflows in the run environment.", "user": map[string]any{"login": "Minos"},
			}})
			adapter, snapshot, err := currentForgeSnapshot(t.Context(), cfg, facts)
			if err != nil {
				t.Fatal(err)
			}
			commits, err := adapter.PullRequestCommits(t.Context(), forge.Repository{Owner: facts.Owner, Name: facts.Repo}, 1)
			if err != nil {
				t.Fatal(err)
			}
			context := releasedHoldContext(t.Context(), adapter, snapshot, forge.Repository{Owner: facts.Owner, Name: facts.Repo}, 1, cfg.Service.BotLogin, commits, currentEnvironmentStamp(cfg))
			if context.ReleasedHoldStage != "finishing" || context.ReleasedHoldDiagnosis == "" {
				t.Fatalf("released hold context = %#v, want finishing release with diagnosis", context)
			}
		})
	}
}

func TestForgejoCompletionMarkersFollowForeignCommitProvenance(t *testing.T) {
	for _, test := range []struct {
		name         string
		movement     []map[string]any
		wantDecision ReconcileDecision
		wantStarts   int
	}{
		{
			name:         "Minos-only movement preserves completion",
			movement:     []map[string]any{{"sha": "witness", "author": map[string]any{"login": "contributor"}}, {"sha": "current", "author": map[string]any{"login": "Minos"}}},
			wantDecision: ReconcileNothing,
		},
		{
			name:         "mixed movement spends completion",
			movement:     []map[string]any{{"sha": "witness", "author": map[string]any{"login": "contributor"}}, {"sha": "foreign", "author": map[string]any{"login": "contributor"}}, {"sha": "current", "author": map[string]any{"login": "Minos"}}},
			wantDecision: SpawnStarted, wantStarts: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			state.changePullRequest(func(pullRequest map[string]any) { pullRequest["head"].(map[string]any)["sha"] = "witness" })
			state.setStatuses([]map[string]any{{
				"id": float64(7), "status": "success", "context": "Minos", "creator": map[string]any{"login": "Minos"},
				"description": product.Clean().Description(), "target_url": fmt.Sprintf("%s/minos-e2e-owner/subject/pulls/1#minos-target-%s", state.server.URL, state.targetSHA()),
			}})
			state.changePullRequest(func(pullRequest map[string]any) { pullRequest["head"].(map[string]any)["sha"] = "current" })
			state.setCommits(test.movement)
			cfg, repo, facts := state.service(t)
			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			starts := 0
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name == "systemd-run" {
					starts++
				}
				return nil, nil
			}
			result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
			if err != nil {
				t.Fatal(err)
			}
			if result.Decision != test.wantDecision || starts != test.wantStarts {
				t.Fatalf("decision = %q, starts = %d; want %q, %d", result.Decision, starts, test.wantDecision, test.wantStarts)
			}
		})
	}
}

func TestForgejoHeadMovementCommandClassifiesTheWholeCommitInterval(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) { pullRequest["head"].(map[string]any)["sha"] = "current" })
	cfg, _, _ := state.service(t)
	cfg.Service.BotLogin = "minos-bot"
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")
	t.Setenv("MINOS_GIT_AUTHOR_NAME", "Minos")
	for _, test := range []struct {
		name, middle, want string
	}{
		{name: "own movement", middle: "minos-bot", want: "own"},
		{name: "mixed movement", middle: "contributor", want: "foreign"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state.setCommits([]map[string]any{
				{"sha": "witness", "author": map[string]any{"login": "contributor"}},
				{"sha": "middle", "author": map[string]any{"login": test.middle}},
				{"sha": "current", "author": map[string]any{"login": "minos-bot"}},
			})
			var output bytes.Buffer
			if err := ForgeCommand(t.Context(), []string{"head-movement", "witness", "current"}, &output); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(output.String()); got != test.want {
				t.Fatalf("movement = %q, want %q", got, test.want)
			}
		})
	}
}

func TestForgejoReleasedHoldSurvivesMinosOnlyHeadMovement(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) { pullRequest["head"].(map[string]any)["sha"] = "held" })
	state.setStatuses([]map[string]any{heldFixtureStatus(7, state.server.URL, "held", "earlier-target", "")})
	state.setIssueComments([]map[string]any{{"id": float64(9), "body": "Held at: review\nThe target lacked the required fixture.", "user": map[string]any{"login": "Minos"}}})
	state.changePullRequest(func(pullRequest map[string]any) { pullRequest["head"].(map[string]any)["sha"] = "current" })
	state.setCommits([]map[string]any{{"sha": "held", "author": map[string]any{"login": "contributor"}}, {"sha": "current", "author": map[string]any{"login": "Minos"}}})
	cfg, repo, facts := state.service(t)
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	var systemdArgs []string
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemd-run" {
			systemdArgs = append([]string(nil), args...)
		}
		return nil, nil
	}
	result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != SpawnStarted {
		t.Fatalf("decision = %q", result.Decision)
	}
	environment := systemdEnvironment(t, systemdArgs)
	if environment["MINOS_RELEASED_HOLD_HEAD"] != "held" || environment["MINOS_RELEASED_HOLD_STAGE"] != "review" {
		t.Fatalf("released hold environment = %#v", environment)
	}
}

func heldFixtureStatus(id int, apiBase, head, target, envStamp string) map[string]any {
	targetURL := fmt.Sprintf("%s/minos-e2e-owner/subject/pulls/1#minos-target-%s", apiBase, target)
	if envStamp != "" {
		targetURL += "+minos-env-" + envStamp
	}
	return map[string]any{
		"id": float64(id), "status": "pending", "context": "Minos",
		"creator": map[string]any{"login": "Minos"}, "description": product.Held().Description(),
		"target_url": targetURL,
		"sha":        head,
	}
}

func TestForgejoSweepMeasuresPullRequestSnapshotReadsPerPass(t *testing.T) {
	tests := []struct {
		name         string
		dependencies []map[string]any
		wantStarted  bool
	}{
		{name: "started", dependencies: []map[string]any{}, wantStarted: true},
		{name: "deferred", dependencies: []map[string]any{{
			"number": 7, "state": "open",
			"repository": map[string]any{"full_name": "minos-e2e-owner/prerequisite"},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			second := stackedFixturePull(2, "main", "second-candidate")
			second["draft"] = true
			state.setStackedChildren([]map[string]any{second})
			state.setDependencies(test.dependencies)
			cfg := writeSweepFixtureConfig(t, state)

			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			var commands []string
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				commands = append(commands, name)
				return nil, nil
			}

			if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
				t.Fatal(err)
			}
			got := state.pullRequestSnapshotReads("1")
			t.Logf("pull request 1 snapshot reads this pass = %d", got)
			if got != 1 {
				t.Fatalf("pull request 1 snapshot reads this pass = %d, want one", got)
			}
			if got := state.pullRequestSnapshotReads("2"); got != 1 {
				t.Fatalf("pull request 2 snapshot reads this pass = %d, want one", got)
			}
			started := slices.Contains(commands, "systemd-run")
			if started != test.wantStarted {
				t.Fatalf("systemd-run called = %t, want %t; commands = %v", started, test.wantStarted, commands)
			}
		})
	}
}

func TestForgejoSweepDeliversEligibleGroupCandidatesToOneLead(t *testing.T) {
	for _, test := range []struct {
		name               string
		continuing         bool
		establishedMembers bool
	}{
		{name: "started primary"},
		{name: "continued singleton with fixed membership", continuing: true},
		{name: "continued group preserves claimed member and admits later sibling", continuing: true, establishedMembers: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			children := []map[string]any{stackedFixturePull(2, "main", "claimed-member")}
			if test.establishedMembers {
				children = append(children, stackedFixturePull(3, "main", "later-sibling"))
			}
			state.setStackedChildren(children)
			cfg := writeSweepFixtureConfig(t, state)
			setSweepFixtureMaxConcurrent(t, cfg, 3)
			if test.continuing {
				_, _, facts := state.service(t)
				facts.HeadSHA, facts.BaseSHA = state.headSHA(), state.targetSHA()
				current := handoffProgress{Stage: "fix", Round: 2, Head: facts.HeadSHA, LatestReview: 7}
				predecessor := handoffProgress{Stage: "review", Round: 2, Head: facts.HeadSHA, LatestReview: 7}
				runDir, handoffFile := writeProgressHandoff(t, cfg, facts, current, &predecessor)
				if test.establishedMembers {
					if err := os.WriteFile(filepath.Join(runDir, "members.json"), []byte(`{"members":[{"number":"1"},{"number":"2"}]}`), 0o600); err != nil {
						t.Fatal(err)
					}
					member := Facts{Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo, PR: "2"}
					guard := groupMemberGuardPath(cfg.Runs.Dir, UnitName(member))
					if err := os.MkdirAll(filepath.Dir(guard), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(guard, []byte(UnitName(facts)+"\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					data, err := os.ReadFile(handoffFile)
					if err != nil {
						t.Fatal(err)
					}
					var handoff runHandoff
					if err := json.Unmarshal(data, &handoff); err != nil {
						t.Fatal(err)
					}
					handoff.MemberHeads = []handoffMember{{
						Owner: facts.Owner, Repo: facts.Repo, Number: "2",
						Head: children[0]["head"].(map[string]any)["sha"].(string),
					}}
					data, err = json.Marshal(handoff)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(handoffFile, data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}

			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			var firstStart []string
			var activeUnits []string
			starts := 0
			commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "systemctl" {
					var listing strings.Builder
					for _, unit := range activeUnits {
						fmt.Fprintf(&listing, "%s.service loaded active running Minos lead\n", unit)
					}
					return []byte(listing.String()), nil
				}
				if name == "systemd-run" {
					starts++
					unitFlag := slices.Index(args, "--unit")
					if unitFlag < 0 || unitFlag+1 >= len(args) {
						t.Fatalf("systemd-run arguments omit unit: %v", args)
					}
					activeUnits = append(activeUnits, args[unitFlag+1])
					if firstStart == nil {
						firstStart = append([]string(nil), args...)
					}
				}
				return nil, nil
			}
			if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
				t.Fatal(err)
			}
			if firstStart == nil {
				t.Fatal("sweep did not start the primary lead")
			}
			wantStarts := 1
			if test.continuing {
				wantStarts = 2
			}
			if starts != wantStarts {
				t.Fatalf("sweep started %d runs, want %d", starts, wantStarts)
			}
			if test.establishedMembers {
				claimedMember := UnitName(Facts{Owner: "minos-e2e-owner", Repo: "subject", PR: "2"})
				laterSibling := UnitName(Facts{Owner: "minos-e2e-owner", Repo: "subject", PR: "3"})
				if slices.Contains(activeUnits, claimedMember) {
					t.Fatalf("continued group's claimed member started its own run: %v", activeUnits)
				}
				if !slices.Contains(activeUnits, laterSibling) {
					t.Fatalf("later sibling did not start with spare capacity: %v", activeUnits)
				}
			}
			environment := systemdEnvironment(t, firstStart)
			if got := environment["MINOS_RESUME"] == "true"; got != test.continuing {
				t.Fatalf("MINOS_RESUME set = %t, want %t", got, test.continuing)
			}
			candidatePath := environment["MINOS_GROUP_CANDIDATES"]
			if test.continuing {
				if candidatePath != "" {
					t.Fatalf("continuation received fresh candidate path %q", candidatePath)
				}
				return
			}
			data, err := os.ReadFile(candidatePath)
			if err != nil {
				t.Fatal(err)
			}
			var candidates []Facts
			if err := json.Unmarshal(data, &candidates); err != nil {
				t.Fatalf("decode candidates %q: %v", data, err)
			}
			if len(candidates) != 1 || candidates[0].PR != "2" {
				t.Fatalf("lead candidates = %#v, want eligible sibling #2", candidates)
			}
		})
	}

	t.Run("primary live from previous pass does not own unclaimed sibling", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.setStackedChildren([]map[string]any{stackedFixturePull(2, "main", "sibling")})
		cfg := writeSweepFixtureConfig(t, state)
		setSweepFixtureMaxConcurrent(t, cfg, 3)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		var activeUnits []string
		starts := 0
		commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "systemctl":
				var listing strings.Builder
				for _, unit := range activeUnits {
					fmt.Fprintf(&listing, "%s.service loaded active running Minos lead\n", unit)
				}
				return []byte(listing.String()), nil
			case "systemd-run":
				unitFlag := slices.Index(args, "--unit")
				if unitFlag < 0 || unitFlag+1 >= len(args) {
					t.Fatalf("systemd-run arguments omit unit: %v", args)
				}
				starts++
				activeUnits = append(activeUnits, args[unitFlag+1])
				return nil, nil
			default:
				return nil, nil
			}
		}

		if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
			t.Fatal(err)
		}
		if starts != 1 {
			t.Fatalf("first sweep started %d runs, want one primary", starts)
		}

		if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
			t.Fatal(err)
		}
		if starts != 2 {
			t.Fatalf("two passes started %d runs, want the live primary and newly eligible sibling", starts)
		}
	})
}

func TestForgejoSweepRemovesDeadGroupMemberGuard(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg := writeSweepFixtureConfig(t, state)
	member := UnitName(Facts{Forge: "forgejo", Owner: "minos-e2e-owner", Repo: "subject", PR: "2"})
	guard := groupMemberGuardPath(cfg.Runs.Dir, member)
	if err := os.MkdirAll(filepath.Dir(guard), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guard, []byte("minos-run-minos-e2e-owner-subject-pr1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(guard); !os.IsNotExist(err) {
		t.Fatalf("dead group guard remains: %v", err)
	}
}

func TestForgejoSweepPreservesLiveGroupMemberGuardAndSuppressesMember(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.setStackedChildren([]map[string]any{stackedFixturePull(2, "main", "sibling")})
	cfg := writeSweepFixtureConfig(t, state)
	primary := "minos-run-minos-e2e-owner-subject-pr1"
	member := UnitName(Facts{Forge: "forgejo", Owner: "minos-e2e-owner", Repo: "subject", PR: "2"})
	guard := groupMemberGuardPath(cfg.Runs.Dir, member)
	if err := os.MkdirAll(filepath.Dir(guard), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guard, []byte(primary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	starts := 0
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemctl" {
			return []byte(primary + ".service loaded active running Minos lead\n"), nil
		}
		if name == "systemd-run" {
			starts++
		}
		return nil, nil
	}
	if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(guard); err != nil || string(data) != primary+"\n" {
		t.Fatalf("live group guard after sweep = %q, %v", data, err)
	}
	result, err := SpawnRun(t.Context(), cfg, RepoConfig{}, Facts{Forge: "forgejo", Owner: "minos-e2e-owner", Repo: "subject", PR: "2"}, AdmissionContext{}, RunClassReview)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != SpawnSuppressed || result.BlockingUnit != primary+".service" || starts != 0 {
		t.Fatalf("member result after sweep = %+v, starts = %d", result, starts)
	}
}

func TestForgejoSweepReconciliationKeepsDecisionSnapshotFresh(t *testing.T) {
	t.Run("moved head and branch coordinates", func(t *testing.T) {
		state, environment := sweepAfterPriorityMutation(t, func(state *forgejoFixtureState) {
			state.changePullRequest(func(pullRequest map[string]any) {
				pullRequest["head"].(map[string]any)["sha"] = "fresh-head"
				pullRequest["head"].(map[string]any)["ref"] = "fresh-branch"
				pullRequest["base"].(map[string]any)["sha"] = "fresh-base"
			})
		})
		if environment["MINOS_HEAD_SHA"] != "fresh-head" || environment["MINOS_HEAD_BRANCH"] != "fresh-branch" ||
			environment["MINOS_TARGET_SHA"] != "fresh-base" || environment["MINOS_BASE_REF"] != "main" {
			t.Fatalf("systemd environment = %#v", environment)
		}
		if got := state.pullRequestSnapshotReads("1"); got != 1 {
			t.Fatalf("pull request 1 snapshot reads this pass = %d, want one", got)
		}
	})

	for _, eligibility := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "closed", mutate: func(pullRequest map[string]any) { pullRequest["state"] = "closed" }},
		{name: "merged", mutate: func(pullRequest map[string]any) { pullRequest["merged"] = true }},
		{name: "draft", mutate: func(pullRequest map[string]any) { pullRequest["draft"] = true }},
	} {
		t.Run(eligibility.name+" eligibility", func(t *testing.T) {
			_, environment := sweepAfterPriorityMutation(t, func(state *forgejoFixtureState) {
				state.changePullRequest(eligibility.mutate)
			})
			if environment != nil {
				t.Fatalf("systemd environment = %#v, want no run", environment)
			}
		})
	}

	t.Run("terminal review", func(t *testing.T) {
		state, environment := sweepAfterPriorityMutation(t, func(state *forgejoFixtureState) {
			state.setReviews([]map[string]any{{
				"id": 41, "state": "APPROVED", "commit_id": state.headSHA(),
				"body": "fresh terminal review", "user": map[string]any{"login": "Minos"},
			}})
		})
		if environment != nil {
			t.Fatalf("systemd environment = %#v, want no run", environment)
		}
		if writes, _ := state.statusWriteFacts(); writes != 1 {
			t.Fatalf("status writes = %d, want one recovered terminal status", writes)
		}
	})

	t.Run("terminal status", func(t *testing.T) {
		state, environment := sweepAfterPriorityMutation(t, func(state *forgejoFixtureState) {
			state.setStatuses([]map[string]any{{
				"id": 7, "context": "Minos", "status": "success", "description": product.Clean().Description(),
				"target_url": fmt.Sprintf("%s/minos-e2e-owner/subject/pulls/1#minos-target-%s", state.server.URL, state.targetSHA()),
				"creator":    map[string]any{"login": "Minos"},
			}})
		})
		if environment != nil {
			t.Fatalf("systemd environment = %#v, want no run", environment)
		}
		if writes, _ := state.statusWriteFacts(); writes != 0 {
			t.Fatalf("status writes = %d, want existing terminal status preserved", writes)
		}
	})
}

func TestForgejoContinuationPriorityReadsListedHeadStatuses(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.setStatuses([]map[string]any{{
		"id": 9, "context": "Minos", "status": "pending", "description": product.Continuation().Description(),
		"target_url": state.server.URL + "/continuation", "creator": map[string]any{"login": "Minos"},
	}})
	cfg, _, facts := state.service(t)
	facts.HeadSHA = state.headSHA()
	priority, err := currentContinuationPriority(t.Context(), cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	if priority != 0 {
		t.Fatalf("continuation priority = %d, want zero", priority)
	}
	for _, head := range state.statusReadFacts() {
		if head != facts.HeadSHA {
			t.Fatalf("status read head = %q, want listed head %q", head, facts.HeadSHA)
		}
	}
}

func sweepAfterPriorityMutation(t *testing.T, mutate func(*forgejoFixtureState)) (*forgejoFixtureState, map[string]string) {
	t.Helper()
	state := newForgejoFixtureState(t)
	cfg := writeSweepFixtureConfig(t, state)
	state.setPriorityBoundaryMutation(func() { mutate(state) })

	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	var systemdArgs []string
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemd-run" {
			systemdArgs = append([]string(nil), args...)
		}
		return nil, nil
	}
	if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
		t.Fatal(err)
	}
	if len(systemdArgs) == 0 {
		return state, nil
	}
	return state, systemdEnvironment(t, systemdArgs)
}

func writeSweepFixtureConfig(t *testing.T, state *forgejoFixtureState) ServiceConfig {
	t.Helper()
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	if err := os.MkdirAll(filepath.Join(cfg.Root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := `forge = "forgejo"
owner = "minos-e2e-owner"
repo = "subject"
[adaptation]
run-body = "/opt/minos/run-body/run-body"
`
	if err := os.WriteFile(filepath.Join(cfg.Root, "repos", "subject.toml"), []byte(repoConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// setSweepFixtureMaxConcurrent keeps admission capacity above the grouped
// journey's two pull requests. The one-run assertions must therefore prove
// grouped suppression, rather than merely observe the global cap.
func setSweepFixtureMaxConcurrent(t *testing.T, cfg ServiceConfig, maximum int) {
	t.Helper()
	servicePath := filepath.Join(cfg.Root, "service.toml")
	serviceConfig, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	serviceConfig = append(serviceConfig, []byte(fmt.Sprintf("max-concurrent = %d\n", maximum))...)
	if err := os.WriteFile(servicePath, serviceConfig, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeOperatorFixtureConfig(t *testing.T, cfg ServiceConfig) {
	t.Helper()
	writeServiceConfig(t, cfg)
	if err := os.MkdirAll(filepath.Join(cfg.Root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := `forge = "forgejo"
owner = "minos-e2e-owner"
repo = "subject"
[adaptation]
run-body = "/opt/minos/run-body/run-body"
`
	if err := os.WriteFile(filepath.Join(cfg.Root, "repos", "subject.toml"), []byte(repoConfig), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestForgeClaimAssignsAndReactsIdempotently(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")

	for attempt := 1; attempt <= 2; attempt++ {
		var stdout bytes.Buffer
		if err := ForgeCommand(t.Context(), []string{"claim"}, &stdout); err != nil {
			t.Fatalf("claim attempt %d: %v", attempt, err)
		}
		if !strings.Contains(stdout.String(), `"outcome":"applied"`) {
			t.Fatalf("claim attempt %d output = %q", attempt, stdout.String())
		}
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if !slices.Contains(state.assignees, "Minos") {
		t.Fatalf("assignees = %v, want Minos", state.assignees)
	}
	if !slices.Contains(state.reactions, "eyes") {
		t.Fatalf("reactions = %v, want eyes", state.reactions)
	}
	if state.assignmentWrites != 1 || state.reactionWrites != 1 {
		t.Fatalf("writes = assignment:%d reaction:%d, want one each", state.assignmentWrites, state.reactionWrites)
	}
	if state.obsoleteAssignmentWrites != 0 {
		t.Fatalf("obsolete assignment route received %d writes, want none", state.obsoleteAssignmentWrites)
	}
}

func TestForgeReactionUsesForgejo14ShapeAndReadBackIdempotency(t *testing.T) {
	var apiShape struct {
		Request  map[string]any `json:"request"`
		Response map[string]any `json:"response"`
	}
	data, err := os.ReadFile(filepath.Join("testdata", "forgejo14", "reaction.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &apiShape); err != nil {
		t.Fatal(err)
	}
	state := newForgejoFixtureState(t)
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")

	content := apiShape.Request["content"].(string)
	if apiShape.Response["content"] != content || apiShape.Response["user"].(map[string]any)["login"] != "Minos" {
		t.Fatalf("reaction fixture does not preserve the Forgejo response shape: %#v", apiShape)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := ForgeCommand(t.Context(), []string{"reaction", state.headSHA(), state.targetSHA(), content}, &bytes.Buffer{}); err != nil {
			t.Fatalf("reaction attempt %d: %v", attempt, err)
		}
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if !slices.Contains(state.reactions, "+1") || state.reactionWrites != 1 {
		t.Fatalf("reactions = %v, writes = %d, want one +1 write", state.reactions, state.reactionWrites)
	}
	if state.reviewWrites != 0 {
		t.Fatalf("reaction journey posted %d reviews, want none", state.reviewWrites)
	}
}

func TestForgeStatusReadBackIsScopedToPullRequest(t *testing.T) {
	for _, test := range []struct {
		name         string
		includeOwned bool
		wantPosts    int
	}{
		{name: "newer foreign status does not duplicate an owned status", includeOwned: true, wantPosts: 0},
		{name: "foreign status does not hide genuine absence", wantPosts: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			cfg, _, facts := state.service(t)
			writeServiceConfig(t, cfg)
			t.Setenv("MINOS_CONFIG", cfg.Root)
			t.Setenv("MINOS_FORGE", facts.Forge)
			t.Setenv("MINOS_OWNER", facts.Owner)
			t.Setenv("MINOS_REPO_NAME", facts.Repo)
			t.Setenv("MINOS_PR", facts.PR)

			head, target := state.headSHA(), state.targetSHA()
			statuses := []map[string]any{{
				"id": 8, "context": "Minos", "status": "pending", "description": product.Working().Description(),
				"target_url": state.server.URL + "/minos-e2e-owner/subject/pulls/2#minos-target-" + target,
				"creator":    map[string]any{"login": "Minos"},
			}}
			if test.includeOwned {
				statuses = append(statuses, map[string]any{
					"id": 7, "context": "Minos", "status": "pending", "description": product.Working().Description(),
					"target_url": statusTargetURL(cfg.Forges[facts.Forge].APIBase, Facts{
						Owner: facts.Owner, Repo: facts.Repo, PR: facts.PR, BaseSHA: target,
					}),
					"creator": map[string]any{"login": "Minos"},
				})
			}
			state.setStatuses(statuses)

			var stdout strings.Builder
			if err := ForgeCommand(t.Context(), []string{"status", head, target, "working"}, &stdout); err != nil {
				t.Fatalf("status write: %v\n%s", err, stdout.String())
			}
			if posts := state.statusPostFacts(); len(posts) != test.wantPosts {
				t.Fatalf("forge received %d status posts, want %d: %#v", len(posts), test.wantPosts, posts)
			}
		})
	}
}

func TestSnapshotCarriesSortedLabelsAndForgeTargetSyncMethod(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["labels"] = []any{
			map[string]any{"id": float64(2), "name": "Flaky Test"},
			map[string]any{"id": float64(1), "name": "Needs Work"},
		}
	})
	cfg, _, facts := state.service(t)
	snapshot, err := currentSnapshot(t.Context(), cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(snapshot.Labels, []string{"Flaky Test", "Needs Work"}) {
		t.Fatalf("labels = %v", snapshot.Labels)
	}
	if snapshot.TargetSyncMethod != "merge" {
		t.Fatalf("target sync method = %q, want forge-configured merge", snapshot.TargetSyncMethod)
	}
	state.mu.Lock()
	state.repository["default_update_style"] = "rebase"
	state.mu.Unlock()
	snapshot, err = currentSnapshot(t.Context(), cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TargetSyncMethod != "rebase" {
		t.Fatalf("target sync method = %q, want forge-configured rebase", snapshot.TargetSyncMethod)
	}
}

func TestForgeCheckLogsReturnsOnlyLatestStatusRunEvidence(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.statuses = []map[string]any{
		{
			"id": float64(1), "context": "CI / test", "status": "success",
			"target_url": "/minos-e2e-owner/subject/actions/runs/41/jobs/0",
		},
		{
			"id": float64(2), "context": "CI / test", "status": "success",
			"target_url": "/minos-e2e-owner/subject/actions/runs/42/jobs/0",
		},
	}
	state.actionRuns = []map[string]any{
		{"id": float64(141), "index_in_repo": float64(41), "status": "success", "title": "old"},
		{"id": float64(142), "index_in_repo": float64(42), "status": "success", "title": "current"},
	}
	state.actionJobs[141] = []map[string]any{{"id": float64(241), "name": "test", "status": "success"}}
	state.actionJobs[142] = []map[string]any{{"id": float64(242), "name": "test", "status": "success"}}
	state.actionLogs[241] = "FLAKY old_test\n"
	state.actionLogs[242] = "Summary 2 passed (1 flaky)\nFLAKY current_test\n"

	cfg, _, facts := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)
	t.Setenv("MINOS_PR", facts.PR)

	var stdout bytes.Buffer
	if err := ForgeCommand(
		t.Context(),
		[]string{"check-logs", state.headSHA(), state.targetSHA()},
		&stdout,
	); err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		HeadSHA string `json:"head_sha"`
		Runs    []struct {
			Index int64 `json:"index"`
			Jobs  []struct {
				Log string `json:"log"`
			} `json:"jobs"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.HeadSHA != state.headSHA() || len(evidence.Runs) != 1 || evidence.Runs[0].Index != 42 {
		t.Fatalf("evidence identity = %#v", evidence)
	}
	if len(evidence.Runs[0].Jobs) != 1 ||
		!strings.Contains(evidence.Runs[0].Jobs[0].Log, "FLAKY current_test") ||
		strings.Contains(evidence.Runs[0].Jobs[0].Log, "old_test") {
		t.Fatalf("evidence jobs = %#v", evidence.Runs[0].Jobs)
	}
}

func TestForgeTerminalCleanupWritesAreGuardedIdempotentAndReadBack(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["labels"] = []any{map[string]any{"id": float64(7), "name": "Flaky Test"}}
	})
	state.reactions = []string{"eyes"}
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")

	head, target := state.headSHA(), state.targetSHA()
	for attempt := 0; attempt < 2; attempt++ {
		if err := ForgeCommand(t.Context(), []string{"label-remove", head, target, "Flaky Test"}, &bytes.Buffer{}); err != nil {
			t.Fatalf("label removal attempt %d: %v", attempt+1, err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := ForgeCommand(t.Context(), []string{"merge", head, target, "merge"}, &bytes.Buffer{}); err != nil {
			t.Fatalf("merge attempt %d: %v", attempt+1, err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := ForgeCommand(t.Context(), []string{"reaction-remove", head, target, "eyes"}, &bytes.Buffer{}); err != nil {
			t.Fatalf("reaction removal attempt %d: %v", attempt+1, err)
		}
		if err := ForgeCommand(t.Context(), []string{"delete-source-branch", head, target, "journey-one"}, &bytes.Buffer{}); err != nil {
			t.Fatalf("branch deletion attempt %d: %v", attempt+1, err)
		}
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	labels, _ := state.pullRequest["labels"].([]any)
	if len(labels) != 0 || state.labelDeleteWrites != 1 {
		t.Fatalf("labels = %#v, delete writes = %d", labels, state.labelDeleteWrites)
	}
	if slices.Contains(state.reactions, "eyes") || state.reactionDeleteWrites != 1 {
		t.Fatalf("reactions = %v, delete writes = %d", state.reactions, state.reactionDeleteWrites)
	}
	if state.mergeWrites != 1 || state.sourceBranchExists || !state.branchDeletedByMerge || state.branchDeleteWrites != 0 {
		t.Fatalf("merge writes = %d, source exists = %t, merge deleted = %t, raw branch deletes = %d",
			state.mergeWrites, state.sourceBranchExists, state.branchDeletedByMerge, state.branchDeleteWrites)
	}
}

func TestForgeRemovesEyesIdempotentlyFromAnOpenTerminalPullRequest(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.reactions = []string{"eyes"}
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")

	for attempt := 0; attempt < 2; attempt++ {
		if err := ForgeCommand(t.Context(), []string{"reaction-remove", state.headSHA(), state.targetSHA(), "eyes"}, &bytes.Buffer{}); err != nil {
			t.Fatalf("reaction removal attempt %d: %v", attempt+1, err)
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if slices.Contains(state.reactions, "eyes") || state.reactionDeleteWrites != 1 {
		t.Fatalf("reactions = %v, delete writes = %d", state.reactions, state.reactionDeleteWrites)
	}
}

func TestForgePreservesForkSourceBranch(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["head"].(map[string]any)["repo"].(map[string]any)["full_name"] = "contributor/subject"
		pullRequest["merged"] = true
		pullRequest["state"] = "closed"
	})
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")

	err := ForgeCommand(t.Context(), []string{"delete-source-branch", state.headSHA(), state.targetSHA(), "journey-one"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("fork deletion error = %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.sourceBranchExists || state.branchDeleteWrites != 0 {
		t.Fatalf("fork source exists = %t, delete writes = %d", state.sourceBranchExists, state.branchDeleteWrites)
	}
}

func TestForgeMergeDeletesSameRepoBranchAndForgeRetargetsStackedChildren(t *testing.T) {
	state := newForgejoFixtureState(t)
	stacked := stackedFixturePull(2, "journey-one", "journey-two")
	state.setStackedChildren([]map[string]any{stacked})
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")

	if err := ForgeCommand(t.Context(), []string{"merge", state.headSHA(), state.targetSHA(), "merge"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.mergeWrites != 1 || state.sourceBranchExists || !state.branchDeletedByMerge {
		t.Fatalf("merge writes = %d, source exists = %t, merge deleted = %t", state.mergeWrites, state.sourceBranchExists, state.branchDeletedByMerge)
	}
	if base := stacked["base"].(map[string]any)["ref"]; base != "main" || stacked["state"] != "open" {
		t.Fatalf("stacked child base = %v, state = %v", base, stacked["state"])
	}
}

func TestForgeMergePreservesForkSourceBranch(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["head"].(map[string]any)["repo"].(map[string]any)["full_name"] = "contributor/subject"
	})
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")

	if err := ForgeCommand(t.Context(), []string{"merge", state.headSHA(), state.targetSHA(), "merge"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.mergeWrites != 1 || !state.sourceBranchExists || state.branchDeletedByMerge {
		t.Fatalf("merge writes = %d, source exists = %t, merge deleted = %t", state.mergeWrites, state.sourceBranchExists, state.branchDeletedByMerge)
	}
}

func stackedFixturePull(number float64, baseRef, headRef string) map[string]any {
	return map[string]any{
		"number": number, "state": "open", "merged": false, "draft": false,
		"user": map[string]any{"login": "fixture-author"},
		"base": map[string]any{"ref": baseRef, "sha": "target-" + baseRef, "repo": map[string]any{"full_name": "minos-e2e-owner/subject"}},
		"head": map[string]any{"ref": headRef, "sha": "head-" + headRef, "repo": map[string]any{"full_name": "minos-e2e-owner/subject"}},
	}
}

func TestForgeDeleteSourceBranchRetargetsStackedChildrenBeforeDeletion(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["merged"] = true
		pullRequest["state"] = "closed"
	})
	stacked := stackedFixturePull(2, "journey-one", "journey-two")
	unrelated := stackedFixturePull(3, "main", "journey-three")
	state.setStackedChildren([]map[string]any{stacked, unrelated})
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")

	for attempt := 0; attempt < 2; attempt++ {
		if err := ForgeCommand(t.Context(), []string{"delete-source-branch", state.headSHA(), state.targetSHA(), "journey-one"}, &bytes.Buffer{}); err != nil {
			t.Fatalf("branch deletion attempt %d: %v", attempt+1, err)
		}
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.sourceBranchExists || state.branchDeleteWrites != 1 {
		t.Fatalf("source exists = %t, branch deletes = %d", state.sourceBranchExists, state.branchDeleteWrites)
	}
	if base := stacked["base"].(map[string]any)["ref"]; base != "main" || stacked["state"] != "open" {
		t.Fatalf("stacked child base = %v, state = %v", base, stacked["state"])
	}
	if state.childRetargetWrites != 1 || state.childRetargetAfterDelete {
		t.Fatalf("retarget writes = %d, after delete = %t", state.childRetargetWrites, state.childRetargetAfterDelete)
	}
	if base := unrelated["base"].(map[string]any)["ref"]; base != "main" {
		t.Fatalf("unrelated pull base = %v", base)
	}
}

func TestForgeDeleteSourceBranchRefusesWhenStackedChildRetargetFails(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["merged"] = true
		pullRequest["state"] = "closed"
	})
	state.setStackedChildren([]map[string]any{stackedFixturePull(2, "journey-one", "journey-two")})
	state.mu.Lock()
	state.childRetargetCode = http.StatusInternalServerError
	state.mu.Unlock()
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")

	err := ForgeCommand(t.Context(), []string{"delete-source-branch", state.headSHA(), state.targetSHA(), "journey-one"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("retarget failure error = %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.sourceBranchExists || state.branchDeleteWrites != 0 {
		t.Fatalf("source exists = %t, branch deletes = %d", state.sourceBranchExists, state.branchDeleteWrites)
	}
}

func TestForgeReviewCommentsUseForgejo14ShapeAndForgeReadBackIdempotency(t *testing.T) {
	var apiShape struct {
		Request  map[string]any   `json:"request"`
		Review   map[string]any   `json:"review"`
		Comments []map[string]any `json:"comments"`
	}
	data, err := os.ReadFile(filepath.Join("testdata", "forgejo14", "review-with-comments.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &apiShape); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		preseed bool
	}{
		{name: "post then rerun"},
		{name: "crashed post already visible", preseed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			head, target := installAnchoredWorkspace(t, state, "internal/state.go", 41)
			cfg, _, _ := state.service(t)
			writeServiceConfig(t, cfg)
			t.Setenv("MINOS_CONFIG", cfg.Root)
			t.Setenv("MINOS_FORGE", "forgejo")
			t.Setenv("MINOS_OWNER", "minos-e2e-owner")
			t.Setenv("MINOS_REPO_NAME", "subject")
			t.Setenv("MINOS_PR", "1")

			body := apiShape.Request["body"].(string)
			record, err := product.FormatRecord(map[string]string{"head": head, "target": target})
			if err != nil {
				t.Fatal(err)
			}
			expectedBody := body + "\n\n" + record
			if test.preseed {
				review := mapsClone(apiShape.Review)
				review["commit_id"] = head
				review["body"] = expectedBody
				state.setReviewWithComments(review, apiShape.Comments)
			}

			bodyPath := filepath.Join(t.TempDir(), "body.md")
			commentsPath := filepath.Join(t.TempDir(), "comments.json")
			if err := os.WriteFile(bodyPath, []byte(body+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			comments, err := json.Marshal([]requestedReviewComment{{
				Path: "internal/state.go", Line: 41, Body: "The transition accepts an invalid state.",
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(commentsPath, comments, 0o600); err != nil {
				t.Fatal(err)
			}

			attempts := 2
			if test.preseed {
				attempts = 1
			}
			for attempt := 0; attempt < attempts; attempt++ {
				if err := ForgeCommand(t.Context(), []string{"review", head, target, "comment", bodyPath, commentsPath}, &bytes.Buffer{}); err != nil {
					t.Fatalf("review attempt %d: %v", attempt+1, err)
				}
			}

			writes, payload := state.reviewWriteFacts()
			wantWrites := 1
			if test.preseed {
				wantWrites = 0
			}
			if writes != wantWrites {
				t.Fatalf("review writes = %d, want %d", writes, wantWrites)
			}
			if !test.preseed {
				if payload["commit_id"] != head || payload["event"] != "COMMENT" || payload["body"] != expectedBody {
					t.Fatalf("review payload = %#v", payload)
				}
				postedComments := payload["comments"].([]any)
				comment := postedComments[0].(map[string]any)
				if comment["path"] != "internal/state.go" || comment["new_position"] != float64(41) {
					t.Fatalf("review comment = %#v", comment)
				}
			}
		})
	}

	t.Run("review without inline comments is also idempotent", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, _, _ := state.service(t)
		writeServiceConfig(t, cfg)
		t.Setenv("MINOS_CONFIG", cfg.Root)
		t.Setenv("MINOS_FORGE", "forgejo")
		t.Setenv("MINOS_OWNER", "minos-e2e-owner")
		t.Setenv("MINOS_REPO_NAME", "subject")
		t.Setenv("MINOS_PR", "1")
		bodyPath := filepath.Join(t.TempDir(), "body.md")
		if err := os.WriteFile(bodyPath, []byte("The reviewed code is clean.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			if err := ForgeCommand(t.Context(), []string{"review", state.headSHA(), state.targetSHA(), "approve", bodyPath}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
		}
		if writes, _ := state.reviewWriteFacts(); writes != 1 {
			t.Fatalf("review writes = %d, want one", writes)
		}
	})

	t.Run("confirmed-unfixed request changes reach the guarded review consumer once", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		head, target := installAnchoredWorkspace(t, state, "internal/state.go", 41)
		cfg, _, _ := state.service(t)
		writeServiceConfig(t, cfg)
		t.Setenv("MINOS_CONFIG", cfg.Root)
		t.Setenv("MINOS_FORGE", "forgejo")
		t.Setenv("MINOS_OWNER", "minos-e2e-owner")
		t.Setenv("MINOS_REPO_NAME", "subject")
		t.Setenv("MINOS_PR", "1")

		bodyPath := filepath.Join(t.TempDir(), "body.md")
		commentsPath := filepath.Join(t.TempDir(), "comments.json")
		if err := os.WriteFile(bodyPath, []byte("Confirmed code findings remain unresolved.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		comments, err := json.Marshal([]requestedReviewComment{{
			Path: "internal/state.go", Line: 41, Body: "The transition accepts an invalid state.",
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(commentsPath, comments, 0o600); err != nil {
			t.Fatal(err)
		}

		for attempt := 0; attempt < 2; attempt++ {
			if err := ForgeCommand(t.Context(), []string{"review", head, target, "request-changes", bodyPath, commentsPath}, &bytes.Buffer{}); err != nil {
				t.Fatalf("request-changes attempt %d: %v", attempt+1, err)
			}
		}

		writes, payload := state.reviewWriteFacts()
		if writes != 1 || payload["event"] != "REQUEST_CHANGES" || payload["commit_id"] != head {
			t.Fatalf("request-changes writes = %d, payload = %#v", writes, payload)
		}
		postedComments := payload["comments"].([]any)
		comment := postedComments[0].(map[string]any)
		if comment["path"] != "internal/state.go" || comment["new_position"] != float64(41) {
			t.Fatalf("request-changes comment = %#v", comment)
		}
	})

	t.Run("check-caused request changes carry their durable witness", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		configureForgeCommandFixture(t, state)
		bodyPath := filepath.Join(t.TempDir(), "body.md")
		if err := os.WriteFile(bodyPath, []byte("Required checks remain red.\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := ForgeCommand(t.Context(), []string{
			"review", state.headSHA(), state.targetSHA(), "request-changes-checks", bodyPath,
		}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}

		writes, payload := state.reviewWriteFacts()
		if writes != 1 || payload["event"] != "REQUEST_CHANGES" {
			t.Fatalf("review writes = %d, payload = %#v", writes, payload)
		}
		record, ok := product.TrailingRecord(payload["body"].(string))
		if !ok || record[product.RecordCauseKey] != product.RecordCauseRequiredChecks || record[product.RecordTargetKey] != state.targetSHA() {
			t.Fatalf("trailing record = %#v, valid = %t", record, ok)
		}
	})
}

func TestForgeMemberWritesStayOnTheirOwnPullRequests(t *testing.T) {
	state := newForgejoFixtureState(t)
	head, target := installAnchoredWorkspace(t, state, "internal/state.go", 41)

	var writes []string
	var requests []string
	reviews := map[string][]map[string]any{}
	comments := map[string]map[string][]map[string]any{}
	statuses := map[string][]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		requests = append(requests, r.Method+" "+path)
		if r.Method == http.MethodGet && path == "/api/v1/user" {
			writeFixtureJSON(t, w, map[string]any{"login": "Minos"})
			return
		}
		if strings.HasSuffix(path, "/statuses") && r.Method == http.MethodGet {
			headID := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/repos/minos-e2e-owner/subject/commits/"), "/statuses")
			writeFixtureJSON(t, w, statuses[headID])
			return
		}
		if strings.Contains(path, "/statuses/") {
			if r.Method == http.MethodPost {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				member := "unknown"
				if targetURL, ok := payload["target_url"].(string); ok {
					for _, candidate := range []string{"1", "2"} {
						if strings.Contains(targetURL, "/pulls/"+candidate+"#") {
							member = candidate
						}
					}
				}
				payload["id"] = len(statuses[head]) + 1
				payload["creator"] = map[string]any{"login": "Minos"}
				statuses[head] = append([]map[string]any{payload}, statuses[head]...)
				writes = append(writes, "status:"+member)
				writeFixtureJSON(t, w, payload)
				return
			}
		}
		if r.Method == http.MethodGet && path == "/api/v1/repos/minos-e2e-owner/subject/branches/main" {
			writeFixtureJSON(t, w, map[string]any{"commit": map[string]any{"id": target}})
			return
		}
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) < 7 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "repos" || parts[5] != "pulls" {
			http.Error(w, "unexpected fixture request", http.StatusNotFound)
			return
		}
		member := parts[6]
		switch {
		case r.Method == http.MethodGet && len(parts) == 7:
			memberNumber, err := strconv.Atoi(member)
			if err != nil {
				http.Error(w, "invalid pull request", http.StatusBadRequest)
				return
			}
			writeFixtureJSON(t, w, map[string]any{
				"number": memberNumber, "state": "open", "merged": false,
				"head": map[string]any{"sha": head},
				"base": map[string]any{"ref": "main", "sha": target, "repo": map[string]any{"full_name": "minos-e2e-owner/subject"}},
			})
		case r.Method == http.MethodGet && len(parts) == 8 && parts[7] == "reviews":
			writeFixtureJSON(t, w, reviews[member])
		case r.Method == http.MethodPost && len(parts) == 8 && parts[7] == "reviews":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			id := int64(len(reviews[member]) + 1)
			review := map[string]any{"id": id, "state": payload["event"], "commit_id": payload["commit_id"], "body": payload["body"], "user": map[string]any{"login": "Minos"}}
			reviews[member] = append(reviews[member], review)
			commentRows := []map[string]any{}
			for index, raw := range payload["comments"].([]any) {
				comment := mapsClone(raw.(map[string]any))
				comment["id"] = index + 1
				comment["pull_request_review_id"] = id
				comment["position"] = comment["new_position"]
				comment["original_position"] = float64(0)
				comment["diff_hunk"] = "@@ -41,3 +41,3 @@"
				delete(comment, "new_position")
				commentRows = append(commentRows, comment)
			}
			if comments[member] == nil {
				comments[member] = map[string][]map[string]any{}
			}
			comments[member][strconv.FormatInt(id, 10)] = commentRows
			writes = append(writes, "review:"+member+":"+fmt.Sprint(commentRows[0]["body"]))
			writeFixtureJSON(t, w, review)
		case r.Method == http.MethodGet && len(parts) == 10 && parts[7] == "reviews" && parts[9] == "comments":
			writeFixtureJSON(t, w, comments[member][parts[8]])
		default:
			http.Error(w, "unexpected fixture request", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	cfg, _, _ := state.service(t)
	cfg.Forges["forgejo"] = ForgeConfig{Adaptation: state.adaptationPath, APIBase: server.URL, WebhookSecretFile: state.tokenPath, CredentialFile: state.tokenPath}
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")
	primary := Facts{Owner: "minos-e2e-owner", Repo: "subject", PR: "1"}
	sibling := Facts{Owner: "minos-e2e-owner", Repo: "subject", PR: "2"}
	siblingGuard := groupMemberGuardPath(cfg.Runs.Dir, UnitName(sibling))
	if err := os.MkdirAll(filepath.Dir(siblingGuard), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(siblingGuard, []byte(UnitName(primary)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, member := range []struct{ number, finding string }{{"1", "primary-only finding"}, {"2", "sibling-only finding"}} {
		bodyPath := filepath.Join(t.TempDir(), member.number+".md")
		commentsPath := filepath.Join(t.TempDir(), member.number+".json")
		if err := os.WriteFile(bodyPath, []byte("Member review.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal([]requestedReviewComment{{Path: "internal/state.go", Line: 41, Body: member.finding}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(commentsPath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		prefix := []string{"--member", "minos-e2e-owner", "subject", member.number}
		if err := ForgeCommand(t.Context(), append(prefix, "review", head, target, "comment", bodyPath, commentsPath), &bytes.Buffer{}); err != nil {
			t.Fatalf("review: %v; requests: %v", err, requests)
		}
		if err := ForgeCommand(t.Context(), append(prefix, "status", head, target, "attention"), &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}

	if !slices.Equal(writes, []string{
		"review:1:primary-only finding", "status:1", "review:2:sibling-only finding", "status:2",
	}) {
		t.Fatalf("member write sequence = %v", writes)
	}
}

func TestForgeBriefReviewRemainsDistinctFromSweepReviewAndIdempotent(t *testing.T) {
	state := newForgejoFixtureState(t)
	head, target := installAnchoredWorkspace(t, state, "internal/state.go", 41)
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")

	directory := t.TempDir()
	sweepBody := filepath.Join(directory, "sweep.md")
	briefBody := filepath.Join(directory, "brief.md")
	commentsPath := filepath.Join(directory, "comments.json")
	if err := os.WriteFile(sweepBody, []byte("Main sweep findings.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(briefBody, []byte("Repository review brief findings.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	comments := []requestedReviewComment{{Path: "internal/state.go", Body: "Brief concern.", Line: 41}}
	encoded, err := json.Marshal(comments)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(commentsPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ForgeCommand(t.Context(), []string{"review", head, target, "comment", sweepBody}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := ForgeCommand(t.Context(), []string{"review", head, target, "comment", briefBody, commentsPath}, &bytes.Buffer{}); err != nil {
			t.Fatalf("brief review attempt %d: %v", attempt, err)
		}
	}
	writes, payload := state.reviewWriteFacts()
	if writes != 2 {
		t.Fatalf("review writes = %d, want one sweep group and one brief group", writes)
	}
	if !strings.HasPrefix(payload["body"].(string), "Repository review brief findings.") {
		t.Fatalf("last review body = %q, want distinct brief group", payload["body"])
	}
}

func TestForgeReviewFoldsOffDiffFindingsIntoTheBody(t *testing.T) {
	state := newForgejoFixtureState(t)
	head, target := installAnchoredWorkspace(t, state, "src/code.txt", 10)
	configureForgeCommandFixture(t, state)
	comments := []requestedReviewComment{
		{Path: "src/code.txt", Line: 10, Body: "Anchored concern."},
		{Path: "src/code.txt", Line: 1, Body: "Outside-hunk concern."},
	}

	runReviewAttempts(t, state, head, target, "Review findings.", comments, 2)
	writes, payload := state.reviewWriteFacts()
	if writes != 1 {
		t.Fatalf("review writes = %d, want one", writes)
	}
	posted := payload["comments"].([]any)
	if len(posted) != 1 || posted[0].(map[string]any)["new_position"] != float64(10) {
		t.Fatalf("inline comments = %#v", posted)
	}
	body := payload["body"].(string)
	for _, want := range []string{
		"Findings that could not be anchored inline:",
		"`src/code.txt` line 1",
		"Outside-hunk concern.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("review body omitted %q: %q", want, body)
		}
	}
}

func TestForgeReviewConvergesWhenTheForgeRewritesStoredPositions(t *testing.T) {
	state := newForgejoFixtureState(t)
	head, target := installAnchoredWorkspace(t, state, "src/code.txt", 10)
	state.setPositionRewrite("src/code.txt", 10, 3)
	configureForgeCommandFixture(t, state)

	runReviewAttempts(t, state, head, target, "Review findings.", []requestedReviewComment{{
		Path: "src/code.txt", Line: 10, Body: "Blame-rewritten concern.",
	}}, 2)
	if writes, _ := state.reviewWriteFacts(); writes != 1 {
		t.Fatalf("review writes = %d, want one", writes)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	stored := state.reviewComments[1][0]
	if stored["position"] != int64(3) || stored["diff_hunk"] == "" {
		t.Fatalf("stored anchored comment = %#v", stored)
	}
}

func TestForgeReviewConvergesWhenTheForgeStoresACommentUnanchored(t *testing.T) {
	state := newForgejoFixtureState(t)
	head, target := installAnchoredWorkspace(t, state, "src/code.txt", 10)
	state.setDiffNewSide(nil)
	configureForgeCommandFixture(t, state)

	runReviewAttempts(t, state, head, target, "Review findings.", []requestedReviewComment{{
		Path: "src/code.txt", Line: 10, Body: "Durable concern.",
	}}, 2)
	if writes, _ := state.reviewWriteFacts(); writes != 1 {
		t.Fatalf("review writes = %d, want one", writes)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	stored := state.reviewComments[1][0]
	if stored["position"] != widgetUnanchoredPosition || stored["diff_hunk"] != "" {
		t.Fatalf("stored unanchored comment = %#v", stored)
	}
}

func configureForgeCommandFixture(t *testing.T, state *forgejoFixtureState) {
	t.Helper()
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")
}

func runReviewAttempts(
	t *testing.T,
	state *forgejoFixtureState,
	head, target, body string,
	comments []requestedReviewComment,
	attempts int,
) {
	t.Helper()
	directory := t.TempDir()
	bodyPath := filepath.Join(directory, "body.md")
	commentsPath := filepath.Join(directory, "comments.json")
	if err := os.WriteFile(bodyPath, []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(comments)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(commentsPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ForgeCommand(t.Context(), []string{
			"review", head, target, "comment", bodyPath, commentsPath,
		}, &bytes.Buffer{}); err != nil {
			t.Fatalf("review attempt %d: %v", attempt, err)
		}
	}
}

func mapsClone(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func installAnchoredWorkspace(t *testing.T, state *forgejoFixtureState, path string, changedLines ...int) (head, target string) {
	t.Helper()
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-q")
	runGit(t, workspace, "config", "user.name", "Minos Test")
	runGit(t, workspace, "config", "user.email", "minos@example.invalid")

	lineCount := 8
	for _, line := range changedLines {
		if line+4 > lineCount {
			lineCount = line + 4
		}
	}
	lines := make([]string, lineCount)
	for index := range lines {
		lines[index] = fmt.Sprintf("line %d", index+1)
	}
	fullPath := filepath.Join(workspace, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-q", "-m", "target")
	target = strings.TrimSpace(gitOutput(t, workspace, "rev-parse", "HEAD"))

	for _, line := range changedLines {
		lines[line-1] = fmt.Sprintf("changed line %d", line)
	}
	if err := os.WriteFile(fullPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-q", "-m", "head")
	head = strings.TrimSpace(gitOutput(t, workspace, "rev-parse", "HEAD"))

	diff, err := mergeBaseDiff(t.Context(), workspace, target, head)
	if err != nil {
		t.Fatal(err)
	}
	state.setDiffNewSide(newSideIntervals(diff))
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["head"].(map[string]any)["sha"] = head
		pullRequest["base"].(map[string]any)["sha"] = target
	})
	t.Setenv("MINOS_WORKSPACE", workspace)
	return head, target
}

type forgejoFixtureState struct {
	t                *testing.T
	pullRequest      map[string]any
	repository       map[string]any
	reviews          []map[string]any
	statuses         []map[string]any
	statusesByCommit map[string][]map[string]any
	commits          []map[string]any
	issueComments    []map[string]any
	dependencies     []map[string]any
	dependencyPages  [][]map[string]any
	dependencyCode   int
	actionRuns       []map[string]any
	actionJobs       map[int64][]map[string]any
	actionLogs       map[int64]string
	server           *httptest.Server
	tokenPath        string
	adaptationPath   string

	mu                       sync.Mutex
	assignees                []string
	reactions                []string
	assignmentWritePaths     []string
	reactionWritePaths       []string
	assignmentWrites         int
	obsoleteAssignmentWrites int
	reactionWrites           int
	reactionDeleteWrites     int
	labelDeleteWrites        int
	mergeWrites              int
	branchDeleteWrites       int
	sourceBranchExists       bool
	stackedChildren          []map[string]any
	childRetargetWrites      int
	childRetargetAfterDelete bool
	childRetargetCode        int
	branchDeletedByMerge     bool
	statusWrites             int
	statusPostRequests       []statusPostRequest
	reviewWrites             int
	reviewPayloads           []map[string]any
	reviewComments           map[int64][]map[string]any
	diffNewSide              map[string][][2]int64
	positionRewrites         map[string]map[int64]int64
	statusReadCommits        []string
	pullRequestReads         map[string]int
	priorityBoundaryMutation func()
	writeSequence            []string
	virtualRefLookups        int
	annexeCloneURL           string
	operatorPullRequests     []operatorFixturePullRequest
}

type operatorFixturePullRequest struct {
	pullRequest map[string]any
	statuses    []map[string]any
	reviews     []map[string]any
	comments    []map[string]any
}

type statusPostRequest struct {
	Head    string
	Payload map[string]any
}

// Widget#28 exposed this blame-origin coordinate on an unanchored comment.
const widgetUnanchoredPosition int64 = 3691

func newForgejoFixtureState(t *testing.T) *forgejoFixtureState {
	t.Helper()
	fixture := readFixture(t, "001-pull_request-opened.json")
	var event struct {
		PullRequest map[string]any `json:"pull_request"`
		Repository  map[string]any `json:"repository"`
	}
	if err := json.Unmarshal([]byte(fixture.Body), &event); err != nil {
		t.Fatal(err)
	}
	adaptationPath, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	state := &forgejoFixtureState{
		t: t, pullRequest: event.PullRequest, repository: event.Repository,
		adaptationPath: adaptationPath, reviewComments: make(map[int64][]map[string]any),
		diffNewSide: make(map[string][][2]int64), positionRewrites: make(map[string]map[int64]int64),
		actionJobs: make(map[int64][]map[string]any), actionLogs: make(map[int64]string),
		pullRequestReads: make(map[string]int),
		dependencies:     []map[string]any{}, sourceBranchExists: true, dependencyCode: http.StatusOK,
		statusesByCommit: make(map[string][]map[string]any),
	}
	state.commits = []map[string]any{{"sha": state.pullRequest["head"].(map[string]any)["sha"], "author": map[string]any{"login": "fixture-author"}}}
	state.server = httptest.NewServer(http.HandlerFunc(state.handle))
	t.Cleanup(state.server.Close)
	state.tokenPath = filepath.Join(t.TempDir(), "forge.token")
	if err := os.WriteFile(state.tokenPath, []byte("fixture-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return state
}

func (s *forgejoFixtureState) service(t *testing.T) (ServiceConfig, RepoConfig, Facts) {
	t.Helper()
	s.mu.Lock()
	pullRequestNumber := fmt.Sprint(s.pullRequest["number"])
	s.mu.Unlock()
	cfg := ServiceConfig{Root: t.TempDir()}
	cfg.Service.BotLogin = "Minos"
	cfg.Listener.Bind = ":0"
	cfg.Runs.Dir = t.TempDir()
	cfg.Forges = map[string]ForgeConfig{
		"forgejo": {
			Adaptation: s.adaptationPath, APIBase: s.server.URL,
			WebhookSecretFile: s.tokenPath, CredentialFile: s.tokenPath,
		},
	}
	repo := RepoConfig{Forge: "forgejo", Owner: "minos-e2e-owner", Repo: "subject"}
	repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"
	facts := Facts{
		Forge: "forgejo", Owner: repo.Owner, Repo: repo.Repo,
		PR: pullRequestNumber, Occasion: "pr-opened",
	}
	return cfg, repo, facts
}

func (s *forgejoFixtureState) setStackedChildren(children []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stackedChildren = children
}

func (s *forgejoFixtureState) operatorPullRequest(number int, head string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(s.pullRequest)
	if err != nil {
		s.t.Fatal(err)
	}
	var pullRequest map[string]any
	if err := json.Unmarshal(data, &pullRequest); err != nil {
		s.t.Fatal(err)
	}
	pullRequest["number"] = float64(number)
	pullRequest["head"].(map[string]any)["sha"] = head
	return pullRequest
}

func (s *forgejoFixtureState) setOperatorPullRequests(pullRequests []operatorFixturePullRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.operatorPullRequests = pullRequests
}

// operatorPullRequestForPath resolves a request made by the operator-listing
// journey. The caller holds the fixture mutex.
func (s *forgejoFixtureState) operatorPullRequestForPath(path string) *operatorFixturePullRequest {
	for index := range s.operatorPullRequests {
		pullRequest := &s.operatorPullRequests[index]
		prefix := fmt.Sprintf("/api/v1/repos/minos-e2e-owner/subject/pulls/%v", pullRequest.pullRequest["number"])
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return pullRequest
		}
	}
	return nil
}

func (s *forgejoFixtureState) operatorPullRequestForIssuePath(path string) *operatorFixturePullRequest {
	for index := range s.operatorPullRequests {
		pullRequest := &s.operatorPullRequests[index]
		prefix := fmt.Sprintf("/api/v1/repos/minos-e2e-owner/subject/issues/%v", pullRequest.pullRequest["number"])
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return pullRequest
		}
	}
	return nil
}

// stackedChild resolves a request path to a fixture child pull request. The
// caller already holds the fixture mutex.
func (s *forgejoFixtureState) stackedChild(path string) map[string]any {
	for _, child := range s.stackedChildren {
		if path == fmt.Sprintf("/api/v1/repos/minos-e2e-owner/subject/pulls/%v", child["number"]) {
			return child
		}
	}
	return nil
}

func (s *forgejoFixtureState) stackedChildForPath(path string) map[string]any {
	for _, child := range s.stackedChildren {
		number := fmt.Sprint(child["number"])
		if strings.Contains(path, "/pulls/"+number+"/") || strings.Contains(path, "/issues/"+number) {
			return child
		}
	}
	return nil
}

func (s *forgejoFixtureState) headSHA() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pullRequest["head"].(map[string]any)["sha"].(string)
}

func (s *forgejoFixtureState) targetSHA() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pullRequest["base"].(map[string]any)["sha"].(string)
}

func (s *forgejoFixtureState) changePullRequest(change func(map[string]any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s.pullRequest)
}

func (s *forgejoFixtureState) setReviews(reviews []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reviews = reviews
}

func (s *forgejoFixtureState) setReviewWithComments(review map[string]any, comments []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reviews = []map[string]any{review}
	id := int64(review["id"].(float64))
	s.reviewComments[id] = comments
}

func (s *forgejoFixtureState) setDiffNewSide(intervals map[string][][2]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diffNewSide = intervals
}

func (s *forgejoFixtureState) setPositionRewrite(path string, requested, stored int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.positionRewrites[path] == nil {
		s.positionRewrites[path] = make(map[int64]int64)
	}
	s.positionRewrites[path][requested] = stored
}

func (s *forgejoFixtureState) reviewWriteFacts() (int, map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reviewPayloads) == 0 {
		return s.reviewWrites, nil
	}
	return s.reviewWrites, s.reviewPayloads[len(s.reviewPayloads)-1]
}

func (s *forgejoFixtureState) setStatuses(statuses []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = statuses
	head := s.pullRequest["head"].(map[string]any)["sha"].(string)
	s.statusesByCommit[head] = statuses
}

func (s *forgejoFixtureState) setIssueComments(comments []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issueComments = comments
}

func (s *forgejoFixtureState) setCommits(commits []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commits = commits
}

func (s *forgejoFixtureState) setDependencies(dependencies []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dependencies = dependencies
	s.dependencyPages = nil
	s.dependencyCode = http.StatusOK
}

func (s *forgejoFixtureState) setDependencyPages(pages [][]map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dependencyPages = pages
	s.dependencyCode = http.StatusOK
}

func (s *forgejoFixtureState) setDependenciesFailure(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dependencyCode = status
}

func (s *forgejoFixtureState) setAnnexeCloneURL(cloneURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.annexeCloneURL = cloneURL
}

func (s *forgejoFixtureState) statusReadFacts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.statusReadCommits...)
}

func (s *forgejoFixtureState) pullRequestSnapshotReads(pullRequest string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pullRequestReads[pullRequest]
}

func (s *forgejoFixtureState) setPriorityBoundaryMutation(mutate func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.priorityBoundaryMutation = mutate
}

func (s *forgejoFixtureState) runPriorityBoundaryMutation() {
	mutate := s.priorityBoundaryMutation
	if mutate == nil {
		return
	}
	s.priorityBoundaryMutation = nil
	s.mu.Unlock()
	mutate()
	s.mu.Lock()
}

func (s *forgejoFixtureState) statusWriteFacts() (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var target any
	if len(s.statuses) > 0 {
		target = s.statuses[0]["target_url"]
	}
	return s.statusWrites, target
}

func (s *forgejoFixtureState) statusPostFacts() []statusPostRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	posts := make([]statusPostRequest, 0, len(s.statusPostRequests))
	for _, request := range s.statusPostRequests {
		posts = append(posts, statusPostRequest{Head: request.Head, Payload: mapsClone(request.Payload)})
	}
	return posts
}

func (s *forgejoFixtureState) issueCommentFacts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	comments := make([]string, 0, len(s.issueComments))
	for _, comment := range s.issueComments {
		comments = append(comments, fmt.Sprint(comment["body"]))
	}
	return comments
}

func (s *forgejoFixtureState) virtualBranchReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.virtualRefLookups
}

func (s *forgejoFixtureState) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	pullPath := fmt.Sprintf("/api/v1/repos/minos-e2e-owner/subject/pulls/%v", s.pullRequest["number"])
	issuePath := fmt.Sprintf("/api/v1/repos/minos-e2e-owner/subject/issues/%v", s.pullRequest["number"])
	switch {
	case r.Method == http.MethodGet && path == "/api/v1/user":
		writeFixtureJSON(s.t, w, map[string]any{"login": "Minos"})
	case r.Method == http.MethodGet && path == "/api/v1/repos/minos-e2e-owner/subject":
		writeFixtureJSON(s.t, w, s.repository)
	case r.Method == http.MethodGet && path == "/api/v1/repos/minos-e2e-owner/subject-Annexe":
		if s.annexeCloneURL == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeFixtureJSON(s.t, w, map[string]any{"clone_url": s.annexeCloneURL})
	case r.Method == http.MethodGet && path == "/api/v1/repos/minos-e2e-owner/subject/pulls" && len(s.operatorPullRequests) > 0:
		pullRequests := make([]map[string]any, 0, len(s.operatorPullRequests))
		for _, pullRequest := range s.operatorPullRequests {
			pullRequests = append(pullRequests, pullRequest.pullRequest)
		}
		writeFixtureJSON(s.t, w, pullRequests)
	case r.Method == http.MethodGet && path == "/api/v1/repos/minos-e2e-owner/subject/pulls":
		writeFixtureJSON(s.t, w, append([]map[string]any{s.pullRequest}, s.stackedChildren...))
	case r.Method == http.MethodGet && s.operatorPullRequestForPath(path) != nil && !strings.Contains(strings.TrimPrefix(path, "/api/v1/repos/minos-e2e-owner/subject/pulls/"), "/"):
		writeFixtureJSON(s.t, w, s.operatorPullRequestForPath(path).pullRequest)
	case r.Method == http.MethodGet && s.operatorPullRequestForPath(path) != nil && strings.HasSuffix(path, "/dependencies"):
		writeFixtureJSON(s.t, w, []map[string]any{})
	case r.Method == http.MethodGet && s.operatorPullRequestForPath(path) != nil && strings.HasSuffix(path, "/reviews"):
		writeFixtureJSON(s.t, w, s.operatorPullRequestForPath(path).reviews)
	case r.Method == http.MethodGet && s.operatorPullRequestForIssuePath(path) != nil && strings.HasSuffix(path, "/dependencies"):
		writeFixtureJSON(s.t, w, []map[string]any{})
	case r.Method == http.MethodGet && s.operatorPullRequestForIssuePath(path) != nil && strings.HasSuffix(path, "/comments"):
		writeFixtureJSON(s.t, w, s.operatorPullRequestForIssuePath(path).comments)
	case r.Method == http.MethodGet && path == pullPath:
		s.pullRequestReads[fmt.Sprint(s.pullRequest["number"])]++
		writeFixtureJSON(s.t, w, s.pullRequest)
	case r.Method == http.MethodGet && s.stackedChild(path) != nil:
		s.pullRequestReads[fmt.Sprint(s.stackedChild(path)["number"])]++
		writeFixtureJSON(s.t, w, s.stackedChild(path))
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/dependencies") && s.stackedChildForPath(path) != nil:
		child := s.stackedChildForPath(path)
		dependencies, _ := child["dependencies"].([]map[string]any)
		if dependencies == nil {
			dependencies = []map[string]any{}
		}
		writeFixtureJSON(s.t, w, dependencies)
	case r.Method == http.MethodGet && path != issuePath+"/dependencies" && strings.Contains(path, "/issues/") && strings.HasSuffix(path, "/dependencies"):
		writeFixtureJSON(s.t, w, []map[string]any{})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/reviews") && s.stackedChildForPath(path) != nil:
		child := s.stackedChildForPath(path)
		reviews, _ := child["reviews"].([]map[string]any)
		if reviews == nil {
			reviews = []map[string]any{}
		}
		writeFixtureJSON(s.t, w, reviews)
	case r.Method == http.MethodPatch && s.stackedChild(path) != nil:
		if s.childRetargetCode != 0 {
			http.Error(w, "retarget fixture failure", s.childRetargetCode)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
		}
		child := s.stackedChild(path)
		if base, ok := payload["base"].(string); ok {
			child["base"].(map[string]any)["ref"] = base
		}
		s.childRetargetWrites++
		if !s.sourceBranchExists {
			s.childRetargetAfterDelete = true
		}
		writeFixtureJSON(s.t, w, child)
	case r.Method == http.MethodGet && path == issuePath+"/dependencies":
		if s.dependencyCode != http.StatusOK {
			http.Error(w, "dependency fixture failure", s.dependencyCode)
			return
		}
		if s.dependencyPages != nil {
			page, err := strconv.Atoi(r.URL.Query().Get("page"))
			if err != nil || page < 1 {
				page = 1
			}
			if page > len(s.dependencyPages) {
				writeFixtureJSON(s.t, w, []map[string]any{})
				return
			}
			writeFixtureJSON(s.t, w, s.dependencyPages[page-1])
			return
		}
		writeFixtureJSON(s.t, w, s.dependencies)
	case r.Method == http.MethodGet && path == issuePath+"/comments":
		writeFixtureJSON(s.t, w, s.issueComments)
	case r.Method == http.MethodPost && path == issuePath+"/comments":
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
		}
		comment := map[string]any{
			"id": float64(len(s.issueComments) + 1), "body": payload["body"],
			"user": map[string]any{"login": "Minos"},
		}
		s.issueComments = append(s.issueComments, comment)
		writeFixtureJSON(s.t, w, comment)
	case r.Method == http.MethodGet && path == pullPath+"/commits":
		writeFixtureJSON(s.t, w, s.commits)
	case r.Method == http.MethodGet && strings.Contains(path, "/pulls/") && strings.HasSuffix(path, "/commits"):
		child := s.stackedChildForPath(path)
		commits, _ := child["commits"].([]map[string]any)
		if commits == nil {
			commits = []map[string]any{{"sha": child["head"].(map[string]any)["sha"], "author": map[string]any{"login": "fixture-author"}}}
		}
		writeFixtureJSON(s.t, w, commits)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/branches/main"):
		base := s.pullRequest["base"].(map[string]any)
		writeFixtureJSON(s.t, w, map[string]any{
			"commit": map[string]any{"id": base["sha"]}, "protected": false,
			"user_can_merge": true, "status_check_contexts": []string{},
		})
	case r.Method == http.MethodDelete && strings.Contains(path, "/branches/"):
		if !s.sourceBranchExists {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		s.sourceBranchExists = false
		s.branchDeleteWrites++
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.Contains(path, "/branches/"):
		if strings.Contains(path, "refs/pull/") {
			s.virtualRefLookups++
		}
		if !s.sourceBranchExists {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeFixtureJSON(s.t, w, map[string]any{"protected": false})
		if s.pullRequestReads[fmt.Sprint(s.pullRequest["number"])] > 0 {
			s.runPriorityBoundaryMutation()
		}
	case r.Method == http.MethodPost && path == pullPath+"/merge":
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
		}
		if payload["head_commit_id"] != s.pullRequest["head"].(map[string]any)["sha"] {
			http.Error(w, "head mismatch", http.StatusConflict)
			return
		}
		s.pullRequest["merged"] = true
		s.pullRequest["state"] = "closed"
		s.mergeWrites++
		if s.sourceBranchExists && payload["delete_branch_after_merge"] == true {
			s.sourceBranchExists = false
			s.branchDeletedByMerge = true
			headRef := s.pullRequest["head"].(map[string]any)["ref"]
			baseRef := s.pullRequest["base"].(map[string]any)["ref"]
			for _, child := range s.stackedChildren {
				base := child["base"].(map[string]any)
				if child["state"] == "open" && base["ref"] == headRef {
					base["ref"] = baseRef
				}
			}
		}
		writeFixtureJSON(s.t, w, map[string]any{})
	case r.Method == http.MethodGet && strings.Contains(path, "/commits/") && strings.HasSuffix(path, "/statuses"):
		commit := strings.TrimSuffix(strings.SplitN(path, "/commits/", 2)[1], "/statuses")
		for _, pullRequest := range s.operatorPullRequests {
			if pullRequest.pullRequest["head"].(map[string]any)["sha"] == commit {
				writeFixtureJSON(s.t, w, pullRequest.statuses)
				return
			}
		}
		s.statusReadCommits = append(s.statusReadCommits, commit)
		statuses := s.statusesByCommit[commit]
		childStatuses := false
		for _, child := range s.stackedChildren {
			if child["head"].(map[string]any)["sha"] == commit {
				statuses, _ = child["statuses"].([]map[string]any)
				if statuses == nil {
					statuses = []map[string]any{}
				}
				childStatuses = true
				break
			}
		}
		if !childStatuses && len(s.statusesByCommit) == 0 {
			statuses = s.statuses
		}
		writeFixtureJSON(s.t, w, statuses)
		if s.pullRequestReads[fmt.Sprint(s.pullRequest["number"])] == 0 {
			s.runPriorityBoundaryMutation()
		}
	case r.Method == http.MethodGet && path == "/api/v1/repos/minos-e2e-owner/subject/actions/runs":
		writeFixtureJSON(s.t, w, map[string]any{"workflow_runs": s.actionRuns})
	case r.Method == http.MethodGet && strings.Contains(path, "/actions/runs/") && strings.HasSuffix(path, "/jobs"):
		idText := strings.TrimSuffix(strings.SplitN(path, "/actions/runs/", 2)[1], "/jobs")
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil {
			http.Error(w, "bad run id", http.StatusBadRequest)
			return
		}
		writeFixtureJSON(s.t, w, s.actionJobs[id])
	case r.Method == http.MethodGet && strings.Contains(path, "/actions/jobs/") && strings.HasSuffix(path, "/logs"):
		idText := strings.TrimSuffix(strings.SplitN(path, "/actions/jobs/", 2)[1], "/logs")
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil {
			http.Error(w, "bad job id", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(s.actionLogs[id]))
	case r.Method == http.MethodPost && strings.Contains(path, "/statuses/"):
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
		}
		nextID := float64(1)
		for _, status := range s.statuses {
			var id float64
			switch value := status["id"].(type) {
			case int:
				id = float64(value)
			case float64:
				id = value
			}
			if id >= nextID {
				nextID = id + 1
			}
		}
		payload["id"] = nextID
		payload["creator"] = map[string]any{"login": "Minos"}
		head := strings.TrimPrefix(path, "/api/v1/repos/minos-e2e-owner/subject/statuses/")
		s.statuses = append([]map[string]any{payload}, s.statuses...)
		s.statusesByCommit[head] = append([]map[string]any{payload}, s.statusesByCommit[head]...)
		for _, child := range s.stackedChildren {
			if child["head"].(map[string]any)["sha"] == head {
				statuses, _ := child["statuses"].([]map[string]any)
				child["statuses"] = append([]map[string]any{payload}, statuses...)
				break
			}
		}
		s.statusWrites++
		s.writeSequence = append(s.writeSequence, "status:"+fmt.Sprint(payload["description"]))
		s.statusPostRequests = append(s.statusPostRequests, statusPostRequest{Head: head, Payload: mapsClone(payload)})
		writeFixtureJSON(s.t, w, payload)
	case r.Method == http.MethodGet && path == pullPath+"/reviews":
		writeFixtureJSON(s.t, w, s.reviews)
	case r.Method == http.MethodPost && path == pullPath+"/reviews":
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
		}
		nextID := int64(len(s.reviews) + 1)
		review := map[string]any{
			"id": nextID, "state": payload["event"], "commit_id": payload["commit_id"],
			"body": payload["body"], "user": map[string]any{"login": "Minos"},
		}
		var comments []map[string]any
		if requestedComments, ok := payload["comments"].([]any); ok {
			for index, raw := range requestedComments {
				comment := mapsClone(raw.(map[string]any))
				comment["id"] = index + 1
				comment["pull_request_review_id"] = nextID
				path, _ := comment["path"].(string)
				requestedLine := int64(comment["new_position"].(float64))
				if lineInIntervals(requestedLine, s.diffNewSide[path]) {
					storedLine := requestedLine
					if rewrites := s.positionRewrites[path]; rewrites != nil {
						if rewritten, ok := rewrites[requestedLine]; ok {
							storedLine = rewritten
						}
					}
					comment["position"] = storedLine
					comment["diff_hunk"] = fmt.Sprintf("@@ -%d,3 +%d,3 @@", requestedLine, requestedLine)
				} else {
					comment["position"] = widgetUnanchoredPosition
					comment["diff_hunk"] = ""
				}
				comment["original_position"] = float64(0)
				delete(comment, "new_position")
				comments = append(comments, comment)
			}
		}
		s.reviews = append(s.reviews, review)
		s.reviewComments[nextID] = comments
		s.reviewWrites++
		s.reviewPayloads = append(s.reviewPayloads, payload)
		writeFixtureJSON(s.t, w, review)
	case r.Method == http.MethodGet && strings.HasPrefix(path, pullPath+"/reviews/") && strings.HasSuffix(path, "/comments"):
		trimmed := strings.TrimSuffix(strings.TrimPrefix(path, pullPath+"/reviews/"), "/comments")
		id, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			http.Error(w, "invalid review id", http.StatusBadRequest)
			return
		}
		writeFixtureJSON(s.t, w, s.reviewComments[id])
	case r.Method == http.MethodGet && (path == issuePath || s.stackedChildForPath(path) != nil && strings.HasSuffix(path, "/issues/"+fmt.Sprint(s.stackedChildForPath(path)["number"]))):
		assignees := make([]map[string]any, 0, len(s.assignees))
		for _, login := range s.assignees {
			assignees = append(assignees, map[string]any{"login": login})
		}
		writeFixtureJSON(s.t, w, map[string]any{"assignees": assignees})
	case r.Method == http.MethodPost && (path == issuePath+"/assignees" || s.stackedChildForPath(path) != nil && strings.HasSuffix(path, "/assignees")):
		s.obsoleteAssignmentWrites++
		http.Error(w, "route not found", http.StatusNotFound)
	case r.Method == http.MethodPatch && (path == issuePath || s.stackedChildForPath(path) != nil && strings.HasSuffix(path, "/issues/"+fmt.Sprint(s.stackedChildForPath(path)["number"]))):
		var payload struct {
			Assignees []string `json:"assignees"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
		}
		for _, login := range payload.Assignees {
			if !slices.Contains(s.assignees, login) {
				s.assignees = append(s.assignees, login)
			}
		}
		s.assignmentWrites++
		s.assignmentWritePaths = append(s.assignmentWritePaths, path)
		writeFixtureJSON(s.t, w, map[string]any{})
	case r.Method == http.MethodGet && (path == issuePath+"/reactions" || s.stackedChildForPath(path) != nil && strings.HasSuffix(path, "/reactions")):
		reactions := make([]map[string]any, 0, len(s.reactions))
		for _, content := range s.reactions {
			reactions = append(reactions, map[string]any{"content": content, "user": map[string]any{"login": "Minos"}})
		}
		writeFixtureJSON(s.t, w, reactions)
	case r.Method == http.MethodPost && (path == issuePath+"/reactions" || s.stackedChildForPath(path) != nil && strings.HasSuffix(path, "/reactions")):
		var payload struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
		}
		if !slices.Contains(s.reactions, payload.Content) {
			s.reactions = append(s.reactions, payload.Content)
		}
		s.reactionWrites++
		s.reactionWritePaths = append(s.reactionWritePaths, path)
		writeFixtureJSON(s.t, w, map[string]any{
			"content":    payload.Content,
			"created_at": "2026-07-19T12:00:00Z",
			"user":       map[string]any{"login": "Minos"},
		})
	case r.Method == http.MethodDelete && path == issuePath+"/reactions":
		var payload struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
		}
		for index, content := range s.reactions {
			if content == payload.Content {
				s.reactions = append(s.reactions[:index], s.reactions[index+1:]...)
				break
			}
		}
		s.reactionDeleteWrites++
		s.writeSequence = append(s.writeSequence, "reaction-remove:"+payload.Content)
		writeFixtureJSON(s.t, w, map[string]any{})
	case r.Method == http.MethodGet && path == issuePath+"/labels":
		writeFixtureJSON(s.t, w, s.pullRequest["labels"])
	case r.Method == http.MethodDelete && strings.HasPrefix(path, issuePath+"/labels/"):
		name := strings.TrimPrefix(path, issuePath+"/labels/")
		labels, _ := s.pullRequest["labels"].([]any)
		kept := make([]any, 0, len(labels))
		for _, raw := range labels {
			label := raw.(map[string]any)
			if label["name"] != name {
				kept = append(kept, raw)
			}
		}
		s.pullRequest["labels"] = kept
		s.labelDeleteWrites++
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, fmt.Sprintf("unexpected fixture request %s %s", r.Method, path), http.StatusNotFound)
	}
}

func writeFixtureJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func writeServiceConfig(t *testing.T, cfg ServiceConfig) {
	t.Helper()
	body := fmt.Sprintf(`[service]
bot-login = %q
[listener]
bind = ":0"
[forges.forgejo]
adaptation = %q
api-base = %q
webhook-secret-file = %q
credential-file = %q
[runs]
dir = %q
`, cfg.Service.BotLogin, cfg.Forges["forgejo"].Adaptation, cfg.Forges["forgejo"].APIBase,
		cfg.Forges["forgejo"].WebhookSecretFile, cfg.Forges["forgejo"].CredentialFile, cfg.Runs.Dir)
	if err := os.WriteFile(filepath.Join(cfg.Root, "service.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
