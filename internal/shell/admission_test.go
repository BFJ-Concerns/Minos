package shell

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

func TestAdmissionRulesUseCurrentMetadata(t *testing.T) {
	ready := forge.Snapshot{
		Author: "Author", Labels: []string{"Ready", "Reviewable"},
		TargetBranch: "main", Title: "Release v2", HeadRepository: "owner/repo", TargetRepository: "Owner/Repo",
	}
	rules := AdmissionRules{
		Labels: []string{"Another", "Ready"}, ExcludeLabels: []string{"Blocked"},
		Authors: []string{"author"}, BaseBranches: []string{"main"}, TitlePattern: "^Release ",
	}
	if reasons := rules.metadataDeferrals(ready); len(reasons) != 0 {
		t.Fatalf("matching request deferred: %v", reasons)
	}
	for _, test := range []struct {
		name   string
		change func(*forge.Snapshot)
		reason string
	}{
		{"missing label", func(s *forge.Snapshot) { s.Labels = []string{"ready"} }, "admission label did not match"},
		{"excluded label wins", func(s *forge.Snapshot) { s.Labels = []string{"Ready", "Blocked"} }, "admission excludes a pull request label"},
		{"author", func(s *forge.Snapshot) { s.Author = "another" }, "admission author did not match"},
		{"base branch", func(s *forge.Snapshot) { s.TargetBranch = "Main" }, "admission base branch did not match"},
		{"title", func(s *forge.Snapshot) { s.Title = "Draft release" }, "admission title did not match"},
		{"draft", func(s *forge.Snapshot) { s.Draft = true }, "draft"},
		{"fork", func(s *forge.Snapshot) { s.HeadRepository = "forker/repo" }, "admission excludes forks"},
		{"unknown source", func(s *forge.Snapshot) { s.HeadRepository = "" }, "admission source repository unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := ready
			test.change(&snapshot)
			if got := rules.metadataDeferrals(snapshot); !slices.Equal(got, []string{test.reason}) {
				t.Fatalf("deferrals = %v, want %q", got, test.reason)
			}
		})
	}
	if got := (AdmissionRules{Drafts: "only"}).metadataDeferrals(ready); !slices.Equal(got, []string{"admission requires a draft"}) {
		t.Fatalf("draft-only deferrals = %v", got)
	}
	ready.HeadRepository = "forker/repo"
	ready.Draft = true
	for _, drafts := range []string{"any", "only"} {
		if got := (AdmissionRules{Drafts: drafts, AllowForks: true}).metadataDeferrals(ready); len(got) != 0 {
			t.Fatalf("explicitly allowed fork draft deferred: %v", got)
		}
	}
	ready.HeadRepository = ""
	if got := (AdmissionRules{Drafts: "any", AllowForks: true}).metadataDeferrals(ready); len(got) != 1 {
		t.Fatalf("allow-forks with unknown source returned %v", got)
	}
}

