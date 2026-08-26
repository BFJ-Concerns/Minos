package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/product"
)

func TestGroupCandidateEnumerationSurfacesSameTargetSibling(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.setStackedChildren([]map[string]any{stackedFixturePull(2, "main", "sibling")})
	cfg, repo, primary := state.service(t)
	all := []sweepCandidate{{repo: repo, facts: primary}, {repo: repo, facts: Facts{Forge: primary.Forge, Owner: primary.Owner, Repo: primary.Repo, PR: "2"}}}
	encoded, err := groupCandidatesJSON(t.Context(), cfg, repo, primary, all)
	if err != nil {
		t.Fatal(err)
	}
	var candidates []Facts
	if err := json.Unmarshal([]byte(encoded), &candidates); err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].PR != "2" || candidates[0].BaseRef != "main" {
		t.Fatalf("group candidates = %#v, want sibling #2 targeting main", candidates)
	}
}

func TestGroupCandidateEnumerationExcludesNonMembersAtTheEnumerationSeam(t *testing.T) {
	state := newForgejoFixtureState(t)
	eligible := stackedFixturePull(2, "main", "eligible")
	fork := stackedFixturePull(3, "main", "fork")
	fork["head"].(map[string]any)["repo"] = map[string]any{"full_name": "elsewhere/subject"}
	structural := stackedFixturePull(4, "main", "structural/rework")
	differentTarget := stackedFixturePull(5, "release", "release-member")
	live := stackedFixturePull(6, "main", "already-running")
	guarded := stackedFixturePull(7, "main", "guarded")
	state.setStackedChildren([]map[string]any{eligible, fork, structural, differentTarget, live, guarded})
	cfg, repo, primary := state.service(t)
	repo.StructuralBranchPrefixes = []string{"structural/"}
	if err := os.MkdirAll(filepath.Dir(groupMemberGuardPath(cfg.Runs.Dir, UnitName(Facts{Forge: primary.Forge, Owner: primary.Owner, Repo: primary.Repo, PR: "7"}))), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(groupMemberGuardPath(cfg.Runs.Dir, UnitName(Facts{Forge: primary.Forge, Owner: primary.Owner, Repo: primary.Repo, PR: "7"})), []byte("minos-run-owner-repo-pr1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	all := []sweepCandidate{{repo: repo, facts: primary}}
	for number := 2; number <= 7; number++ {
		all = append(all, sweepCandidate{repo: repo, facts: Facts{Forge: primary.Forge, Owner: primary.Owner, Repo: primary.Repo, PR: strconv.Itoa(number)}})
	}
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("minos-run-minos-e2e-owner-subject-pr6.service loaded active running Minos lead\nminos-run-owner-repo-pr1.service loaded active running Minos lead\n"), nil
	}
	encoded, err := groupCandidatesJSON(t.Context(), cfg, repo, primary, all)
	if err != nil {
		t.Fatal(err)
	}
	var candidates []Facts
	if err := json.Unmarshal([]byte(encoded), &candidates); err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].PR != "2" {
		t.Fatalf("group candidates = %#v, want only eligible sibling #2", candidates)
	}
}

func TestGroupCandidateEnumerationDoesNotGiveStructuralPrimaryOrdinarySiblings(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["head"].(map[string]any)["ref"] = "structural/rework"
	})
	state.setStackedChildren([]map[string]any{stackedFixturePull(2, "main", "sibling")})
	cfg, repo, primary := state.service(t)
	repo.StructuralBranchPrefixes = []string{"structural/"}
	all := []sweepCandidate{{repo: repo, facts: primary}, {repo: repo, facts: Facts{Forge: primary.Forge, Owner: primary.Owner, Repo: primary.Repo, PR: "2"}}}
	encoded, err := groupCandidatesJSON(t.Context(), cfg, repo, primary, all)
	if err != nil {
		t.Fatal(err)
	}
	if encoded != "[]" {
		t.Fatalf("structural primary group candidates = %s, want none", encoded)
	}
}

