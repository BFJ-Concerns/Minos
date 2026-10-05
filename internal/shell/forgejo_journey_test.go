package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
)

func TestForgejoAdmissionUsesFreshPullRequestSnapshot(t *testing.T) {
	t.Run("draft pull request is not started", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.changePullRequest(func(pullRequest map[string]any) {
			pullRequest["draft"] = true
			pullRequest["head"].(map[string]any)["ref"] = "structural/draft"
		})
		cfg, repo, facts := state.service(t)

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

	t.Run("open dependency defers until the dependency closes", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.setDependencies([]map[string]any{{
			"number": 7, "state": "open",
			"repository": map[string]any{"full_name": "minos-e2e-owner/prerequisite"},
		}})
		cfg, repo, facts := state.service(t)
		state.changePullRequest(func(pullRequest map[string]any) {
			pullRequest["head"].(map[string]any)["ref"] = "feature/blocked"
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
		if _, found := systemdEnvironment(t, systemdArgs)["MINOS_RUN_CLASS"]; found {
			t.Fatalf("systemd environment still carries MINOS_RUN_CLASS: %#v", systemdEnvironment(t, systemdArgs))
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

	t.Run("latest owned clean status on the current head suppresses a new run", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, facts := state.service(t)
		state.setStatuses([]map[string]any{{
			"id": 7, "context": "Minos", "status": "success", "description": product.Clean().Description(),
			"target_url": state.server.URL + "/minos-e2e-owner/subject/pulls/2#minos-target-earlier",
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

	t.Run("failure status carrying a clean description starts a fresh run", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.setStatuses([]map[string]any{{
			"id": 7, "context": "Minos", "status": "failure", "description": product.Clean().Description(),
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

	t.Run("incomplete pull request with a comment review starts a fresh attempt", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.setReviews([]map[string]any{{
			"id": 41, "state": "COMMENT", "commit_id": state.headSHA(),
			"body": "Review incomplete: confirmed findings still require verification.",
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

func stackedFixturePull(number float64, baseRef, headRef string) map[string]any {
	return map[string]any{
		"number": number, "state": "open", "merged": false, "draft": false,
		"user": map[string]any{"login": "fixture-author"},
		"base": map[string]any{"ref": baseRef, "sha": "target-" + baseRef, "repo": map[string]any{"full_name": "minos-e2e-owner/subject"}},
		"head": map[string]any{"ref": headRef, "sha": "head-" + headRef, "repo": map[string]any{"full_name": "minos-e2e-owner/subject"}},
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
		if writes, _ := state.statusWriteFacts(); writes != 0 {
			t.Fatalf("status writes = %d, want no reconciliation write", writes)
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

func TestForgejoReconciliationStripsStaleApprovalReaction(t *testing.T) {
	staleApproval := func(t *testing.T) *forgejoFixtureState {
		t.Helper()
		state := newForgejoFixtureState(t)
		approvedHead := state.headSHA()
		state.reactions = []string{"+1"}
		state.setReviews([]map[string]any{{
			"id": 41, "state": "APPROVED", "commit_id": approvedHead,
			"body": "approved before the author pushed again", "user": map[string]any{"login": "Minos"},
		}})
		state.changePullRequest(func(pullRequest map[string]any) {
			pullRequest["head"].(map[string]any)["sha"] = "head-after-approval"
		})
		return state
	}

	t.Run("sweep removes it before a deferred admission can claim", func(t *testing.T) {
		state := staleApproval(t)
		state.setDependencies([]map[string]any{{
			"number": 7, "state": "open",
			"repository": map[string]any{"full_name": "minos-e2e-owner/prerequisite"},
		}})
		cfg := writeSweepFixtureConfig(t, state)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "systemd-run" {
				t.Fatalf("stale-approval sweep claimed a deferred pull request")
			}
			return nil, nil
		}

		if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
			t.Fatal(err)
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if slices.Contains(state.reactions, "+1") || state.reactionDeleteWrites != 1 {
			t.Fatalf("reactions = %v, removal writes = %d, want stale +1 removed once", state.reactions, state.reactionDeleteWrites)
		}
		if !slices.Equal(state.writeSequence, []string{"reaction-remove:+1"}) {
			t.Fatalf("forge writes = %v, want only the stale reaction removal", state.writeSequence)
		}
		if len(state.reviews) != 1 || state.reviews[0]["commit_id"] == state.pullRequest["head"].(map[string]any)["sha"] {
			t.Fatalf("reviews = %#v, want the stale review retained", state.reviews)
		}
	})

	t.Run("webhook removes it before a work-in-progress admission defers", func(t *testing.T) {
		state := staleApproval(t)
		state.changePullRequest(func(pullRequest map[string]any) {
			pullRequest["head"].(map[string]any)["ref"] = "structural/stale-approval"
		})
		cfg, _, _ := state.service(t)
		cfg.Forges["local"] = cfg.Forges["forgejo"]
		delete(cfg.Forges, "forgejo")
		if err := os.WriteFile(cfg.Forges["local"].WebhookSecretFile, []byte("secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(cfg.Root, "repos"), 0o755); err != nil {
			t.Fatal(err)
		}
		repoConfig := `forge = "local"
owner = "minos-e2e-owner"
repo = "subject"
work-in-progress-branch-prefixes = ["structural/"]
[adaptation]
run-body = "/opt/minos/run-body/run-body"
`
		if err := os.WriteFile(filepath.Join(cfg.Root, "repos", "subject.toml"), []byte(repoConfig), 0o600); err != nil {
			t.Fatal(err)
		}

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Fatalf("stale-approval webhook reached admission command %s", name)
			return nil, nil
		}

		fixture := readFixture(t, "001-pull_request-opened.json")
		response, err := sendAuthenticatedHook(t, cfg, "pull_request", fixture.Body)
		if err != nil {
			t.Fatalf("handleHook() error = %v", err)
		}
		if response.Code != http.StatusAccepted || response.Body.String() != "deferred: work-in-progress branch \"structural/stale-approval\"\n" {
			t.Fatalf("webhook response = %d %q", response.Code, response.Body.String())
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if slices.Contains(state.reactions, "+1") || state.reactionDeleteWrites != 1 {
			t.Fatalf("reactions = %v, removal writes = %d, want stale +1 removed once", state.reactions, state.reactionDeleteWrites)
		}
	})

	t.Run("unmoved head keeps its reaction", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.reactions = []string{"+1"}
		state.setReviews([]map[string]any{{
			"id": 41, "state": "APPROVED", "commit_id": state.headSHA(),
			"body": "approval at the current head", "user": map[string]any{"login": "Minos"},
		}})
		cfg := writeSweepFixtureConfig(t, state)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "systemd-run" {
				t.Fatalf("current-head pull request sweep claimed a completed pull request")
			}
			return nil, nil
		}

		if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
			t.Fatal(err)
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if !slices.Contains(state.reactions, "+1") || state.reactionDeleteWrites != 0 {
			t.Fatalf("reactions = %v, removal writes = %d, want current-head +1 retained", state.reactions, state.reactionDeleteWrites)
		}
	})

	t.Run("re-approved head keeps its reaction despite its historical approval", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		approvedHead := state.headSHA()
		state.reactions = []string{"+1"}
		state.setReviews([]map[string]any{{
			"id": 41, "state": "APPROVED", "commit_id": approvedHead,
			"body": "approval before the author pushed again", "user": map[string]any{"login": "Minos"},
		}, {
			"id": 42, "state": "APPROVED", "commit_id": "head-after-re-review",
			"body": "approval after the author pushed again", "user": map[string]any{"login": "Minos"},
		}})
		state.changePullRequest(func(pullRequest map[string]any) {
			pullRequest["head"].(map[string]any)["sha"] = "head-after-re-review"
		})
		cfg := writeSweepFixtureConfig(t, state)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "systemd-run" {
				t.Fatalf("re-approved pull request sweep claimed a completed pull request")
			}
			return nil, nil
		}

		if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
			t.Fatal(err)
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if !slices.Contains(state.reactions, "+1") || state.reactionDeleteWrites != 0 {
			t.Fatalf("reactions = %v, removal writes = %d, want re-approved +1 retained", state.reactions, state.reactionDeleteWrites)
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
	return writeSweepFixtureConfigWithKnobs(t, state, "")
}

// writeSweepFixtureConfigWithKnobs writes the sweep fixture's configuration
// with the given tables appended to the repository's own file.
func writeSweepFixtureConfigWithKnobs(t *testing.T, state *forgejoFixtureState, knobs string) ServiceConfig {
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
` + knobs
	if err := os.WriteFile(filepath.Join(cfg.Root, "repos", "subject.toml"), []byte(repoConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// labelMarkerKnobs configures every marker as a label.
const labelMarkerKnobs = `[markers]
in-flight = {label = "minos/reviewing"}
clean = {label = "minos/approved"}
attention = {label = "minos/attention"}
`

const labelMarkersJSON = `{"in-flight":{"label":"minos/reviewing"},"clean":{"label":"minos/approved"},"attention":{"label":"minos/attention"}}`

// defineMarkerLabels gives the fixture repository the marker labels beside
// an unrelated one.
func defineMarkerLabels(state *forgejoFixtureState) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.repositoryLabels = []map[string]any{
		{"id": float64(7), "name": "Ready", "color": "336699"},
		{"id": float64(21), "name": "minos/reviewing", "color": "fbca04"},
		{"id": float64(22), "name": "minos/approved", "color": "0e8a16"},
		{"id": float64(23), "name": "minos/attention", "color": "d93f0b"},
	}
}

func TestForgeMarkersWriteTheConfiguredLabelForm(t *testing.T) {
	state := newForgejoFixtureState(t)
	defineMarkerLabels(state)
	configureForgeCommandFixture(t, state)
	t.Setenv("MINOS_MARKERS", labelMarkersJSON)

	for attempt := 1; attempt <= 2; attempt++ {
		if err := ForgeCommand(t.Context(), []string{"claim"}, &bytes.Buffer{}); err != nil {
			t.Fatalf("claim attempt %d: %v", attempt, err)
		}
	}
	state.mu.Lock()
	if labels := state.pullRequestLabelNames(); !slices.Contains(labels, "minos/reviewing") || state.labelWrites != 1 {
		t.Fatalf("claim labels = %v, label writes = %d, want minos/reviewing written once", labels, state.labelWrites)
	}
	if len(state.reactions) != 0 || state.reactionWrites != 0 {
		t.Fatalf("claim reactions = %v, writes = %d, want no reaction in the label form", state.reactions, state.reactionWrites)
	}
	state.mu.Unlock()

	head, target := state.headSHA(), state.targetSHA()
	for attempt := 1; attempt <= 2; attempt++ {
		for _, args := range [][]string{{"marker", head, target, "clean", "add"}, {"marker", head, target, "in-flight", "remove"}} {
			if err := ForgeCommand(t.Context(), args, &bytes.Buffer{}); err != nil {
				t.Fatalf("clean outcome attempt %d %v: %v", attempt, args, err)
			}
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if labels := state.pullRequestLabelNames(); !slices.Equal(labels, []string{"minos/approved"}) {
		t.Fatalf("clean outcome labels = %v, want the in-flight label swapped for the clean one", labels)
	}
	if !slices.Equal(state.writeSequence, []string{"label-add:minos/reviewing", "label-add:minos/approved", "label-remove:minos/reviewing"}) {
		t.Fatalf("label writes = %v", state.writeSequence)
	}
	if len(state.reactions) != 0 || state.reactionWrites != 0 || state.reactionDeleteWrites != 0 {
		t.Fatalf("reactions = %v, writes = %d/%d, want none in the label form", state.reactions, state.reactionWrites, state.reactionDeleteWrites)
	}
}

func TestForgeMarkerRefusesALabelTheRepositoryDoesNotDefine(t *testing.T) {
	state := newForgejoFixtureState(t)
	configureForgeCommandFixture(t, state)
	t.Setenv("MINOS_MARKERS", labelMarkersJSON)

	for _, args := range [][]string{{"claim"}, {"marker", state.headSHA(), state.targetSHA(), "clean", "add"}} {
		var stdout bytes.Buffer
		err := ForgeCommand(t.Context(), args, &stdout)
		if err == nil || !strings.Contains(stdout.String(), `"outcome":"rejected"`) ||
			!strings.Contains(stdout.String(), "is not defined on minos-e2e-owner/subject or its organisation") {
			t.Fatalf("%v: error = %v, output = %q, want a rejection naming the label and repository", args, err, stdout.String())
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.labelWrites != 0 || len(state.repositoryLabels) != 0 {
		t.Fatalf("label writes = %d, repository labels = %v, want nothing written or created", state.labelWrites, state.repositoryLabels)
	}
}

func TestForgeMarkerFindsAnOrganisationLabelOnALaterPage(t *testing.T) {
	state := newForgejoFixtureState(t)
	unrelated := make([]map[string]any, 0, 50)
	for index := range 50 {
		unrelated = append(unrelated, map[string]any{"id": float64(100 + index), "name": fmt.Sprintf("area/%d", index)})
	}
	state.mu.Lock()
	state.organisationLabelPages = [][]map[string]any{unrelated, {{"id": float64(51), "name": "minos/approved"}}}
	state.mu.Unlock()
	configureForgeCommandFixture(t, state)
	t.Setenv("MINOS_MARKERS", labelMarkersJSON)

	var stdout bytes.Buffer
	if err := ForgeCommand(t.Context(), []string{"marker", state.headSHA(), state.targetSHA(), "clean", "add"}, &stdout); err != nil {
		t.Fatalf("organisation label on page 2: %v (%s)", err, stdout.String())
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if labels := state.pullRequestLabelNames(); !slices.Equal(labels, []string{"minos/approved"}) {
		t.Fatalf("labels = %v, want the organisation's minos/approved", labels)
	}
}

func TestForgeMarkerRefusesMarkersWithTrailingData(t *testing.T) {
	state := newForgejoFixtureState(t)
	defineMarkerLabels(state)
	configureForgeCommandFixture(t, state)
	for _, suffix := range []string{" trailing-garbage", "}", "]"} {
		t.Setenv("MINOS_MARKERS", labelMarkersJSON+suffix)
		for _, args := range [][]string{{"claim"}, {"marker", state.headSHA(), state.targetSHA(), "clean", "add"}} {
			if err := ForgeCommand(t.Context(), args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "MINOS_MARKERS") {
				t.Fatalf("suffix %q %v: error = %v, want MINOS_MARKERS refused", suffix, args, err)
			}
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.writeSequence) != 0 || state.labelWrites != 0 || state.reviewRequestWrites != 0 {
		t.Fatalf("writes = %v, want none from a malformed MINOS_MARKERS", state.writeSequence)
	}
}

func TestForgeMarkerSkipsAnUnconfiguredAttentionMarker(t *testing.T) {
	state := newForgejoFixtureState(t)
	configureForgeCommandFixture(t, state)

	var stdout bytes.Buffer
	if err := ForgeCommand(t.Context(), []string{"marker", state.headSHA(), state.targetSHA(), "attention", "add"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "attention marker is not configured") {
		t.Fatalf("output = %q", stdout.String())
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.writeSequence) != 0 || state.reactionWrites != 0 || state.labelWrites != 0 {
		t.Fatalf("writes = %v, want none for an unconfigured marker", state.writeSequence)
	}
}

func TestForgejoReconciliationStripsStaleLabelMarkersOnHeadMovement(t *testing.T) {
	state := newForgejoFixtureState(t)
	defineMarkerLabels(state)
	state.mu.Lock()
	state.setPullRequestLabels(state.repositoryLabels[2:])
	state.mu.Unlock()
	// The labels were earned by an earlier head; the author has pushed
	// since, so the current head carries no Minos result.
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["head"].(map[string]any)["sha"] = "head-after-the-push"
	})
	state.setDependencies([]map[string]any{{
		"number": 7, "state": "open",
		"repository": map[string]any{"full_name": "minos-e2e-owner/prerequisite"},
	}})
	cfg := writeSweepFixtureConfigWithKnobs(t, state, labelMarkerKnobs)

	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemd-run" {
			t.Fatal("stale-marker sweep claimed a deferred pull request")
		}
		return nil, nil
	}
	if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if labels := state.pullRequestLabelNames(); len(labels) != 0 {
		t.Fatalf("labels = %v, want the stale clean and attention labels stripped", labels)
	}
	if !slices.Equal(state.writeSequence, []string{"label-remove:minos/approved", "label-remove:minos/attention"}) {
		t.Fatalf("forge writes = %v, want only the stale label removals", state.writeSequence)
	}
}

func TestForgeClaimRequestsReviewAndReactsIdempotently(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")
	t.Setenv("MINOS_MARKERS", defaultMarkersJSON)

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
	if !slices.Contains(state.requestedReviewers, "Minos") {
		t.Fatalf("requested reviewers = %v, want Minos", state.requestedReviewers)
	}
	if !slices.Contains(state.reactions, "eyes") {
		t.Fatalf("reactions = %v, want eyes", state.reactions)
	}
	if state.reviewRequestWrites != 1 || state.reactionWrites != 1 {
		t.Fatalf("writes = review request:%d reaction:%d, want one each", state.reviewRequestWrites, state.reactionWrites)
	}
	if state.obsoleteAssignmentWrites != 0 {
		t.Fatalf("assignee routes received %d writes, want none", state.obsoleteAssignmentWrites)
	}
}

func TestForgeClaimRefusesReviewRequestMissingFromReadBack(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.reviewRequestsUnrecorded = true
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")
	t.Setenv("MINOS_MARKERS", defaultMarkersJSON)

	var stdout bytes.Buffer
	_ = ForgeCommand(t.Context(), []string{"claim"}, &stdout)
	if strings.Contains(stdout.String(), `"outcome":"applied"`) ||
		!strings.Contains(stdout.String(), "review request was not confirmed") {
		t.Fatalf("claim output = %q, want unconfirmed review request", stdout.String())
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.reviewRequestWrites != 1 {
		t.Fatalf("review request writes = %d, want 1", state.reviewRequestWrites)
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
	t.Setenv("MINOS_MARKERS", defaultMarkersJSON)

	content := apiShape.Request["content"].(string)
	if apiShape.Response["content"] != content || apiShape.Response["user"].(map[string]any)["login"] != "Minos" {
		t.Fatalf("reaction fixture does not preserve the Forgejo response shape: %#v", apiShape)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := ForgeCommand(t.Context(), []string{"marker", state.headSHA(), state.targetSHA(), "clean", "add"}, &bytes.Buffer{}); err != nil {
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

func TestForgeReactionRemoveUsesForgejo14ShapeAndReadBackIdempotency(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.reactions = []string{"eyes"}
	configureForgeCommandFixture(t, state)

	for attempt := 0; attempt < 2; attempt++ {
		if err := ForgeCommand(t.Context(), []string{"marker", state.headSHA(), state.targetSHA(), "in-flight", "remove"}, &bytes.Buffer{}); err != nil {
			t.Fatalf("reaction removal attempt %d: %v", attempt+1, err)
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if slices.Contains(state.reactions, "eyes") || state.reactionDeleteWrites != 1 {
		t.Fatalf("reactions = %v, delete writes = %d", state.reactions, state.reactionDeleteWrites)
	}
}

func TestForgeStatusWritesCarryTheConfiguredContext(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, facts := state.service(t)
	cfg.Service.StatusContext = "Review Bot"
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)
	t.Setenv("MINOS_PR", facts.PR)

	var stdout strings.Builder
	if err := ForgeCommand(t.Context(), []string{"status", state.headSHA(), state.targetSHA(), "working"}, &stdout); err != nil {
		t.Fatalf("status write: %v\n%s", err, stdout.String())
	}
	posts := state.statusPostFacts()
	if len(posts) != 1 {
		t.Fatalf("forge received %d status posts, want 1: %#v", len(posts), posts)
	}
	if context := posts[0].Payload["context"]; context != "Review Bot" {
		t.Fatalf("status context = %v, want the configured Review Bot", context)
	}
	if !strings.Contains(stdout.String(), "applied") {
		t.Fatalf("status write was not confirmed by read-back: %s", stdout.String())
	}
}

func TestGuardedSetStatusAcceptsOnlyTheExportedContext(t *testing.T) {
	for _, test := range []struct {
		name     string
		exported map[string]string
		context  string
		want     forge.WriteOutcome
	}{
		{name: "the exported context is written", exported: map[string]string{"MINOS_STATUS_CONTEXT": "Review Bot"}, context: "Review Bot", want: forge.WriteApplied},
		{name: "a context other than the exported one is rejected", exported: map[string]string{"MINOS_STATUS_CONTEXT": "Review Bot"}, context: "Minos", want: forge.WriteRejected},
		{name: "no exported context rejects every write", context: "Minos", want: forge.WriteRejected},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			cfg, _, facts := state.service(t)
			credential, err := ReadSecret(cfg.Forges[facts.Forge].CredentialFile)
			if err != nil {
				t.Fatal(err)
			}
			runner := forge.ScriptRunner{Directory: state.adaptationPath, APIBase: state.server.URL, Credential: credential}
			out, err := runner.Run(t.Context(), forge.RunRequest{
				Operation: "guarded-set-status",
				Arguments: []string{facts.Owner, facts.Repo, facts.PR, state.headSHA(), state.targetSHA(), "Minos",
					test.context, "pending", product.Working().Description()},
				Env: test.exported,
			})
			if err != nil {
				t.Fatalf("guarded-set-status: %v\n%s", err, out)
			}
			var result forge.WriteResult
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("decode %s: %v", out, err)
			}
			if result.Outcome != test.want {
				t.Fatalf("outcome = %q (%s), want %q", result.Outcome, result.Reason, test.want)
			}
			wantPosts := 0
			if test.want == forge.WriteApplied {
				wantPosts = 1
			}
			if posts := len(state.statusPostFacts()); posts != wantPosts {
				t.Fatalf("forge received %d status posts for outcome %q, want %d", posts, result.Outcome, wantPosts)
			}
		})
	}
}

func TestForgeCompletionMarkerReadsOnlyTheConfiguredContext(t *testing.T) {
	for _, test := range []struct {
		name         string
		context      string
		wantDecision ReconcileDecision
	}{
		{name: "clean status under the configured context suppresses a new run", context: "Review Bot", wantDecision: "nothing"},
		{name: "clean status under the old Minos context is ignored", context: "Minos", wantDecision: "started"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			state.setStatuses([]map[string]any{{
				"id": 7, "context": test.context, "status": "success", "description": product.Clean().Description(),
				"target_url": state.server.URL + "/minos-e2e-owner/subject/pulls/1#minos-target-" + state.targetSHA(),
				"creator":    map[string]any{"login": "Minos"},
			}})
			cfg, repo, facts := state.service(t)
			cfg.Service.StatusContext = "Review Bot"

			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				return nil, nil
			}
			result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
			if err != nil {
				t.Fatal(err)
			}
			if result.Decision != test.wantDecision {
				t.Fatalf("decision = %q, want %q", result.Decision, test.wantDecision)
			}
		})
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
					"target_url": fmt.Sprintf("%s/%s/%s/pulls/%s#minos-target-%s",
						strings.TrimSuffix(cfg.Forges[facts.Forge].APIBase, "/api/v1"), facts.Owner, facts.Repo, facts.PR, target),
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

// A run clones through the protocol: `minos forge repository-metadata` and
// `minos forge clone-credential` run the real Forgejo adaptation, whose
// lookup authenticates with the forge's token and whose HTTPS credential is
// that token.
func TestForgeRepositoryMetadataAndCloneCredentialServeTheRunsClones(t *testing.T) {
	state := newForgejoFixtureState(t)
	configureForgeCommandFixture(t, state)
	state.setSecondaryCloneURL("https://forge.example/minos-e2e-owner/subject-plans.git")

	var stdout bytes.Buffer
	if err := ForgeCommand(t.Context(), []string{"repository-metadata", "minos-e2e-owner", "subject-plans"}, &stdout); err != nil {
		t.Fatalf("repository-metadata: %v", err)
	}
	var metadata forge.RepositoryMetadata
	if err := json.Unmarshal(stdout.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata != (forge.RepositoryMetadata{CloneURL: "https://forge.example/minos-e2e-owner/subject-plans.git", DefaultBranch: "main"}) {
		t.Fatalf("repository metadata = %#v", metadata)
	}
	stdout.Reset()
	if err := ForgeCommand(t.Context(), []string{"repository-metadata", "minos-e2e-owner", "absent"}, &stdout); err == nil || strings.TrimSpace(stdout.String()) != `{"lookup_status":404}` {
		t.Fatalf("repository metadata for an absent repository: error %v, output %q; want the forge's 404 as data", err, stdout.String())
	}
	if err := ForgeCommand(t.Context(), []string{"repository-metadata", "minos-e2e-owner/subject-plans"}, &bytes.Buffer{}); err == nil {
		t.Fatalf("repository-metadata accepted one argument")
	}

	stdout.Reset()
	if err := ForgeCommand(t.Context(), []string{"clone-credential"}, &stdout); err != nil {
		t.Fatalf("clone-credential: %v", err)
	}
	var credential forge.CloneCredential
	if err := json.Unmarshal(stdout.Bytes(), &credential); err != nil {
		t.Fatal("clone-credential output is not a credential object")
	}
	if credential.Username == "" || credential.Password != "fixture-token" {
		t.Fatalf("clone credential username = %q, password is the forge token = %t", credential.Username, credential.Password == "fixture-token")
	}

	// The token is what authorises the lookup: another one is refused.
	if err := os.WriteFile(state.tokenPath, []byte("another-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ForgeCommand(t.Context(), []string{"repository-metadata", "minos-e2e-owner", "subject-plans"}, &bytes.Buffer{}); err == nil {
		t.Fatalf("repository metadata succeeded with a token the forge does not know")
	}
}

func TestSnapshotCarriesTheClonesSourceAndTheAuthorsDeclarations(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["title"] = "Fixture change"
		pullRequest["body"] = "Not in scope: a concrete transport."
		pullRequest["head"].(map[string]any)["repo"] = map[string]any{"full_name": "forker/subject"}
	})
	cfg, _, facts := state.service(t)
	snapshot, err := currentSnapshot(t.Context(), cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Title != "Fixture change" || snapshot.Body != "Not in scope: a concrete transport." || snapshot.HeadRepository != "forker/subject" {
		t.Fatalf("snapshot title %q, body %q, head repository %q", snapshot.Title, snapshot.Body, snapshot.HeadRepository)
	}
}

func TestSnapshotCarriesSortedLabels(t *testing.T) {
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
			t.Setenv("MINOS_LEAD_MODEL", "claude-opus-5-5")

			leadModel := "claude-opus-5-5"
			body := apiShape.Request["body"].(string)
			attribution := "\n\nReviewed by: `" + leadModel + "`."
			record, err := product.FormatRecord(map[string]string{"head": head, "target": target})
			if err != nil {
				t.Fatal(err)
			}
			expectedBody := body + attribution + "\n\n" + record
			if test.preseed {
				review := mapsClone(apiShape.Review)
				review["commit_id"] = head
				review["body"] = expectedBody
				state.setReviewWithComments(review, []map[string]any{mapsClone(apiShape.Comments[0])})
			}

			attempts := 2
			if test.preseed {
				attempts = 1
			}
			for attempt := 0; attempt < attempts; attempt++ {
				if err := postFixtureReview(t, head, target, "comment", body, []requestedReviewComment{{
					Path: "internal/state.go", Line: 41, Body: "The transition accepts an invalid state.",
				}}); err != nil {
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
				if comment["body"] != "The transition accepts an invalid state." {
					t.Fatalf("review comment carries lead attribution the composer already supplies per finding: %#v", comment["body"])
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
}

// The default destination is resolved by the real configuration loader;
// review state comes only from writes through the compiled forge command.
func TestForgeReviewFilesCleanRoundToDefaultCommentDestination(t *testing.T) {
	state := newForgejoFixtureState(t)
	head, target := installAnchoredWorkspace(t, state, "internal/review.go", 7, 42)
	configureForgeCommandFixture(t, state)
	_, repo, _ := state.service(t)
	binary := filepath.Join(t.TempDir(), "minos")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/minos")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture command: %v\n%s", err, output)
	}
	t.Setenv("MINOS_BIN", binary)
	t.Setenv("MINOS_HEAD_SHA", head)
	t.Setenv("MINOS_TARGET_SHA", target)
	verdict := adjudicatedReviewPayload(t, []map[string]any{
		adjudicatedFinding("specialist-1:1", "Medium", "Advisory state transition", 42, "The transition is difficult to identify."),
		adjudicatedFinding("specialist-2:1", "Low", "Advisory recovery wording", 7, "The recovery path is difficult to identify."),
	})
	plan := postComposedReview(t, head, target, verdict)
	outcome := fileComposedTriage(t, state, plan, repo.FilingDestination)
	if outcome["outcome"] != "filed" || outcome["written"] != float64(2) {
		t.Fatalf("default comment delivery = %#v, want filed with two entries", outcome)
	}
	writes, payload := state.reviewWriteFacts()
	if writes != 1 || payload["event"] != "COMMENT" || payload["commit_id"] != head {
		t.Fatalf("default filing review writes = %d, payload = %#v", writes, payload)
	}
	body := payload["body"].(string)
	for _, title := range []string{"Advisory state transition", "Advisory recovery wording"} {
		if !strings.Contains(body, title) {
			t.Fatalf("filed body %q lacks %q", body, title)
		}
	}
	if strings.Count(body, "<!-- minos:") != 2 {
		t.Fatalf("filed body = %q, want two filing markers", body)
	}
	if comments, _ := payload["comments"].([]any); len(comments) != 0 {
		t.Fatalf("filing created inline comments: %#v", comments)
	}
	again := fileComposedTriage(t, state, plan, repo.FilingDestination)
	if again["outcome"] != "nothing-to-file" {
		t.Fatalf("repeat filing = %#v", again)
	}
	if writes, _ := state.reviewWriteFacts(); writes != 1 {
		t.Fatalf("repeat filing posted %d reviews", writes)
	}
	var snapshotOutput bytes.Buffer
	if err := ForgeCommand(t.Context(), []string{"snapshot"}, &snapshotOutput); err != nil {
		t.Fatal(err)
	}
	var snapshot forge.Snapshot
	if err := json.Unmarshal(snapshotOutput.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, review := range snapshot.Reviews {
		if _, terminal := terminalState(review); terminal {
			t.Fatalf("filing review became a terminal verdict: %#v", review)
		}
	}
}

func TestForgeReviewPublishesBlockingFindingsAndFilesTheRest(t *testing.T) {
	t.Run("a blocking round posts verified High and Low findings together", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		head, target := installAnchoredWorkspace(t, state, "internal/review.go", 7, 42)
		configureForgeCommandFixture(t, state)
		installFilingRepository(t, state)
		verdict := adjudicatedReviewPayload(t, []map[string]any{
			adjudicatedFinding("specialist-1:1", "High", "Gating state transition", 42, "The transition accepts an invalid state."),
			adjudicatedFinding("specialist-2:1", "Low", "Advisory recovery wording", 7, "The recovery path is difficult to identify."),
		})
		verdict["outOfScopeObservations"] = []map[string]any{{"title": "Unverified observation", "verified": false}}
		plan := postComposedReview(t, head, target, verdict, "specialist-1:1")
		writes, payload := state.reviewWriteFacts()
		if writes != 1 || payload["event"] != "REQUEST_CHANGES" || payload["commit_id"] != head {
			t.Fatalf("review writes = %d, payload = %#v", writes, payload)
		}
		posted := payload["comments"].([]any)
		if len(posted) != 2 {
			t.Fatalf("posted comments = %#v, want both verified findings", posted)
		}
		for i, want := range []string{"Blocking · High: Gating state transition", "Advisory · Low: Advisory recovery wording"} {
			if !strings.Contains(posted[i].(map[string]any)["body"].(string), want) {
				t.Fatalf("comment = %#v, want %q", posted[i], want)
			}
		}
		filingDestination := FilingDestination{Kind: FilingKindFile, Repository: "minos-e2e-owner/subject-plans", Path: "ISSUES.md"}
		if outcome := fileComposedTriage(t, state, plan, filingDestination); outcome["outcome"] != "nothing-to-file" {
			t.Fatalf("blocking round files findings: %#v", outcome)
		}
	})

	for _, withDestination := range []bool{true, false} {
		t.Run(fmt.Sprintf("clean round has no comments, filing destination configured=%t", withDestination), func(t *testing.T) {
			state := newForgejoFixtureState(t)
			head, target := installAnchoredWorkspace(t, state, "internal/review.go", 7, 42)
			configureForgeCommandFixture(t, state)
			plans := installFilingRepository(t, state)
			destination := FilingDestination{Kind: FilingKindFile, Repository: "minos-e2e-owner/subject-plans", Path: "ISSUES.md"}
			if !withDestination {
				destination = FilingDestination{Kind: FilingKindNone}
			}
			verdict := adjudicatedReviewPayload(t, []map[string]any{
				adjudicatedFinding("specialist-1:1", "Medium", "Advisory state transition", 42, "The transition is difficult to identify."),
				adjudicatedFinding("specialist-2:1", "Low", "Advisory recovery wording", 7, "The recovery path is difficult to identify."),
			})
			verdict["outOfScopeObservations"] = []map[string]any{{"title": "Unverified observation", "verified": false}}
			plan := postComposedReview(t, head, target, verdict)
			outcome := fileComposedTriage(t, state, plan, destination)
			if withDestination {
				if outcome["outcome"] != "filed" || outcome["written"] != float64(2) || outcome["location"] != "minos-e2e-owner/subject-plans:ISSUES.md" {
					t.Fatalf("triage outcome = %#v", outcome)
				}
				issues := plans.issues(t)
				if !strings.Contains(issues, "Advisory state transition") || !strings.Contains(issues, "Advisory recovery wording") || strings.Contains(issues, "Unverified observation") {
					t.Fatalf("filed content = %q", issues)
				}
			} else if outcome["outcome"] != "discarded" || outcome["review"] != nil {
				t.Fatalf("the none destination generated a fallback: %#v", outcome)
			}
			for _, args := range [][]string{{"marker", head, target, "clean", "add"}, {"status", head, target, "clean"}} {
				if err := ForgeCommand(t.Context(), args, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
			}
			if writes, _ := state.reviewWriteFacts(); writes != 0 {
				t.Fatalf("clean round posted %d reviews", writes)
			}
			if !slices.Contains(state.reactions, "+1") {
				t.Fatal("clean round has no thumbs-up")
			}
			cfg := writeSweepFixtureConfig(t, state)
			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name == "systemd-run" {
					t.Fatal("clean or dependency-deferred head was claimed")
				}
				return nil, nil
			}
			if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(state.reactions, "+1") {
				t.Fatal("sweep removed the current head's clean reaction")
			}
			state.changePullRequest(func(pr map[string]any) {
				pr["head"].(map[string]any)["sha"] = "next-head"
			})
			state.setStatuses(nil)
			state.setDependencies([]map[string]any{{"number": 7, "state": "open", "repository": map[string]any{"full_name": "minos-e2e-owner/prerequisite"}}})
			if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
				t.Fatal(err)
			}
			if slices.Contains(state.reactions, "+1") {
				t.Fatal("head movement retained the old clean reaction")
			}
		})
	}

	t.Run("unanchorable gating finding falls back into the review body", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		head, target := installAnchoredWorkspace(t, state, "internal/review.go", 10)
		installStrictAdaptation(t, state)
		configureForgeCommandFixture(t, state)

		verdict := adjudicatedReviewPayload(t, []map[string]any{
			adjudicatedFinding("specialist-1:1", "High", "Gating state transition", 1, "The transition accepts an invalid state."),
		})
		postComposedReview(t, head, target, verdict, "specialist-1:1")

		writes, payload := state.reviewWriteFacts()
		if writes != 1 || payload["event"] != "REQUEST_CHANGES" {
			t.Fatalf("review writes = %d, payload = %#v", writes, payload)
		}
		if posted := payload["comments"].([]any); len(posted) != 0 {
			t.Fatalf("inline comments = %#v, want body fallback", posted)
		}
		body := payload["body"].(string)
		for _, want := range []string{"Findings that could not be anchored inline:", "`internal/review.go` line 1", "Gating state transition"} {
			if !strings.Contains(body, want) {
				t.Fatalf("review body omitted %q: %q", want, body)
			}
		}
	})
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

// An adaptation that declares no out-of-hunk anchoring keeps the strict
// geometry, and the findings it cannot place are named in the review body
// rather than lost.
// The deployed Forgejo adaptation declares that it carries out-of-hunk
// comments, so the same finding the strict adaptation folds reaches the
// author on its own line.
func TestForgeReviewAnchorsOutOfHunkFindingsTheAdaptationDeclares(t *testing.T) {
	state := newForgejoFixtureState(t)
	head, target := installAnchoredWorkspace(t, state, "src/code.txt", 10)
	configureForgeCommandFixture(t, state)

	runReviewAttempts(t, head, target, "Review findings.", []requestedReviewComment{
		{Path: "src/code.txt", Line: 1, Body: "Outside-hunk concern."},
		{Path: "untouched.txt", Line: 1, Body: "Untouched-file concern."},
	}, 2)
	writes, payload := state.reviewWriteFacts()
	if writes != 1 {
		t.Fatalf("review writes = %d, want one", writes)
	}
	posted := payload["comments"].([]any)
	if len(posted) != 1 {
		t.Fatalf("inline comments = %#v", posted)
	}
	comment := posted[0].(map[string]any)
	if comment["path"] != "src/code.txt" || comment["new_position"] != float64(1) {
		t.Fatalf("out-of-hunk comment = %#v", comment)
	}
	body := payload["body"].(string)
	if !strings.Contains(body, "Untouched-file concern.") {
		t.Fatalf("a finding no capability can anchor left the review body: %q", body)
	}
	if strings.Contains(body, "Outside-hunk concern.") {
		t.Fatalf("an anchored finding was also folded into the body: %q", body)
	}
}

// A finding about code the change removed anchors on the deletion side: the
// guarded review consumer accepts old_position naming the removed line.
func TestForgeReviewAnchorsDeletionSideFindings(t *testing.T) {
	state := newForgejoFixtureState(t)
	head, target := installDeletingWorkspace(t, state, "src/code.txt", 10, 22)
	configureForgeCommandFixture(t, state)

	runReviewAttempts(t, head, target, "Review findings.", []requestedReviewComment{
		{Path: "src/code.txt", Line: 20, Body: "The removed guard was the only bounds check."},
	}, 2)
	writes, payload := state.reviewWriteFacts()
	if writes != 1 {
		t.Fatalf("review writes = %d, want one", writes)
	}
	posted := payload["comments"].([]any)
	if len(posted) != 1 {
		t.Fatalf("inline comments = %#v", posted)
	}
	comment := posted[0].(map[string]any)
	if comment["old_position"] != float64(20) || comment["new_position"] != float64(0) {
		t.Fatalf("deletion-side comment = %#v", comment)
	}
}

// A finding about several lines covers them all.
func TestForgeReviewAnchorsMultiLineFindings(t *testing.T) {
	state := newForgejoFixtureState(t)
	head, target := installAnchoredWorkspace(t, state, "src/code.txt", 10, 12)
	configureForgeCommandFixture(t, state)

	runReviewAttempts(t, head, target, "Review findings.", []requestedReviewComment{
		{Path: "src/code.txt", Line: 10, EndLine: 12, Body: "The three branches repeat one decision."},
	}, 2)
	writes, payload := state.reviewWriteFacts()
	if writes != 1 {
		t.Fatalf("review writes = %d, want one", writes)
	}
	comment := payload["comments"].([]any)[0].(map[string]any)
	if comment["new_position"] != float64(10) || comment["extra_lines_count"] != float64(2) {
		t.Fatalf("multi-line comment = %#v", comment)
	}
}

func TestForgeReviewFoldsOffDiffFindingsIntoTheBody(t *testing.T) {
	state := newForgejoFixtureState(t)
	head, target := installAnchoredWorkspace(t, state, "src/code.txt", 10)
	installStrictAdaptation(t, state)
	configureForgeCommandFixture(t, state)
	t.Setenv("MINOS_LEAD_MODEL", "claude-opus-5-5")
	comments := []requestedReviewComment{
		{Path: "src/code.txt", Line: 10, Body: "Anchored concern."},
		{Path: "src/code.txt", Line: 1, Body: "Outside-hunk concern."},
	}

	runReviewAttempts(t, head, target, "Review findings.", comments, 2)
	writes, payload := state.reviewWriteFacts()
	if writes != 1 {
		t.Fatalf("review writes = %d, want one", writes)
	}
	posted := payload["comments"].([]any)
	if len(posted) != 1 || posted[0].(map[string]any)["new_position"] != float64(10) {
		t.Fatalf("inline comments = %#v", posted)
	}
	body := payload["body"].(string)
	addendum := strings.Index(body, "Findings that could not be anchored inline:")
	attribution := strings.Index(body, "Reviewed by: `claude-opus-5-5`.")
	if addendum < 0 || attribution < 0 || attribution < addendum {
		t.Fatalf("review body order = %q", body)
	}
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

	runReviewAttempts(t, head, target, "Review findings.", []requestedReviewComment{{
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

	runReviewAttempts(t, head, target, "Review findings.", []requestedReviewComment{{
		Path: "src/code.txt", Line: 10, Body: "Durable concern.",
	}}, 2)
	if writes, _ := state.reviewWriteFacts(); writes != 1 {
		t.Fatalf("review writes = %d, want one", writes)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	stored := state.reviewComments[1][0]
	if stored["position"] != fixtureUnanchoredPosition || stored["diff_hunk"] != "" {
		t.Fatalf("stored unanchored comment = %#v", stored)
	}
}

// Exercise the Go command and shipped adaptation, then replay the same
// marked payload. The recorded POST is the state under assertion.
func TestForgejoJourneyFileIssue(t *testing.T) {
	for _, test := range []struct{ name, readBackLogin string }{
		{name: "confirmed service author"},
		{name: "read-back author differs", readBackLogin: "another-account"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			state.filedIssueReadBackLogin = test.readBackLogin
			configureForgeCommandFixture(t, state)
			directory := t.TempDir()
			titlePath := filepath.Join(directory, "title")
			bodyPath := filepath.Join(directory, "body")
			title := "Advisory concurrency failure"
			body := "The later writer overwrites the earlier update.\n<!-- minos:am91cm5leQ -->\n"
			if err := os.WriteFile(titlePath, []byte(title), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(bodyPath, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, wantWritten := range []float64{1, 0} {
				var stdout bytes.Buffer
				err := ForgeCommand(t.Context(), []string{"file-issue", "minos-e2e-owner/subject-plans", titlePath, bodyPath}, &stdout)
				var result map[string]any
				if decodeErr := json.Unmarshal(stdout.Bytes(), &result); decodeErr != nil {
					t.Fatalf("file-issue result is unreadable: %v; delivery error=%v", decodeErr, err)
				}
				if test.readBackLogin != "" {
					if err == nil || result["outcome"] != "uncertain" || !strings.Contains(fmt.Sprint(result["reason"]), "read-back does not match") {
						t.Fatalf("author mismatch must refuse confirmation: error=%v result=%v", err, result)
					}
					break
				}
				if err != nil || result["outcome"] != "applied" || result["written"] != wantWritten {
					t.Fatalf("file-issue result = %v, error=%v, want applied with written=%v", result, err, wantWritten)
				}
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if len(state.issueCreateRequests) != 1 || len(state.filedIssues) != 1 {
				t.Fatalf("issue requests=%v, stored issues=%v", state.issueCreateRequests, state.filedIssues)
			}
			payload := state.issueCreateRequests[0]
			if payload["title"] != title || payload["body"] != body || len(payload) != 2 {
				t.Fatalf("issue payload = %v", payload)
			}
		})
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
	t.Setenv("MINOS_MARKERS", defaultMarkersJSON)
}

// defaultMarkersJSON is MINOS_MARKERS as SpawnRun exports the shipped
// defaults.
const defaultMarkersJSON = `{"in-flight":{"reaction":"eyes"},"clean":{"reaction":"+1"}}`

func adjudicatedFinding(id, severity, title string, line int, explanation string) map[string]any {
	return map[string]any{
		"id": id, "source": "Correctness", "title": title, "severity": severity,
		"confidence": 86, "path": "internal/review.go", "line": line, "explanation": explanation,
		"proposingLabel": "specialist", "verifyLabel": "verifier",
		"rawVerifier": map[string]any{"verdict": "upheld", "confidence": 94, "reason": "The condition is reachable."},
	}
}

func adjudicatedReviewPayload(t *testing.T, findings []map[string]any) map[string]any {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	directory := t.TempDir()
	envelopePath := filepath.Join(directory, "envelope.json")
	recordDir := filepath.Join(directory, "record")
	archive := filepath.Join(recordDir, "runs", "cwd", "namespace", "run")
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "manifest.json"), []byte(`{"kind":"run_manifest","status":"complete"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for index, record := range []map[string]any{
		{"label": "specialist", "status": "complete", "resolved_model": "gpt-6-sol-served"},
		{"label": "verifier", "status": "complete", "resolved_model": "claude-opus-5-5"},
	} {
		directory := filepath.Join(archive, "agents", strconv.Itoa(index+1))
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "agent.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	envelope := map[string]any{
		"reviewed": map[string]any{"target": "target-sha", "head": "head-sha", "occasion": nil}, "stage": "present",
		"requiredModelEvidence": []any{
			map[string]any{"label": "specialist", "role": "specialist", "pinnedModel": "gpt-6-sol"},
			map[string]any{"label": "verifier", "role": "verifier", "pinnedModel": "claude-opus-5-5"},
		}, "proposedFindings": findings,
		"briefs": []any{}, "misconfigurations": []any{}, "dispatches": []any{}, "reviewers": []any{},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envelopePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	command := `import { readFile } from "node:fs/promises"; import { adjudicate } from "./workflows/run-record-adjudicator.mjs"; const [envelopePath, recordDir] = process.argv.slice(1); const envelope = JSON.parse(await readFile(envelopePath, "utf8")); process.stdout.write(JSON.stringify(await adjudicate({ envelope, recordDir })));`
	cmd := exec.CommandContext(t.Context(), "node", "--input-type=module", "--eval", command, envelopePath, recordDir)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("adjudicate review payload: %v\n%s", err, output)
	}
	var verdict map[string]any
	if err := json.Unmarshal(output, &verdict); err != nil {
		t.Fatalf("decode adjudicated payload: %v\n%s", err, output)
	}
	if verdict["status"] != "complete" {
		t.Fatalf("adjudicated verdict = %#v", verdict)
	}
	return verdict
}

// postComposedReview runs the publication composer over an adjudicated
// verdict and the lead's decision for it — the same seam the lifecycle's
// publish step drives — and posts each planned review through the guarded
// command in the plan's order. The decision gates exactly the finding ids
// in gating; every other confirmed finding is advisory. The returned plan
// names the triage entries the composer set aside.
func postComposedReview(t *testing.T, head, target string, verdict map[string]any, gating ...string) map[string]any {
	t.Helper()
	directory := t.TempDir()
	verdictPath := filepath.Join(directory, "verdict.json")
	decisionPath := filepath.Join(directory, "decision.json")
	encodedVerdict, err := json.Marshal(verdict)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(verdictPath, encodedVerdict, 0o600); err != nil {
		t.Fatal(err)
	}
	var dispositions []map[string]any
	for _, finding := range verdict["confirmedFindings"].([]any) {
		id := finding.(map[string]any)["id"].(string)
		dispositions = append(dispositions, map[string]any{"key": id, "gating": slices.Contains(gating, id)})
	}
	decisionVerdict := "clean"
	if len(gating) > 0 {
		decisionVerdict = "request-changes"
	}
	decision := map[string]any{
		"kind": "minos-verdict-decision-v1", "verdict": decisionVerdict,
		"basis": "fixture decision", "findings": dispositions,
	}
	if dispositions == nil {
		decision["findings"] = []any{}
	}
	encodedDecision, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decisionPath, encodedDecision, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	orientationPath := filepath.Join(directory, "orientation.json")
	if err := os.WriteFile(orientationPath, []byte(`{"misconfigurations":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	composer := exec.CommandContext(t.Context(), "node", filepath.Join(root, "workflows", "compose-review-publication.mjs"),
		filepath.Join(directory, "publication"), orientationPath, "High", verdictPath, decisionPath)
	output, err := composer.CombinedOutput()
	if err != nil {
		t.Fatalf("compose review publication: %v\n%s", err, output)
	}
	var plan map[string]any
	if err := json.Unmarshal(output, &plan); err != nil {
		t.Fatalf("decode publication plan: %v\n%s", err, output)
	}
	for _, entry := range plan["posts"].([]any) {
		post := entry.(map[string]any)
		if err := ForgeCommand(t.Context(), []string{"review", head, target, post["verdict"].(string), post["body"].(string), post["comments"].(string)}, &bytes.Buffer{}); err != nil {
			t.Fatalf("post %s review: %v", post["review"], err)
		}
	}
	return plan
}

// filingRepository is a secondary repository configured as a file-kind
// filing destination: a bare origin the fixture forge names as
// minos-e2e-owner/subject-plans, seeded with an issue log.
type filingRepository struct {
	origin string
	seed   string
}

func installFilingRepository(t *testing.T, state *forgejoFixtureState) filingRepository {
	t.Helper()
	scratch := t.TempDir()
	origin := filepath.Join(scratch, "origin.git")
	seed := filepath.Join(scratch, "seed")
	runGit(t, scratch, "init", "--quiet", "--bare", "--initial-branch=main", origin)
	runGit(t, scratch, "init", "--quiet", "--initial-branch=main", seed)
	runGit(t, seed, "config", "user.name", "Seed")
	runGit(t, seed, "config", "user.email", "seed@example.invalid")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# Plans\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "ISSUES.md"), []byte("# Issues\n\n- An existing entry.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", ".")
	runGit(t, seed, "commit", "--quiet", "-m", "seed")
	runGit(t, seed, "push", "--quiet", origin, "main")
	state.setSecondaryCloneURL(origin)
	return filingRepository{origin: origin, seed: seed}
}

func (r filingRepository) issues(t *testing.T) string {
	t.Helper()
	return gitOutput(t, r.seed, "--git-dir", r.origin, "show", "main:ISSUES.md")
}

// fileComposedTriage delivers the plan's triage entries the way the
// lifecycle's publish step does — the destination and identity in the
// environment the service exported — and returns the filing's outcome.
func fileComposedTriage(t *testing.T, state *forgejoFixtureState, plan map[string]any, destination FilingDestination) map[string]any {
	t.Helper()
	directory := t.TempDir()
	orientationPath := filepath.Join(directory, "orientation.json")
	orientation := map[string]any{
		"repository": "/workspace", "guidance": []any{},
		"source": map[string]any{"owner": "minos-e2e-owner", "repo": "subject", "pr": "17", "date": "2026-09-12"},
	}
	encoded, err := json.Marshal(orientation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orientationPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	destinationJSON, err := json.Marshal(destination)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// The filing reaches the forge through the run's minos binary and the
	// service configuration it reads, as the run body exports them.
	binary := buildMinos(t)
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	entries := plan["triage"].(map[string]any)["entries"].(string)
	filing := exec.CommandContext(t.Context(), "node", filepath.Join(root, "workflows", "file-triage.mjs"), entries, orientationPath)
	filing.Env = append(os.Environ(),
		"MINOS_FILING_DESTINATION="+string(destinationJSON),
		"MINOS_BIN="+binary,
		"MINOS_CONFIG="+cfg.Root,
		"MINOS_FORGE=forgejo",
		"MINOS_OWNER=minos-e2e-owner",
		"MINOS_REPO_NAME=subject",
		"MINOS_RUN_DIR="+directory,
		"MINOS_COMMIT_AUTHOR_NAME=Minos",
		"MINOS_COMMIT_AUTHOR_EMAIL=minos@example.invalid",
		"MINOS_HEAD_BRANCH=feature",
		"MINOS_SETUP_WORKSPACE="+filepath.Join(root, "scripts", "run-body", "setup-workspace"),
	)
	output, err := filing.CombinedOutput()
	if err != nil {
		t.Fatalf("file triage: %v\n%s", err, output)
	}
	var outcome map[string]any
	if err := json.Unmarshal(output, &outcome); err != nil {
		t.Fatalf("decode triage outcome: %v\n%s", err, output)
	}
	return outcome
}

// postFixtureReview writes a fixture review payload and posts it through the
// same command surface the journey paths exercise.
func postFixtureReview(t *testing.T, head, target, event, body string, comments any) error {
	t.Helper()
	directory := t.TempDir()
	bodyPath := filepath.Join(directory, "body.md")
	commentsPath := filepath.Join(directory, "comments.json")
	if err := os.WriteFile(bodyPath, []byte(body+"\n"), 0o600); err != nil {
		return err
	}
	encoded, err := json.Marshal(comments)
	if err != nil {
		return err
	}
	if err := os.WriteFile(commentsPath, encoded, 0o600); err != nil {
		return err
	}
	if err := ForgeCommand(t.Context(), []string{"review", head, target, event, bodyPath, commentsPath}, &bytes.Buffer{}); err != nil {
		return err
	}
	return nil
}

func runReviewAttempts(
	t *testing.T,
	head, target, body string,
	comments []requestedReviewComment,
	attempts int,
) {
	t.Helper()
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := postFixtureReview(t, head, target, "comment", body, comments); err != nil {
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

// installStrictAdaptation serves the run from an adaptation that declares no
// anchoring capability, the shape of a forge that only accepts comments
// inside a diff hunk.
func installStrictAdaptation(t *testing.T, state *forgejoFixtureState) {
	t.Helper()
	strict := t.TempDir()
	entries, err := os.ReadDir(state.adaptationPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "capabilities.json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		contents, err := os.ReadFile(filepath.Join(state.adaptationPath, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(strict, entry.Name()), contents, info.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
	state.adaptationPath = strict
}

// installDeletingWorkspace stages a change that removes a block of lines, so a
// finding about the removed code has no new-side line to anchor on.
func installDeletingWorkspace(t *testing.T, state *forgejoFixtureState, path string, from, to int) (head, target string) {
	t.Helper()
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-q")
	runGit(t, workspace, "config", "user.name", "Minos Test")
	runGit(t, workspace, "config", "user.email", "minos@example.invalid")

	lines := make([]string, to+8)
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

	remaining := append(append([]string{}, lines[:from-1]...), lines[to:]...)
	if err := os.WriteFile(fullPath, []byte(strings.Join(remaining, "\n")+"\n"), 0o600); err != nil {
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
	issueComments    []map[string]any
	dependencies     []map[string]any
	dependencyPages  [][]map[string]any
	dependencyCode   int
	server           *httptest.Server
	tokenPath        string
	adaptationPath   string

	mu                       sync.Mutex
	requestedReviewers       []string
	reviewRequestsUnrecorded bool
	reactions                []string
	reactionPageSize         int
	reviewRequestWrites      int
	obsoleteAssignmentWrites int
	reactionWrites           int
	reactionDeleteWrites     int
	labelWrites              int
	repositoryLabels         []map[string]any
	organisationLabelPages   [][]map[string]any
	stackedChildren          []map[string]any
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
	forgeAccounts            map[string]map[string]any
	filedIssueReadBackLogin  string
	filedIssues              []map[string]any
	issueCreateRequests      []map[string]any
	secondaryCloneURL        string
}

type statusPostRequest struct {
	Head    string
	Payload map[string]any
}

// Unanchored comments carry a coordinate outside the fixture diff intervals.
const fixtureUnanchoredPosition int64 = 3691

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
		pullRequestReads: make(map[string]int),
		forgeAccounts:    map[string]map[string]any{"fixture-token": {"login": "Minos"}},
		dependencies:     []map[string]any{}, dependencyCode: http.StatusOK,
		statusesByCommit: make(map[string][]map[string]any),
	}
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
	setTestRunCeilings(&cfg)
	cfg.Service.BotLogin = "Minos"
	cfg.Service.StatusContext = "Minos"
	cfg.Listener.Bind = ":0"
	cfg.Runs.Dir = t.TempDir()
	webhookSecret := filepath.Join(t.TempDir(), "webhook.secret")
	if err := os.WriteFile(webhookSecret, []byte("fixture-webhook-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Forges = map[string]ForgeConfig{
		"forgejo": {
			Adaptation: s.adaptationPath, APIBase: s.server.URL,
			WebhookSecretFile: webhookSecret, CredentialFile: s.tokenPath,
		},
	}
	repo := RepoConfig{Forge: "forgejo", Owner: "minos-e2e-owner", Repo: "subject"}
	repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"
	repo.RepositoryKnobs = resolveRepositoryKnobs(RepositoryKnobs{}, RepositoryKnobs{}, func(...string) bool { return false })
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

// branchHead resolves the target branch's current forge coordinate while the
// fixture mutex is held by handle.
func (s *forgejoFixtureState) branchHead(path string) string {
	branch := strings.TrimPrefix(path, "/api/v1/repos/minos-e2e-owner/subject/branches/")
	if branch == s.pullRequest["base"].(map[string]any)["ref"] {
		return s.pullRequest["base"].(map[string]any)["sha"].(string)
	}
	for _, child := range s.stackedChildren {
		head := child["head"].(map[string]any)
		if branch == head["ref"] {
			return head["sha"].(string)
		}
	}
	return ""
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

// setSecondaryCloneURL publishes minos-e2e-owner/subject-plans, the
// secondary repository a configured guidance source may name.
// setRepositoryCloneURL is the reviewed repository's clone URL as its
// repository lookup reports it.
func (s *forgejoFixtureState) setRepositoryCloneURL(cloneURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repository["clone_url"] = cloneURL
}

func (s *forgejoFixtureState) setSecondaryCloneURL(cloneURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secondaryCloneURL = cloneURL
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
	account := s.forgeAccounts[strings.TrimPrefix(r.Header.Get("Authorization"), "token ")]
	pullPath := fmt.Sprintf("/api/v1/repos/minos-e2e-owner/subject/pulls/%v", s.pullRequest["number"])
	issuePath := fmt.Sprintf("/api/v1/repos/minos-e2e-owner/subject/issues/%v", s.pullRequest["number"])
	switch {
	case path == "/api/v1/repos/minos-e2e-owner/subject-plans/issues" && r.Method == http.MethodGet:
		if account == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		issues := []map[string]any{}
		if page == 1 {
			filter := r.URL.Query().Get("state")
			if filter == "" {
				filter = "open"
			}
			for _, stored := range s.filedIssues {
				if filter != "all" && stored["state"] != filter {
					continue
				}
				issue := mapsClone(stored)
				if s.filedIssueReadBackLogin != "" {
					issue["user"] = map[string]any{"login": s.filedIssueReadBackLogin}
				}
				issues = append(issues, issue)
			}
		}
		writeFixtureArray(s.t, w, issues)
	case path == "/api/v1/repos/minos-e2e-owner/subject-plans/issues" && r.Method == http.MethodPost:
		if account == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
			w.WriteHeader(400)
			return
		}
		s.issueCreateRequests = append(s.issueCreateRequests, mapsClone(payload))
		issue := mapsClone(payload)
		issue["number"] = len(s.filedIssues) + 1
		issue["state"] = "open"
		issue["user"] = mapsClone(account)
		s.filedIssues = append(s.filedIssues, issue)
		w.WriteHeader(http.StatusCreated)
		writeFixtureJSON(s.t, w, issue)
	case r.Method == http.MethodGet && path == "/api/v1/user":
		if account == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeFixtureJSON(s.t, w, account)
	case r.Method == http.MethodGet && path == "/api/v1/repos/minos-e2e-owner/subject":
		writeFixtureJSON(s.t, w, s.repository)
	case r.Method == http.MethodGet && path == "/api/v1/repos/minos-e2e-owner/subject-plans":
		if account == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if s.secondaryCloneURL == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeFixtureJSON(s.t, w, map[string]any{"clone_url": s.secondaryCloneURL, "default_branch": "main"})
	case r.Method == http.MethodGet && path == "/api/v1/repos/minos-e2e-owner/subject/pulls":
		writeFixtureJSON(s.t, w, append([]map[string]any{s.pullRequest}, s.stackedChildren...))
	case r.Method == http.MethodGet && path == pullPath:
		s.pullRequestReads[fmt.Sprint(s.pullRequest["number"])]++
		pullRequest := maps.Clone(s.pullRequest)
		requestedReviewers := make([]map[string]any, 0, len(s.requestedReviewers))
		for _, login := range s.requestedReviewers {
			requestedReviewers = append(requestedReviewers, map[string]any{"login": login})
		}
		pullRequest["requested_reviewers"] = requestedReviewers
		writeFixtureJSON(s.t, w, pullRequest)
	case r.Method == http.MethodPost && path == pullPath+"/requested_reviewers":
		var payload struct {
			Reviewers     []string `json:"reviewers"`
			TeamReviewers []string `json:"team_reviewers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
		}
		reviews := make([]map[string]any, 0, len(payload.Reviewers))
		for _, login := range payload.Reviewers {
			if !s.reviewRequestsUnrecorded && !slices.Contains(s.requestedReviewers, login) {
				s.requestedReviewers = append(s.requestedReviewers, login)
			}
			reviews = append(reviews, map[string]any{"state": "REQUEST_REVIEW", "user": map[string]any{"login": login}})
		}
		s.reviewRequestWrites++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(reviews); err != nil {
			s.t.Error(err)
		}
	case r.Method == http.MethodGet && s.stackedChild(path) != nil:
		s.pullRequestReads[fmt.Sprint(s.stackedChild(path)["number"])]++
		writeFixtureJSON(s.t, w, s.stackedChild(path))
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/dependencies") && s.stackedChildForPath(path) != nil:
		child := s.stackedChildForPath(path)
		dependencies, _ := child["dependencies"].([]map[string]any)
		if dependencies == nil {
			dependencies = []map[string]any{}
		}
		writeFixtureArray(s.t, w, dependencies)
	case r.Method == http.MethodGet && path != issuePath+"/dependencies" && strings.Contains(path, "/issues/") && strings.HasSuffix(path, "/dependencies"):
		writeFixtureJSON(s.t, w, []map[string]any{})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/reviews") && s.stackedChildForPath(path) != nil:
		child := s.stackedChildForPath(path)
		reviews, _ := child["reviews"].([]map[string]any)
		if reviews == nil {
			reviews = []map[string]any{}
		}
		writeFixtureArray(s.t, w, reviews)
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
			writeFixtureArray(s.t, w, s.dependencyPages[page-1])
			return
		}
		writeFixtureArray(s.t, w, s.dependencies)
	case r.Method == http.MethodGet && path == issuePath+"/comments":
		writeFixtureArray(s.t, w, s.issueComments)
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
	case r.Method == http.MethodGet && strings.Contains(path, "/branches/") && s.branchHead(path) != "":
		writeFixtureJSON(s.t, w, map[string]any{
			"commit": map[string]any{"id": s.branchHead(path)},
		})
	case r.Method == http.MethodGet && strings.Contains(path, "/branches/"):
		if strings.Contains(path, "refs/pull/") {
			s.virtualRefLookups++
		}
		writeFixtureJSON(s.t, w, map[string]any{})
		if s.pullRequestReads[fmt.Sprint(s.pullRequest["number"])] > 0 {
			s.runPriorityBoundaryMutation()
		}
	case r.Method == http.MethodGet && strings.Contains(path, "/commits/") && strings.HasSuffix(path, "/statuses"):
		commit := strings.TrimSuffix(strings.SplitN(path, "/commits/", 2)[1], "/statuses")
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
		writeFixtureArray(s.t, w, statuses)
		if s.pullRequestReads[fmt.Sprint(s.pullRequest["number"])] == 0 {
			s.runPriorityBoundaryMutation()
		}
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
		writeFixtureArray(s.t, w, s.reviews)
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
					comment["position"] = fixtureUnanchoredPosition
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
		writeFixtureArray(s.t, w, s.reviewComments[id])
	case r.Method == http.MethodPost && (path == issuePath+"/assignees" || s.stackedChildForPath(path) != nil && strings.HasSuffix(path, "/assignees")):
		s.obsoleteAssignmentWrites++
		http.Error(w, "route not found", http.StatusNotFound)
	case r.Method == http.MethodPatch && (path == issuePath || s.stackedChildForPath(path) != nil && strings.HasSuffix(path, "/issues/"+fmt.Sprint(s.stackedChildForPath(path)["number"]))):
		s.obsoleteAssignmentWrites++
		http.Error(w, "route not found", http.StatusNotFound)
	case r.Method == http.MethodGet && (path == issuePath+"/reactions" || s.stackedChildForPath(path) != nil && strings.HasSuffix(path, "/reactions")):
		var reactions []map[string]any
		for _, content := range s.reactions {
			reactions = append(reactions, map[string]any{"content": content, "user": map[string]any{"login": "Minos"}})
		}
		if s.reactionPageSize > 0 {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page < 1 {
				page = 1
			}
			start := min((page-1)*s.reactionPageSize, len(reactions))
			end := min(start+s.reactionPageSize, len(reactions))
			reactions = reactions[start:end]
			if len(reactions) == 0 {
				reactions = nil
			}
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
	case r.Method == http.MethodGet && path == "/api/v1/repos/minos-e2e-owner/subject/labels":
		// Forgejo pages repository labels; the fixture serves them all on
		// the first page and an empty page after.
		if page := r.URL.Query().Get("page"); page != "" && page != "1" {
			writeFixtureJSON(s.t, w, []map[string]any{})
			return
		}
		writeFixtureArray(s.t, w, s.repositoryLabels)
	case r.Method == http.MethodGet && path == "/api/v1/orgs/minos-e2e-owner/labels":
		// Unless a test makes it an organisation, the fixture owner is a
		// user, which Forgejo answers as no organisation.
		if s.organisationLabelPages == nil {
			http.Error(w, `{"message":"GetOrgByName"}`, http.StatusNotFound)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 || page > len(s.organisationLabelPages) {
			writeFixtureJSON(s.t, w, []map[string]any{})
			return
		}
		writeFixtureArray(s.t, w, s.organisationLabelPages[page-1])
	case r.Method == http.MethodGet && path == issuePath+"/labels":
		writeFixtureArray(s.t, w, s.pullRequestLabels())
	case r.Method == http.MethodPost && path == issuePath+"/labels":
		var payload struct {
			Labels []int64 `json:"labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
		}
		labels := s.pullRequestLabels()
		for _, id := range payload.Labels {
			for _, defined := range slices.Concat(append([][]map[string]any{s.repositoryLabels}, s.organisationLabelPages...)...) {
				if fixtureLabelID(defined) == id && !slices.ContainsFunc(labels, func(label map[string]any) bool { return fixtureLabelID(label) == id }) {
					labels = append(labels, defined)
					s.writeSequence = append(s.writeSequence, "label-add:"+fmt.Sprint(defined["name"]))
				}
			}
		}
		s.setPullRequestLabels(labels)
		s.labelWrites++
		writeFixtureArray(s.t, w, labels)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, issuePath+"/labels/"):
		id, err := strconv.ParseInt(strings.TrimPrefix(path, issuePath+"/labels/"), 10, 64)
		if err != nil {
			http.Error(w, "label id must be numeric", http.StatusUnprocessableEntity)
			return
		}
		kept := make([]map[string]any, 0)
		for _, label := range s.pullRequestLabels() {
			if fixtureLabelID(label) == id {
				s.writeSequence = append(s.writeSequence, "label-remove:"+fmt.Sprint(label["name"]))
				continue
			}
			kept = append(kept, label)
		}
		s.setPullRequestLabels(kept)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, fmt.Sprintf("unexpected fixture request %s %s", r.Method, path), http.StatusNotFound)
	}
}

// pullRequestLabels reads the labels the pull request carries, in the shape
// both the pull request and the issue labels endpoint return. The caller
// holds the fixture lock.
func (s *forgejoFixtureState) pullRequestLabels() []map[string]any {
	labels, err := decodeFixtureLabels(s.pullRequest["labels"])
	if err != nil {
		s.t.Error(err)
		return nil
	}
	return labels
}

func decodeFixtureLabels(value any) ([]map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode fixture labels: %w", err)
	}
	var labels []map[string]any
	if err := json.Unmarshal(raw, &labels); err != nil {
		return nil, fmt.Errorf("decode fixture labels: %w", err)
	}
	if labels == nil {
		labels = []map[string]any{}
	}
	return labels, nil
}

func TestFixtureLabelsRejectsConversionErrors(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		want  string
	}{
		{name: "encoding", value: make(chan int), want: "encode fixture labels:"},
		{name: "decoding", value: "not-a-list", want: "decode fixture labels:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			labels, err := decodeFixtureLabels(test.value)
			if err == nil || !strings.Contains(err.Error(), test.want) || labels != nil {
				t.Fatalf("fixture labels = %v, error = %v; want nil labels and %q error", labels, err, test.want)
			}
		})
	}
}

func (s *forgejoFixtureState) setPullRequestLabels(labels []map[string]any) {
	stored := make([]any, 0, len(labels))
	for _, label := range labels {
		stored = append(stored, label)
	}
	s.pullRequest["labels"] = stored
}

// pullRequestLabelNames lists the carried labels by name for assertions.
func (s *forgejoFixtureState) pullRequestLabelNames() []string {
	names := []string{}
	for _, label := range s.pullRequestLabels() {
		names = append(names, fmt.Sprint(label["name"]))
	}
	return names
}

func fixtureLabelID(label map[string]any) int64 {
	switch id := label["id"].(type) {
	case float64:
		return int64(id)
	case int64:
		return id
	case int:
		return int64(id)
	}
	return -1
}

func writeFixtureJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

// Non-reaction collection endpoints return []; reactions use the plain JSON
// writer so their empty inventory and terminal page retain Forgejo's null.
func writeFixtureArray(t *testing.T, w http.ResponseWriter, values []map[string]any) {
	t.Helper()
	if values == nil {
		values = []map[string]any{}
	}
	writeFixtureJSON(t, w, values)
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
web-base = %q
webhook-secret-file = %q
credential-file = %q
[runs]
dir = %q
`, cfg.Service.BotLogin, cfg.Forges["forgejo"].Adaptation, cfg.Forges["forgejo"].APIBase, cfg.Forges["forgejo"].WebBase,
		cfg.Forges["forgejo"].WebhookSecretFile, cfg.Forges["forgejo"].CredentialFile, cfg.Runs.Dir)
	if cfg.Service.StatusContext != "" {
		body = strings.Replace(body, "[listener]\n", fmt.Sprintf("status-context = %q\n[listener]\n", cfg.Service.StatusContext), 1)
	}
	if err := os.WriteFile(filepath.Join(cfg.Root, "service.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestForgeStatusRejectsDescriptionOverrides(t *testing.T) {
	for _, extra := range [][]string{{"operator custom description"}, {"--setup-failure", "workspace-setup"}} {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			state := newForgejoFixtureState(t)
			cfg, _, facts := state.service(t)
			writeServiceConfig(t, cfg)
			t.Setenv("MINOS_CONFIG", cfg.Root)
			t.Setenv("MINOS_FORGE", facts.Forge)
			t.Setenv("MINOS_OWNER", facts.Owner)
			t.Setenv("MINOS_REPO_NAME", facts.Repo)
			t.Setenv("MINOS_PR", facts.PR)
			var stdout strings.Builder
			args := append([]string{"status", state.headSHA(), state.targetSHA(), "incomplete"}, extra...)
			err := ForgeCommand(t.Context(), args, &stdout)
			if err == nil || !strings.Contains(err.Error(), "usage:") || len(state.statusPostFacts()) != 0 {
				t.Fatalf("description override: err=%v result=%s posts=%#v, want usage refusal without writes", err, stdout.String(), state.statusPostFacts())
			}
		})
	}
}

func TestForgeStatusRejectsFreeFormDescription(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, facts := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)
	t.Setenv("MINOS_PR", facts.PR)
	var stdout strings.Builder
	err := ForgeCommand(t.Context(), []string{"status", state.headSHA(), state.targetSHA(), "incomplete", "operator custom description"}, &stdout)
	if err == nil || !strings.Contains(err.Error(), "usage:") || len(state.statusPostFacts()) != 0 {
		t.Fatalf("free-form description: err=%v result=%s posts=%#v, want usage refusal without writes", err, stdout.String(), state.statusPostFacts())
	}
}

func TestForgeStatusDetailsUsesForgeWebBase(t *testing.T) {
	for _, test := range []struct{ name, webBase string }{
		{name: "configured", webBase: "https://forge.example/forge/"},
		{name: "unset"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			cfg, _, facts := state.service(t)
			forge := cfg.Forges[facts.Forge]
			forge.WebBase = test.webBase
			cfg.Forges[facts.Forge] = forge
			writeServiceConfig(t, cfg)
			t.Setenv("MINOS_CONFIG", cfg.Root)
			t.Setenv("MINOS_FORGE", facts.Forge)
			t.Setenv("MINOS_OWNER", facts.Owner)
			t.Setenv("MINOS_REPO_NAME", facts.Repo)
			t.Setenv("MINOS_PR", facts.PR)
			// Configuration must win over an inherited run's web host, even unset.
			t.Setenv("MINOS_WEB_BASE", "https://ambient.example")
			var stdout strings.Builder
			args := []string{"status", state.headSHA(), state.targetSHA(), "working"}
			if err := ForgeCommand(t.Context(), args, &stdout); err != nil {
				t.Fatalf("status write: %v\n%s", err, stdout.String())
			}
			posts := state.statusPostFacts()
			if len(posts) != 1 {
				t.Fatalf("status posts = %d, want 1", len(posts))
			}
			wantBase := state.server.URL
			if test.webBase != "" {
				wantBase = "https://forge.example/forge"
			}
			want := wantBase + "/minos-e2e-owner/subject/pulls/1#minos-target-" + state.targetSHA()
			got := posts[0].Payload["target_url"]
			t.Logf("recorded target_url = %v", got)
			if got != want {
				t.Fatalf("target_url = %v, want %s", got, want)
			}
			if !strings.Contains(stdout.String(), `"outcome":"applied"`) {
				t.Fatalf("status read-back = %s, want applied", stdout.String())
			}
			stdout.Reset()
			if err := ForgeCommand(t.Context(), args, &stdout); err != nil {
				t.Fatalf("repeated status: %v\n%s", err, stdout.String())
			}
			if posts := state.statusPostFacts(); len(posts) != 1 {
				t.Fatalf("repeated status posts = %d, want 1", len(posts))
			}
		})
	}
}
