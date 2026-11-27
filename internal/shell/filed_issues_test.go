package shell

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

func TestFiledIssuesReadsOnlyOpenMarkedIssuesFromTheService(t *testing.T) {
	const body = "Earlier advice at src/parse.go line 9.\n<!-- minos:ZmlsZWQ -->"
	issue := func(title, text, state, author string) map[string]any {
		return map[string]any{"title": title, "body": text, "state": state, "user": map[string]any{"login": author}}
	}
	fixtures := func(login string, pageSize int) []map[string]any {
		pull := issue("Pull request", body, "open", login)
		pull["pull_request"] = map[string]any{}
		issues := []map[string]any{
			pull,
			issue("Closed", body, "closed", login),
			issue("Copied marker", body, "open", "someone-else"),
			issue("Unmarked", "Ordinary issue", "open", login),
		}
		for i := range pageSize {
			issues = append(issues, issue(fmt.Sprintf("Ordinary issue %d", i), "", "open", login))
		}
		return append(issues, issue("Use the shared parser", body, "open", login))
	}
	want := forge.FiledIssue{Title: "Use the shared parser", Body: body}

	t.Run("Forgejo command", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		configureForgeCommandFixture(t, state)
		state.mu.Lock()
		state.filedIssues = fixtures("Minos", 50)
		state.mu.Unlock()

		t.Setenv("MINOS_PR", "")
		var stdout bytes.Buffer
		if err := ForgeCommand(t.Context(), []string{"filed-issues", "minos-e2e-owner/subject-plans"}, &stdout); err != nil {
			t.Fatal(err)
		}
		var issues []forge.FiledIssue
		if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
			t.Fatal(err)
		}
		if len(issues) != 1 || issues[0] != want {
			t.Fatalf("filed issues = %#v, want only %#v", issues, want)
		}
	})

	t.Run("GitHub adaptation", func(t *testing.T) {
		state := newGitHubFixtureState(t)
		state.mu.Lock()
		state.issues = fixtures(githubFixtureBotLogin, 100)
		state.mu.Unlock()
		repo := forge.Repository{Owner: "acme", Name: "widgets"}
		issues, err := state.adapter(t).FiledIssues(t.Context(), repo)
		if err != nil || len(issues) != 1 || issues[0] != want {
			t.Fatalf("filed issues = %#v, error = %v, want only %#v", issues, err, want)
		}
		repo.Name = "missing"
		if _, err := state.adapter(t).FiledIssues(t.Context(), repo); err == nil || !strings.Contains(err.Error(), "404") {
			t.Fatalf("missing repository gave %v, want a failed read", err)
		}
	})
}
