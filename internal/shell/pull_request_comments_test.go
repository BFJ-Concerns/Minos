package shell

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

// An author answers a finding held in a review body, which has no thread, in
// the pull request's conversation; these tests read that conversation back
// through each adaptation.

func TestGitHubPullRequestCommentsReadEveryPageOldestFirst(t *testing.T) {
	state := newGitHubFixtureState(t)
	for index := range 101 {
		state.issueComments = append(state.issueComments, map[string]any{
			"id": 1000 + index, "created_at": fmt.Sprintf("2026-11-02T10:%02d:%02dZ", index/60, index%60),
			"user": map[string]any{"login": "colleague"}, "body": fmt.Sprintf("note %d", index),
		})
	}
	state.issueComments = append(state.issueComments,
		map[string]any{"id": 2, "created_at": "2026-11-01T10:00:00Z", "user": map[string]any{"login": "author"}, "body": "Not a defect: `Lost update` cannot happen under the lock."},
		map[string]any{"id": 1, "created_at": "2026-11-01T09:00:00Z", "user": map[string]any{"login": githubFixtureBotLogin}, "body": "Summary."},
	)
	comments, err := state.adapter(t).PullRequestComments(t.Context(), state.guard().Repository, 7)
	if err != nil {
		t.Fatalf("pull-request comments: %v", err)
	}
	if len(comments) != 103 {
		t.Fatalf("read %d comments, want 103", len(comments))
	}
	want := []forge.PullRequestComment{
		{User: githubFixtureBotLogin, Body: "Summary.", CreatedAt: "2026-11-01T09:00:00Z"},
		{User: "author", Body: "Not a defect: `Lost update` cannot happen under the lock.", CreatedAt: "2026-11-01T10:00:00Z"},
		{User: "colleague", Body: "note 0", CreatedAt: "2026-11-02T10:00:00Z"},
	}
	if !reflect.DeepEqual(comments[:3], want) || comments[102].Body != "note 100" {
		t.Fatalf("pull-request comments = %#v ... %#v", comments[:3], comments[102])
	}
}

func forgejoCommentsAdapter(t *testing.T, handler http.HandlerFunc) *forge.Adapter {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	directory, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := forge.NewAdapter(forge.ScriptRunner{Directory: directory, APIBase: server.URL, Credential: "fixture-token"}, "Minos", "Minos")
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestForgejoPullRequestCommentsPageUntilEmptyOldestFirst(t *testing.T) {
	adapter := forgejoCommentsAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/issues/17/comments" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`[{"id":4,"created_at":"2026-11-01T11:00:00Z","user":{"login":"author"},"body":"Rejected: ` + "`b.go:9`" + ` is carried."},{"id":3,"created_at":"2026-11-01T10:00:00Z","user":{"login":"Minos"},"body":"Summary."}]`))
		case "2":
			_, _ = w.Write([]byte(`[{"id":5,"created_at":"2026-11-01T12:00:00Z","user":{"username":"colleague"},"body":null}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
	comments, err := adapter.PullRequestComments(t.Context(), forge.Repository{Owner: "owner", Name: "repo"}, 17)
	if err != nil {
		t.Fatalf("pull-request comments: %v", err)
	}
	want := []forge.PullRequestComment{
		{User: "Minos", Body: "Summary.", CreatedAt: "2026-11-01T10:00:00Z"},
		{User: "author", Body: "Rejected: `b.go:9` is carried.", CreatedAt: "2026-11-01T11:00:00Z"},
		{User: "colleague", Body: "", CreatedAt: "2026-11-01T12:00:00Z"},
	}
	if !reflect.DeepEqual(comments, want) {
		t.Fatalf("pull-request comments = %#v", comments)
	}
}

func TestForgejoPullRequestCommentsReadOnceFromAForgeThatIgnoresPaging(t *testing.T) {
	requests := 0
	adapter := forgejoCommentsAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":3,"created_at":"2026-11-01T10:00:00Z","user":{"login":"author"},"body":"Rejected."}]`))
	})
	comments, err := adapter.PullRequestComments(t.Context(), forge.Repository{Owner: "owner", Name: "repo"}, 17)
	if err != nil {
		t.Fatalf("pull-request comments: %v", err)
	}
	if !reflect.DeepEqual(comments, []forge.PullRequestComment{{User: "author", Body: "Rejected.", CreatedAt: "2026-11-01T10:00:00Z"}}) || requests != 2 {
		t.Fatalf("pull-request comments = %#v after %d requests", comments, requests)
	}
}

func TestForgejoPullRequestCommentsFailOnAMalformedListing(t *testing.T) {
	adapter := forgejoCommentsAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`null`))
	})
	if comments, err := adapter.PullRequestComments(t.Context(), forge.Repository{Owner: "owner", Name: "repo"}, 17); err == nil {
		t.Fatalf("a null listing read as comments: %#v", comments)
	}
}

func TestForgeCommentsPrintsAnEmptyListForAPullRequestWithNoComments(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, facts := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_PR", facts.PR)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)
	var out bytes.Buffer
	if err := ForgeCommand(t.Context(), []string{"comments"}, &out); err != nil {
		t.Fatalf("forge comments: %v", err)
	}
	if !strings.HasPrefix(out.String(), "[]") {
		t.Fatalf("forge comments printed %q", out.String())
	}
}
