package shell

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

func TestReconcileRemovesClosedReviewedChange(t *testing.T) {
	var cfg ServiceConfig
	cfg.Runs.Dir = t.TempDir()
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "1"}
	guard := forge.Guard{Repository: forge.Repository{Owner: facts.Owner, Name: facts.Repo}, PullRequest: 1}
	path := reviewedChangePath(cfg, facts.Forge, guard)
	writeTestFile(t, path, "{}\n")
	guard.PullRequest = 2
	other := reviewedChangePath(cfg, facts.Forge, guard)
	writeTestFile(t, other, "{}\n")

	for _, state := range []string{"closed", "merged"} {
		result, err := reconcilePullRequestSnapshot(t.Context(), cfg, RepoConfig{}, facts, nil, forge.Snapshot{State: state, PullRequest: 1})
		if err != nil || result.Decision != ReconcileNothing {
			t.Fatalf("reconcile %s = %v, %v", state, result, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("closed record survived: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another pull request lost its record: %v", err)
	}
}

func TestPruneReviewedChangeChecksTheForgeWhenMissingFromOpenList(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg := writeSweepFixtureConfig(t, state)
	repo := RepoConfig{Forge: "forgejo", Owner: "minos-e2e-owner", Repo: "subject"}
	guard := forge.Guard{Repository: forge.Repository{Owner: repo.Owner, Name: repo.Repo}, PullRequest: 1}
	path := reviewedChangePath(cfg, repo.Forge, guard)
	writeTestFile(t, path, "{}\n")
	entries := readReviewedChangeEntries(cfg)
	pruneClosedReviewedChanges(t.Context(), cfg, repo, entries, nil)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("open pull request missing from the list lost its record: %v", err)
	}
	state.changePullRequest(func(pr map[string]any) { pr["state"] = "closed" })
	pruneClosedReviewedChanges(t.Context(), cfg, repo, entries, nil)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("closed record survived the sweep: %v", err)
	}
}

func TestSweepPrunesReviewedChangeAfterMissedCloseDelivery(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg := writeSweepFixtureConfig(t, state)
	guard := forge.Guard{Repository: forge.Repository{Owner: "minos-e2e-owner", Name: "subject"}, PullRequest: 1}
	path := reviewedChangePath(cfg, "forgejo", guard)
	writeTestFile(t, path, "{}\n")
	state.changePullRequest(func(pr map[string]any) { pr["state"] = "closed" })
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	if err := SweepCommand(t.Context(), []string{"--config", cfg.Root}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("closed record %s survived the sweep: %v", filepath.Base(path), err)
	}
}