func TestAdmissionConfigReplacesWholeTable(t *testing.T) {
	root := t.TempDir()
	service := testServiceConfig + `
[repositories.admission]
labels = ["Fleet"]
allow-forks = true
drafts = "any"
`
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(service), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "repos")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, table string }{
		{"inherit", ""},
		{"replace", "[admission]\nbase-branches = [\"main\"]\n"},
		{"reset", "[admission]\n"},
	} {
		body := fmt.Sprintf("forge = \"local\"\nowner = \"owner\"\nrepo = %q\n[adaptation]\nrun-body = \"/run-body\"\n", test.name) + test.table
		if err := os.WriteFile(filepath.Join(dir, test.name+".toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	repos, err := LoadRepoConfigs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range repos {
		got := repo.Admission
		if repo.Repo == "inherit" {
			if !slices.Equal(got.Labels, []string{"Fleet"}) || !got.AllowForks || got.Drafts != "any" {
				t.Fatalf("inherited admission = %+v", got)
			}
			continue
		}
		if len(got.Labels) > 0 || got.AllowForks || got.Drafts != "" {
			t.Fatalf("%s retained service admission fields: %+v", repo.Repo, got)
		}
		if repo.Repo == "replace" && !slices.Equal(got.BaseBranches, []string{"main"}) {
			t.Fatalf("replacement admission = %+v", got)
		}
	}
}

func TestAdmissionConfigRejectsInvalidRules(t *testing.T) {
	for _, setting := range []string{
		`drafts = "unknown"`, `title-pattern = "["`, `paths = ["["]`,
		`paths = ["/outside"]`, `paths = ["../outside"]`, `labels = [""]`,
	} {
		t.Run(setting, func(t *testing.T) {
			root := t.TempDir()
			body := testServiceConfig + "\n[repositories.admission]\n" + setting + "\n"
			if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadServiceConfig(root); err == nil || !strings.Contains(err.Error(), "admission.") {
				t.Fatalf("config error = %v, want admission validation", err)
			}
		})
	}
}

func TestAdmissionWebhookAndSweepExcludeWithoutWriting(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg := writeSweepFixtureConfigWithKnobs(t, state, labelMarkerKnobs+`
[admission]
labels = ["Ready"]
paths = ["internal/"]
`)
	defineMarkerLabels(state)
	state.setPullRequestLabels(state.repositoryLabels[2:])
	// The delivery has Ready, but the current forge snapshot has only the stale markers.
	fixture := readFixture(t, "004-pull_request-label_updated.json")
	routeConfig := cfg
	routeConfig.Forges = map[string]ForgeConfig{"local": cfg.Forges["forgejo"]}
	if err := os.WriteFile(routeConfig.Forges["local"].WebhookSecretFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.Root, "repos", "subject.toml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(body), `forge = "forgejo"`, `forge = "local"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}

	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	starts := 0
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemd-run" {
			starts++
		}
		return nil, nil
	}
	response, err := sendAuthenticatedHook(t, routeConfig, "pull_request", fixture.Body)
	if err != nil || response.Code != http.StatusAccepted || response.Body.String() != "nothing\n" {
		t.Fatalf("hook response = %d %q, error = %v", response.Code, response.Body.String(), err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SweepCommand(t.Context(), []string{"--config", cfg.Root}); err != nil {
		t.Fatal(err)
	}
	doc := projectionJSON(t, sweepDeferralsPath(cfg))
	if row := projectionRow(t, doc, "deferrals"); row["reason"] != "admission label did not match" || starts != 0 || len(state.writeSequence) != 0 {
		t.Fatalf("excluded request: row = %v, starts = %d, writes = %v", row, starts, state.writeSequence)
	}
	state.mu.Lock()
	state.setPullRequestLabels(state.repositoryLabels[:1])
	state.changedFilePages = [][]map[string]any{
		{{"filename": "docs/readme.md", "status": "added"}},
	}
	state.mu.Unlock()
	if err := SweepCommand(t.Context(), []string{"--config", cfg.Root}); err != nil {
		t.Fatal(err)
	}
	doc = projectionJSON(t, sweepDeferralsPath(cfg))
	if row := projectionRow(t, doc, "deferrals"); row["reason"] != "admission paths did not match" || starts != 0 || len(state.writeSequence) != 0 {
		t.Fatalf("excluded paths: row = %v, starts = %d, writes = %v", row, starts, state.writeSequence)
	}

	state.mu.Lock()
	state.changedFilePages = append(state.changedFilePages, state.changedFilePages[0])
	state.mu.Unlock()
	repos, err := LoadRepoConfigs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, _, facts := state.service(t)
	_, err = reconcilePullRequest(t.Context(), cfg, repos[0], facts)
	if err == nil || !strings.Contains(err.Error(), "duplicate changed-file records") || starts != 0 || len(state.writeSequence) != 0 {
		t.Fatalf("unusable files: error = %v, starts = %d, writes = %v", err, starts, state.writeSequence)
	}

	state.mu.Lock()
	state.changedFilePages = append(state.changedFilePages[:1], []map[string]any{
		{"filename": "archive/old.go", "previous_filename": "internal/nested/old.go", "status": "renamed"},
	})
	state.mu.Unlock()
	if err := SweepCommand(t.Context(), []string{"--config", cfg.Root}); err != nil {
		t.Fatal(err)
	}
	doc = projectionJSON(t, sweepDeferralsPath(cfg))
	if row := projectionRow(t, doc, "readied"); row["decision"] != string(SpawnStarted) || starts != 1 {
		t.Fatalf("admitted rename: row = %v, starts = %d", row, starts)
	}
}

func TestAdmissionChangedPathsOnGitHub(t *testing.T) {
	state := newGitHubFixtureState(t)
	state.mu.Lock()
	for i := range 100 {
		state.changedFiles = append(state.changedFiles, map[string]any{"filename": fmt.Sprintf("docs/%d.txt", i), "status": "removed"})
	}
	state.changedFiles = append(state.changedFiles, map[string]any{
		"filename": "docs/new.txt", "previous_filename": "nested/old.txt", "status": "renamed",
	})
	state.pullRequest["changed_files"] = len(state.changedFiles)
	state.mu.Unlock()

	adapter := state.adapter(t)
	guard := state.guard()
	paths, err := adapter.ChangedPaths(t.Context(), guard, "main")
	if err != nil || len(paths) != 102 || !slices.Contains(paths, "nested/old.txt") || !slices.Contains(paths, "docs/99.txt") {
		t.Fatalf("changed paths count = %d, error = %v, paths = %v", len(paths), err, paths)
	}
	stale := guard
	stale.TargetSHA = "earlier-target"
	if _, err := adapter.ChangedPaths(t.Context(), stale, "main"); err == nil || !strings.Contains(err.Error(), "target moved") {
		t.Fatalf("stale target error = %v", err)
	}
	state.mu.Lock()
	state.pullRequest["changed_files"] = len(state.changedFiles) + 1
	state.mu.Unlock()
	if _, err := adapter.ChangedPaths(t.Context(), guard, "main"); err == nil || !strings.Contains(err.Error(), "incomplete changed-file list") {
		t.Fatalf("incomplete list error = %v", err)
	}
}