func TestGroupCandidateEnumerationExcludesEachAdmissionDeferral(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*forgejoFixtureState, map[string]any, ServiceConfig, Facts)
	}{
		{name: "work in progress branch", mutate: func(_ *forgejoFixtureState, child map[string]any, _ ServiceConfig, _ Facts) {
			child["head"].(map[string]any)["ref"] = "wip/member"
		}},
		{name: "terminal review", mutate: func(_ *forgejoFixtureState, child map[string]any, _ ServiceConfig, _ Facts) {
			head := child["head"].(map[string]any)["sha"]
			child["commits"] = []map[string]any{{"sha": head, "author": map[string]any{"login": "Minos"}}}
			child["reviews"] = []map[string]any{{"id": 1, "state": "APPROVED", "commit_id": head, "user": map[string]any{"login": "Minos"}}}
		}},
		{name: "completed status", mutate: func(state *forgejoFixtureState, child map[string]any, cfg ServiceConfig, _ Facts) {
			head := child["head"].(map[string]any)["sha"]
			child["commits"] = []map[string]any{{"sha": head, "author": map[string]any{"login": "Minos"}}}
			child["statuses"] = []map[string]any{{"id": 1, "context": "Minos", "status": "success", "description": product.Clean().Description(), "target_url": statusTargetURL(cfg.Forges["forgejo"].APIBase, Facts{Owner: "minos-e2e-owner", Repo: "subject", PR: "2", BaseSHA: state.targetSHA()}), "creator": map[string]any{"login": "Minos"}}}
		}},
		{name: "open dependency", mutate: func(_ *forgejoFixtureState, child map[string]any, _ ServiceConfig, _ Facts) {
			child["dependencies"] = []map[string]any{{"number": 9, "state": "open", "repository": map[string]any{"full_name": "minos-e2e-owner/prerequisite"}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			child := stackedFixturePull(2, "main", "sibling")
			state.setStackedChildren([]map[string]any{child})
			cfg, repo, primary := state.service(t)
			repo.WorkInProgressBranchPrefixes = []string{"wip/"}
			test.mutate(state, child, cfg, primary)
			encoded, err := groupCandidatesJSON(t.Context(), cfg, repo, primary, []sweepCandidate{{repo: repo, facts: primary}, {repo: repo, facts: Facts{Forge: primary.Forge, Owner: primary.Owner, Repo: primary.Repo, PR: "2"}}})
			if err != nil {
				t.Fatal(err)
			}
			if encoded != "[]" {
				t.Fatalf("group candidates = %s, want excluded ineligible sibling", encoded)
			}
		})
	}
}

func TestResolvedGroupTargetFollowsStackedChain(t *testing.T) {
	child := forge.Snapshot{HeadBranch: "child", TargetBranch: "parent"}
	parent := forge.Snapshot{HeadBranch: "parent", TargetBranch: "main"}
	if got := resolvedGroupTarget(child, map[string]forge.Snapshot{"2": child, "3": parent}, map[string]bool{}); got != "main" {
		t.Fatalf("resolved target = %q, want main", got)
	}
}

func TestGroupMemberGuardSuppressesWhileHolderIsLiveOrContinuable(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	runs := t.TempDir()
	cfg := ServiceConfig{}
	cfg.Runs.Dir = runs
	primaryFacts := Facts{Owner: "owner", Repo: "repo", PR: "1", HeadSHA: "head"}
	primary := UnitName(primaryFacts)
	member := "minos-run-owner-repo-pr2"
	if err := os.MkdirAll(filepath.Dir(groupMemberGuardPath(runs, member)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(groupMemberGuardPath(runs, member), []byte(primary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(primary + ".service loaded active running Minos lead\n"), nil
	}
	if !groupMemberGuardActive(t.Context(), runs, member) {
		t.Fatal("live group holder did not suppress member")
	}
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	runDir := filepath.Join(runs, primary+"-preserved")
	if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	handoff := writeTestHandoff(t, cfg, primaryFacts, runDir, primaryFacts.HeadSHA, json.RawMessage(`{"round":0,"confirmedUnfixed":[]}`))
	if !groupMemberGuardActive(t.Context(), runs, member) {
		t.Fatal("continuable group holder did not suppress member")
	}
	if err := sweepDeadGroupMemberGuards(t.Context(), runs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(groupMemberGuardPath(runs, member)); err != nil {
		t.Fatalf("continuable group holder guard was swept: %v", err)
	}
	if err := os.Remove(handoff); err != nil {
		t.Fatal(err)
	}
	if groupMemberGuardActive(t.Context(), runs, member) {
		t.Fatal("dead group holder suppressed member")
	}
	if !strings.Contains(groupMemberGuardPath(runs, member), ".group-members") {
		t.Fatal("guard path escaped its admission directory")
	}
}

func TestSpawnRunSuppressesLiveGroupMemberWithoutSpendingConcurrency(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	cfg := ServiceConfig{Root: "/etc/minos", Forges: map[string]ForgeConfig{"forgejo": {}}}
	cfg.Runs.Dir = t.TempDir()
	cfg.Runs.MaxConcurrent = 2
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "2"}
	if err := os.MkdirAll(filepath.Dir(groupMemberGuardPath(cfg.Runs.Dir, UnitName(facts))), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(groupMemberGuardPath(cfg.Runs.Dir, UnitName(facts)), []byte("minos-run-owner-repo-pr1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("minos-run-owner-repo-pr1.service loaded active running Minos lead\n"), nil
	}
	result, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts, AdmissionContext{}, RunClassReview)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != SpawnSuppressed || result.BlockingUnit != "minos-run-owner-repo-pr1.service" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSpawnRunSuppressesGroupMemberWhileHolderAwaitsContinuation(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	cfg := ServiceConfig{Root: "/etc/minos", Forges: map[string]ForgeConfig{"forgejo": {}}}
	cfg.Runs.Dir = t.TempDir()
	cfg.Runs.MaxConcurrent = 2
	primary := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "1", HeadSHA: "head"}
	member := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "2"}
	guard := groupMemberGuardPath(cfg.Runs.Dir, UnitName(member))
	if err := os.MkdirAll(filepath.Dir(guard), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guard, []byte(UnitName(primary)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(primary)+"-preserved")
	if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestHandoff(t, cfg, primary, runDir, primary.HeadSHA, json.RawMessage(`{"round":0,"confirmedUnfixed":[]}`))
	starts := 0
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemd-run" {
			starts++
		}
		return nil, nil
	}
	result, err := SpawnRun(t.Context(), cfg, RepoConfig{}, member, AdmissionContext{}, RunClassReview)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != SpawnSuppressed || result.BlockingUnit != UnitName(primary)+".service" || starts != 0 {
		t.Fatalf("continuation-gap member result = %+v, starts = %d", result, starts)
	}
}

func TestSpawnRunReleasesGroupMembersWhenContinuationHeadMoved(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	cfg := ServiceConfig{Root: "/etc/minos", Forges: map[string]ForgeConfig{"forgejo": {}}}
	cfg.Runs.Dir = t.TempDir()
	cfg.Runs.MaxConcurrent = 2
	primary := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "1", HeadSHA: "new-head"}
	member := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "2"}
	guard := groupMemberGuardPath(cfg.Runs.Dir, UnitName(member))
	if err := os.MkdirAll(filepath.Dir(guard), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guard, []byte(UnitName(primary)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(primary)+"-preserved")
	if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestHandoff(t, cfg, primary, runDir, "old-head", json.RawMessage(`{"round":0,"confirmedUnfixed":[]}`))

	primaryStarted := false
	memberStarted := false
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && primaryStarted {
			return []byte(UnitName(primary) + ".service loaded active running Minos lead\n"), nil
		}
		if name == "systemd-run" {
			if slices.Contains(args, UnitName(primary)) {
				primaryStarted = true
			}
			if slices.Contains(args, UnitName(member)) {
				memberStarted = true
			}
		}
		return nil, nil
	}
	result, err := SpawnRun(t.Context(), cfg, RepoConfig{}, primary, AdmissionContext{}, RunClassReview)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != SpawnStarted || !primaryStarted {
		t.Fatalf("fresh primary result = %+v, started = %t", result, primaryStarted)
	}
	if _, err := os.Stat(guard); !os.IsNotExist(err) {
		t.Fatalf("stale group guard survived rejected continuation: %v", err)
	}
	result, err = SpawnRun(t.Context(), cfg, RepoConfig{}, member, AdmissionContext{}, RunClassReview)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != SpawnStarted || !memberStarted {
		t.Fatalf("released member result = %+v, started = %t", result, memberStarted)
	}
}

func TestSpawnRunAdmitsUnrelatedPullRequestWhileLiveGroupGuardExists(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	cfg := ServiceConfig{Root: "/etc/minos", Forges: map[string]ForgeConfig{"forgejo": {}}}
	cfg.Runs.Dir = t.TempDir()
	cfg.Runs.MaxConcurrent = 2
	guarded := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "2"}
	if err := os.MkdirAll(filepath.Dir(groupMemberGuardPath(cfg.Runs.Dir, UnitName(guarded))), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(groupMemberGuardPath(cfg.Runs.Dir, UnitName(guarded)), []byte("minos-run-owner-repo-pr1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	starts := 0
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemctl" {
			return []byte("minos-run-owner-repo-pr1.service loaded active running Minos lead\n"), nil
		}
		if name == "systemd-run" {
			starts++
		}
		return nil, nil
	}
	unrelated := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "3"}
	result, err := SpawnRun(t.Context(), cfg, RepoConfig{}, unrelated, AdmissionContext{}, RunClassReview)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != SpawnStarted || starts != 1 {
		t.Fatalf("unrelated result = %+v, starts = %d; live group guard must not spend a slot", result, starts)
	}
}

