package shell

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	pruneRetiredReviewedChanges(t.Context(), cfg, repo, []int64{guard.PullRequest}, nil)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("open pull request missing from the list lost its record: %v", err)
	}
	state.changePullRequest(func(pr map[string]any) { pr["state"] = "closed" })
	pruneRetiredReviewedChanges(t.Context(), cfg, repo, []int64{guard.PullRequest}, nil)
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

func TestSweepPrunesGoneAndUnenrolledReviewedChanges(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg := writeSweepFixtureConfig(t, state)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/minos-e2e-owner/subject/pulls/2":
			w.WriteHeader(http.StatusNotFound)
		case "/api/v1/repos/minos-e2e-owner/subject/pulls/3":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/api/v1/repos/minos-e2e-owner/subject/pulls/4":
			w.WriteHeader(http.StatusGone)
		default:
			state.server.Config.Handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(server.Close)
	configuration := cfg.Forges["forgejo"]
	configuration.APIBase = server.URL
	cfg.Forges["forgejo"] = configuration
	writeServiceConfig(t, cfg)
	state.changePullRequest(func(pr map[string]any) { pr["state"] = "closed" })
	repo := forge.Repository{Owner: "minos-e2e-owner", Name: "subject"}
	unenrolled := forge.Repository{Owner: repo.Owner, Name: "old-subject"}
	writeTestFile(t, reviewedChangePath(cfg, "forgejo", forge.Guard{Repository: unenrolled, PullRequest: 1}), "{}\n")
	writeTestFile(t, reviewedChangePath(cfg, "former-forge", forge.Guard{Repository: repo, PullRequest: 1}), "{}\n")
	for _, number := range []int64{1, 2, 3, 4} {
		path := reviewedChangePath(cfg, "forgejo", forge.Guard{Repository: repo, PullRequest: number})
		writeTestFile(t, path, "{}\n")
	}
	other := filepath.Join(cfg.Runs.Dir, ".reviewed", "unrelated-4.json")
	writeTestFile(t, other, "{}\n")
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }

	if err := SweepCommand(t.Context(), []string{"--config", cfg.Root}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(other))
	if err != nil {
		t.Fatal(err)
	}
	keep := reviewedChangePath(cfg, "forgejo", forge.Guard{Repository: repo, PullRequest: 3})
	if len(entries) != 2 || entries[0].Name() != filepath.Base(keep) || entries[1].Name() != filepath.Base(other) {
		t.Fatalf("remaining reviewed directory entries = %v, want %s and %s", entries, filepath.Base(keep), filepath.Base(other))
	}
}

func TestSweepKeepsReviewedChangesWhenEnrolmentCannotBeRead(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg := writeSweepFixtureConfig(t, state)
	path := reviewedChangePath(cfg, "forgejo", forge.Guard{
		Repository: forge.Repository{Owner: "minos-e2e-owner", Name: "subject"}, PullRequest: 1,
	})
	writeTestFile(t, path, "{}\n")
	repoDir := filepath.Join(cfg.Root, "repos")
	if err := os.Rename(repoDir, filepath.Join(cfg.Root, "saved-repos")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, repoDir, "not a directory")
	if err := SweepCommand(t.Context(), []string{"--config", cfg.Root}); err == nil {
		t.Fatal("sweep accepted unreadable enrolment")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("reviewed record lost after enrolment could not be read: %v", err)
	}
}

func TestGitHubReviewedChangeLookupConfirmsMissingPullRequest(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter := state.adapter(t)
	repo := state.guard().Repository
	retired, err := adapter.PullRequestRetired(t.Context(), repo, 7)
	if err != nil || retired {
		t.Fatalf("open pull request retired = %t, %v", retired, err)
	}
	retired, err = adapter.PullRequestRetired(t.Context(), repo, 8)
	if err != nil || !retired {
		t.Fatalf("missing pull request retired = %t, %v", retired, err)
	}
}
