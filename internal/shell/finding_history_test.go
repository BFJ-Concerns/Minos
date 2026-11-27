package shell

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestFindingHistoryReadsGitHubPullRequestsInEveryState(t *testing.T) {
	state := newGitHubFixtureState(t)
	state.mu.Lock()
	state.expectedPullsState = "all"
	for number := 11; number > 0; number-- {
		state.pullRequests = append(state.pullRequests, map[string]any{
			"number":   number,
			"state":    "closed",
			"user":     map[string]any{"login": "author"},
			"html_url": "https://github.example/acme/widgets/pull/1",
		})
	}
	state.mu.Unlock()
	cfg := statusTestConfig(t)
	cfg.Forges["github"] = ForgeConfig{
		APIBase: state.server.URL, Adaptation: state.adaptationPath,
		CredentialFile: state.credentialPath, WebhookSecretFile: "/etc/minos/webhook.secret",
	}
	writeMonitoringConfig(t, cfg)
	reader := NewStateReader(cfg.Root)
	repo := RepoConfig{Forge: "github", Owner: "acme", Repo: "widgets"}
	first, more, err := reader.FindingHistoryPullRequests(t.Context(), repo, 1)
	if err != nil || len(first) != 10 || !more || first[0].Number != 11 {
		t.Fatalf("initial page = %#v, more = %t, error = %v", first, more, err)
	}
	second, more, err := reader.FindingHistoryPullRequests(t.Context(), repo, 2)
	want := FindingHistoryPullRequest{Number: 1, Author: "author", URL: "https://github.example/acme/widgets/pull/1"}
	if err != nil || len(second) != 1 || more || second[0] != want {
		t.Fatalf("older page = %#v, more = %t, error = %v", second, more, err)
	}
	if writes := state.operationWrites(); len(writes) != 0 {
		t.Fatalf("reading findings made forge writes: %#v", writes)
	}
}

func TestFindingHistoryReadsNewForgejoEnrolment(t *testing.T) {
	cfg := statusTestConfig(t)
	reader := NewStateReader(cfg.Root)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/api/v1/repos/acme/widgets/pulls?state=all&sort=newest&limit=10&page=2"
		if r.Method != http.MethodGet || r.URL.RequestURI() != want || r.Header.Get("Authorization") != "token fixture-token" {
			t.Errorf("unexpected Forgejo request: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"index":2,"state":"closed","merged":true,"user":{"username":"author"},"html_url":"https://forgejo.example/acme/widgets/pulls/2"},
			{"number":1,"state":"closed","user":{"login":"another"}}
		]`))
	}))
	t.Cleanup(server.Close)
	adaptation, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(cfg.Root, "local.token")
	writeStatusFile(t, credential, "fixture-token\n")
	cfg.Forges["local"] = ForgeConfig{
		APIBase: server.URL, Adaptation: adaptation, CredentialFile: credential,
		WebhookSecretFile: "/etc/minos/webhook.secret",
	}
	writeMonitoringConfig(t, cfg)
	repo := RepoConfig{Forge: "local", Owner: "acme", Repo: "widgets"}
	pulls, more, err := reader.FindingHistoryPullRequests(t.Context(), repo, 2)
	want := FindingHistoryPullRequest{Number: 2, Author: "author", URL: "https://forgejo.example/acme/widgets/pulls/2"}
	if err != nil || more || len(pulls) != 2 || pulls[0] != want || pulls[1].Author != "another" {
		t.Fatalf("Forgejo page = %#v, more = %t, error = %v", pulls, more, err)
	}
}