func TestSpawnRunAdmitsDeadGroupGuard(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	cfg := ServiceConfig{Root: "/etc/minos", Forges: map[string]ForgeConfig{"forgejo": {}}}
	cfg.Runs.Dir = t.TempDir()
	cfg.Runs.MaxConcurrent = 1
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "2"}
	if err := os.MkdirAll(filepath.Dir(groupMemberGuardPath(cfg.Runs.Dir, UnitName(facts))), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(groupMemberGuardPath(cfg.Runs.Dir, UnitName(facts)), []byte("minos-run-owner-repo-pr1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var started bool
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemd-run" {
			started = true
		}
		return nil, nil
	}
	result, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts, AdmissionContext{}, RunClassReview)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != SpawnStarted || !started {
		t.Fatalf("dead guard result = %+v, started = %t", result, started)
	}
}

func TestClaimGroupMemberRefusesOwnLiveUnit(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.setStackedChildren([]map[string]any{stackedFixturePull(2, "main", "sibling")})
	cfg, repo, primary := state.service(t)
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("minos-run-minos-e2e-owner-subject-pr2.service loaded active running Minos lead\n"), nil
	}
	_, err := claimGroupMember(t.Context(), cfg, repo, primary, repo.Owner, repo.Repo, "2")
	if err == nil || !strings.Contains(err.Error(), "already has a live run") {
		t.Fatalf("claim error = %v, want live-run refusal", err)
	}
}

