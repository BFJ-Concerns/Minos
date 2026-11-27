package shell

import "testing"

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
	cfg := ServiceConfig{Forges: map[string]ForgeConfig{
		"github": {APIBase: state.server.URL, Adaptation: state.adaptationPath, CredentialFile: state.credentialPath},
	}}
	reader := newStateReader(cfg, nil)
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
