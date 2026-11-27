package shell

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

func TestReconcilePrunesOnlyRetiredReviewedChanges(t *testing.T) {
	for _, test := range []struct {
		state      string
		wantPruned bool
	}{
		{state: "open"},
		{state: "closed", wantPruned: true},
		{state: "merged", wantPruned: true},
	} {
		t.Run(test.state, func(t *testing.T) {
			var cfg ServiceConfig
			cfg.Runs.Dir = t.TempDir()
			facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "1"}
			guard := forge.Guard{Repository: forge.Repository{Owner: facts.Owner, Name: facts.Repo}, PullRequest: 1}
			path := reviewedChangePath(cfg, facts.Forge, guard)
			writeTestFile(t, path, "{}\n")
			walkthrough := walkthroughRecordPath(cfg, facts.Forge, guard)
			writeTestFile(t, walkthrough, "{}\n")
			guard.PullRequest = 2
			other := reviewedChangePath(cfg, facts.Forge, guard)
			writeTestFile(t, other, "{}\n")

			snapshot := forge.Snapshot{State: test.state, PullRequest: 1, Draft: true}
			result, err := reconcilePullRequestSnapshot(t.Context(), cfg, RepoConfig{}, facts, nil, snapshot)
			if err != nil || result.Decision != ReconcileNothing {
				t.Fatalf("reconcile %s = %v, %v", test.state, result, err)
			}
			if _, err := os.Stat(path); (test.wantPruned && !os.IsNotExist(err)) || (!test.wantPruned && err != nil) {
				t.Fatalf("stat reviewed record: %v, want pruned = %t", err, test.wantPruned)
			}
			if _, err := os.Stat(walkthrough); (test.wantPruned && !os.IsNotExist(err)) || (!test.wantPruned && err != nil) {
				t.Fatalf("stat walkthrough record: %v, want pruned = %t", err, test.wantPruned)
			}
			if _, err := os.Stat(other); err != nil {
				t.Fatalf("another pull request lost its record: %v", err)
			}
		})
	}
}

func TestPruneReviewedChangeChecksTheForgeWhenMissingFromOpenList(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg := writeSweepFixtureConfig(t, state)
	repo := RepoConfig{Forge: "forgejo", Owner: "minos-e2e-owner", Repo: "subject"}
	guard := forge.Guard{Repository: forge.Repository{Owner: repo.Owner, Name: repo.Repo}, PullRequest: 1}
	path := reviewedChangePath(cfg, repo.Forge, guard)
	writeTestFile(t, path, "{}\n")
	pruneRetiredRecords(t.Context(), cfg, repo, []int64{guard.PullRequest}, nil)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("open pull request missing from the list lost its record: %v", err)
	}
	state.changePullRequest(func(pr map[string]any) { pr["state"] = "closed" })
	pruneRetiredRecords(t.Context(), cfg, repo, []int64{guard.PullRequest}, nil)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("closed record survived the sweep: %v", err)
	}
}

// A close the service missed leaves the pull request's records behind; the
// sweep's retirement check removes each of them, a command record or a
// walkthrough kept alone included.
func TestSweepPrunesPullRequestRecordsAfterMissedCloseDelivery(t *testing.T) {
	type recordPath func(ServiceConfig, string, forge.Guard) string
	for name, kept := range map[string][]recordPath{
		"all":         {reviewedChangePath, commandRecordPath, walkthroughRecordPath},
		"command":     {commandRecordPath},
		"walkthrough": {walkthroughRecordPath},
	} {
		t.Run(name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			cfg := writeSweepFixtureConfig(t, state)
			guard := forge.Guard{Repository: forge.Repository{Owner: "minos-e2e-owner", Name: "subject"}, PullRequest: 1}
			var paths []string
			for _, path := range kept {
				paths = append(paths, path(cfg, "forgejo", guard))
			}
			for _, path := range paths {
				writeTestFile(t, path, "{}\n")
			}
			state.changePullRequest(func(pr map[string]any) { pr["state"] = "closed" })
			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
			if err := SweepCommand(t.Context(), []string{"--config", cfg.Root}); err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("closed record %s survived the sweep: %v", path, err)
				}
			}
		})
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

func TestSweepKeepsReviewedChangeWhenOpenListingFails(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg := writeSweepFixtureConfig(t, state)
	var lookups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/minos-e2e-owner/subject/pulls":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/api/v1/repos/minos-e2e-owner/subject/pulls/1":
			lookups.Add(1)
			w.WriteHeader(http.StatusNotFound)
		default:
			state.server.Config.Handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(server.Close)
	forgeConfig := cfg.Forges["forgejo"]
	forgeConfig.APIBase = server.URL
	cfg.Forges["forgejo"] = forgeConfig
	writeServiceConfig(t, cfg)
	path := reviewedChangePath(cfg, "forgejo", forge.Guard{
		Repository: forge.Repository{Owner: "minos-e2e-owner", Name: "subject"}, PullRequest: 1,
	})
	writeTestFile(t, path, "{}\n")
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }

	if err := SweepCommand(t.Context(), []string{"--config", cfg.Root}); err == nil {
		t.Fatal("sweep accepted an unreadable open listing")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("reviewed record lost after the listing failed: %v", err)
	}
	if got := lookups.Load(); got != 0 {
		t.Fatalf("pull request retirement lookups = %d after listing failure, want 0", got)
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

func TestGitHubReviewedChangeLookupRequiresEvidenceOfRetirement(t *testing.T) {
	state := newGitHubFixtureState(t)
	var anonymousLookups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets/pulls/9":
			if r.Header.Get("Authorization") == "" {
				t.Error("authenticated lookup reached the forge without credentials")
			}
			w.WriteHeader(http.StatusForbidden)
		case "/repos/acme/widgets/pulls/10":
			w.WriteHeader(http.StatusGone)
		case "/repos/acme/absent/pulls/7":
			anonymousLookups.Add(1)
			w.WriteHeader(http.StatusNotFound)
		default:
			state.server.Config.Handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(server.Close)
	runner := state.runner()
	runner.APIBase = server.URL
	adapter, err := forge.NewAdapter(runner, githubFixtureBotLogin, "Minos")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		repo        string
		number      int64
		wantRetired bool
		wantError   bool
	}{
		{name: "open", repo: "widgets", number: 7},
		{name: "missing", repo: "widgets", number: 8, wantRetired: true},
		{name: "forbidden", repo: "widgets", number: 9, wantError: true},
		{name: "gone", repo: "widgets", number: 10, wantRetired: true},
		{name: "no installation", repo: "absent", number: 7, wantError: true},
		{name: "closed", repo: "widgets", number: 7, wantRetired: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "closed" {
				state.mu.Lock()
				state.pullRequest["state"] = "closed"
				state.mu.Unlock()
			}
			retired, err := adapter.PullRequestRetired(t.Context(), forge.Repository{Owner: "acme", Name: test.repo}, test.number)
			if retired != test.wantRetired || (err != nil) != test.wantError {
				t.Fatalf("retired = %t, error = %v, want retired = %t, error present = %t", retired, err, test.wantRetired, test.wantError)
			}
		})
	}
	if got := anonymousLookups.Load(); got != 0 {
		t.Fatalf("pull request lookups without an installation = %d, want 0", got)
	}
}