func TestForgeCommandClaimMemberClaimsFixtureSiblingAndWritesGuard(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.setStackedChildren([]map[string]any{stackedFixturePull(2, "main", "sibling")})
	cfg, repo, primary := state.service(t)
	cfg = writeSweepFixtureConfig(t, state)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", primary.Forge)
	t.Setenv("MINOS_OWNER", primary.Owner)
	t.Setenv("MINOS_REPO_NAME", primary.Repo)
	t.Setenv("MINOS_PR", primary.PR)
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }

	var stdout bytes.Buffer
	if err := ForgeCommand(t.Context(), []string{"claim-member", repo.Owner, repo.Repo, "2"}, &stdout); err != nil {
		t.Fatal(err)
	}
	var claimed Facts
	if err := json.Unmarshal(stdout.Bytes(), &claimed); err != nil {
		t.Fatal(err)
	}
	if claimed.PR != "2" || claimed.HeadRef != "sibling" {
		t.Fatalf("claimed member = %#v", claimed)
	}
	guard, err := os.ReadFile(groupMemberGuardPath(cfg.Runs.Dir, UnitName(claimed)))
	if err != nil {
		t.Fatal(err)
	}
	if string(guard) != UnitName(primary)+"\n" {
		t.Fatalf("guard = %q, want primary unit", guard)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !slices.Equal(state.assignmentWritePaths, []string{"/api/v1/repos/minos-e2e-owner/subject/issues/2"}) ||
		!slices.Equal(state.reactionWritePaths, []string{"/api/v1/repos/minos-e2e-owner/subject/issues/2/reactions"}) ||
		!strings.Contains(strings.Join(state.assignees, ","), "Minos") || !strings.Contains(strings.Join(state.reactions, ","), "eyes") {
		t.Fatalf("fixture member claim = assignment paths:%v reaction paths:%v assignees:%v reactions:%v", state.assignmentWritePaths, state.reactionWritePaths, state.assignees, state.reactions)
	}
}

func TestForgeCommandSnapshotMemberReadsRequestedMember(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.setStackedChildren([]map[string]any{stackedFixturePull(2, "main", "sibling")})
	cfg, repo, primary := state.service(t)
	cfg = writeSweepFixtureConfig(t, state)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", primary.Forge)
	t.Setenv("MINOS_OWNER", primary.Owner)
	t.Setenv("MINOS_REPO_NAME", primary.Repo)
	t.Setenv("MINOS_PR", primary.PR)

	var stdout bytes.Buffer
	if err := ForgeCommand(t.Context(), []string{"snapshot-member", repo.Owner, repo.Repo, "2"}, &stdout); err != nil {
		t.Fatal(err)
	}
	var snapshot forge.Snapshot
	if err := json.Unmarshal(stdout.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.PullRequest != 2 || snapshot.HeadBranch != "sibling" || snapshot.TargetBranch != "main" {
		t.Fatalf("member snapshot = %#v", snapshot)
	}
}
