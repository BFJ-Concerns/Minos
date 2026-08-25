package shell

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestGroupMemberGuardSuppressesOnlyWhileHolderIsLive(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	runs := t.TempDir()
	member := "minos-run-owner-repo-pr2"
	if err := os.MkdirAll(filepath.Dir(groupMemberGuardPath(runs, member)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(groupMemberGuardPath(runs, member), []byte("minos-run-owner-repo-pr1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("minos-run-owner-repo-pr1.service loaded active running Minos lead\n"), nil
	}
	if !groupMemberGuardActive(t.Context(), runs, member) {
		t.Fatal("live group holder did not suppress member")
	}
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
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
	cfg.Runs.MaxConcurrent = 1
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
