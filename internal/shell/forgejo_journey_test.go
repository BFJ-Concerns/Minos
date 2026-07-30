package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"bfj/minos/internal/product"
)

func TestForgejoAdmissionUsesFreshPullRequestSnapshot(t *testing.T) {
	t.Run("draft pull request is not started", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.changePullRequest(func(pullRequest map[string]any) { pullRequest["draft"] = true })
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
		if result != "nothing" {
			t.Fatalf("result = %q, want nothing", result)
		}
	})

	t.Run("Minos review with a missing terminal status is repaired without a new run", func(t *testing.T) {
		state := newForgejoFixtureState(t)
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
		if result != "recovered" {
			t.Fatalf("result = %q, want recovered", result)
		}
		result, err = reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result != "nothing" {
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
		if result != "started" || !started {
			t.Fatalf("result = %q, started = %t; want an actual start", result, started)
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
		if result != "started" || !started {
			t.Fatalf("result = %q, started = %t; want a fresh attempt", result, started)
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
		if result != "started" {
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
		if result != "suppressed" {
			t.Fatalf("result = %q, want suppressed", result)
		}
		if !slices.Equal(commands, []string{"systemctl"}) {
			t.Fatalf("commands = %v, want only active-unit check", commands)
		}
	})

	t.Run("a second pull request waits for the active lead to exit", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, firstFacts := state.service(t)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		var activeUnit string
		var startedUnits []string
		commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "systemctl":
				if activeUnit == "" {
					return nil, nil
				}
				return []byte(activeUnit + " loaded active running Minos lead\n"), nil
			case "systemd-run":
				unitFlag := slices.Index(args, "--unit")
				if unitFlag < 0 || unitFlag+1 >= len(args) {
					t.Fatalf("systemd-run arguments omit unit: %v", args)
				}
				activeUnit = args[unitFlag+1]
				startedUnits = append(startedUnits, activeUnit)
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
		if result != "started" {
			t.Fatalf("first result = %q, want started", result)
		}

		state.changePullRequest(func(pullRequest map[string]any) { pullRequest["number"] = float64(2) })
		_, _, secondFacts := state.service(t)
		secondFacts.HeadSHA = ""
		result, err = reconcilePullRequest(t.Context(), cfg, repo, secondFacts)
		if err != nil {
			t.Fatal(err)
		}
		if result != "suppressed" {
			t.Fatalf("second result while first active = %q, want suppressed", result)
		}
		if len(startedUnits) != 1 {
			t.Fatalf("started units while first active = %v, want one", startedUnits)
		}

		activeUnit = ""
		result, err = reconcilePullRequest(t.Context(), cfg, repo, secondFacts)
		if err != nil {
			t.Fatal(err)
		}
		if result != "started" {
			t.Fatalf("second result after first exit = %q, want started", result)
		}
		if len(startedUnits) != 2 || !strings.Contains(startedUnits[1], "pr2") {
			t.Fatalf("started units = %v, want second pull request unit", startedUnits)
		}
	})
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
	if state.mergeWrites != 1 || state.sourceBranchExists || state.branchDeleteWrites != 1 {
		t.Fatalf("merge writes = %d, source exists = %t, branch deletes = %d", state.mergeWrites, state.sourceBranchExists, state.branchDeleteWrites)
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
	t              *testing.T
	pullRequest    map[string]any
	repository     map[string]any
	reviews        []map[string]any
	statuses       []map[string]any
	actionRuns     []map[string]any
	actionJobs     map[int64][]map[string]any
	actionLogs     map[int64]string
	server         *httptest.Server
	tokenPath      string
	adaptationPath string

	mu                       sync.Mutex
	assignees                []string
	reactions                []string
	assignmentWrites         int
	obsoleteAssignmentWrites int
	reactionWrites           int
	reactionDeleteWrites     int
	labelDeleteWrites        int
	mergeWrites              int
	branchDeleteWrites       int
	sourceBranchExists       bool
	statusWrites             int
	statusPostRequests       []statusPostRequest
	reviewWrites             int
	reviewPayloads           []map[string]any
	reviewComments           map[int64][]map[string]any
	diffNewSide              map[string][][2]int64
	positionRewrites         map[string]map[int64]int64
	statusReadCommits        []string
	virtualRefLookups        int
	annexeCloneURL           string
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
		sourceBranchExists: true,
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
	case r.Method == http.MethodGet && path == pullPath:
		writeFixtureJSON(s.t, w, s.pullRequest)
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
		writeFixtureJSON(s.t, w, map[string]any{})
	case r.Method == http.MethodGet && strings.Contains(path, "/commits/") && strings.HasSuffix(path, "/statuses"):
		commit := strings.TrimSuffix(strings.SplitN(path, "/commits/", 2)[1], "/statuses")
		s.statusReadCommits = append(s.statusReadCommits, commit)
		writeFixtureJSON(s.t, w, s.statuses)
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
		s.statuses = append([]map[string]any{payload}, s.statuses...)
		s.statusWrites++
		head := strings.TrimPrefix(path, "/api/v1/repos/minos-e2e-owner/subject/statuses/")
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
	case r.Method == http.MethodGet && path == issuePath:
		assignees := make([]map[string]any, 0, len(s.assignees))
		for _, login := range s.assignees {
			assignees = append(assignees, map[string]any{"login": login})
		}
		writeFixtureJSON(s.t, w, map[string]any{"assignees": assignees})
	case r.Method == http.MethodPost && path == issuePath+"/assignees":
		s.obsoleteAssignmentWrites++
		http.Error(w, "route not found", http.StatusNotFound)
	case r.Method == http.MethodPatch && path == issuePath:
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
		writeFixtureJSON(s.t, w, map[string]any{})
	case r.Method == http.MethodGet && path == issuePath+"/reactions":
		reactions := make([]map[string]any, 0, len(s.reactions))
		for _, content := range s.reactions {
			reactions = append(reactions, map[string]any{"content": content, "user": map[string]any{"login": "Minos"}})
		}
		writeFixtureJSON(s.t, w, reactions)
	case r.Method == http.MethodPost && path == issuePath+"/reactions":
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
bot-login = "Minos"
[listener]
bind = ":0"
[forges.forgejo]
adaptation = %q
api-base = %q
webhook-secret-file = %q
credential-file = %q
[runs]
dir = %q
`, cfg.Forges["forgejo"].Adaptation, cfg.Forges["forgejo"].APIBase,
		cfg.Forges["forgejo"].WebhookSecretFile, cfg.Forges["forgejo"].CredentialFile, cfg.Runs.Dir)
	if err := os.WriteFile(filepath.Join(cfg.Root, "service.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
