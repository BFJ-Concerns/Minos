package shell

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

func TestGitHubReviewThreadsCarryResolutionAndRepliesForTheServicesOwnThreads(t *testing.T) {
	state := newGitHubFixtureState(t)
	state.reviewThreadPages = []string{
		`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"},"nodes":[
			{"isResolved":true,"isOutdated":false,"path":"src/code.txt","line":4,"originalLine":4,"comments":{"nodes":[
				{"body":"**Blocking · High: Lost update**","author":{"__typename":"Bot","login":"minos-review"},"originalCommit":{"oid":"old-head"},"pullRequestReview":{"databaseId":601}},
				{"body":"Fixed in the next push.","author":{"__typename":"User","login":"author"},"originalCommit":{"oid":"old-head"},"pullRequestReview":{"databaseId":602}}]}},
			{"isResolved":false,"isOutdated":false,"path":"src/code.txt","line":8,"originalLine":8,"comments":{"nodes":[
				{"body":"A colleague's thread.","author":{"__typename":"User","login":"colleague"},"originalCommit":{"oid":"old-head"},"pullRequestReview":{"databaseId":501}}]}}]}}}}}`,
		`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[
			{"isResolved":false,"isOutdated":true,"path":"src/other.txt","line":null,"originalLine":12,"comments":{"nodes":[
				{"body":"**Blocking · High: Remainder dropped**","author":{"__typename":"Bot","login":"minos-review"},"originalCommit":{"oid":"old-head"},"pullRequestReview":{"databaseId":601}}]}}]}}}}}`,
	}
	threads, err := state.adapter(t).ReviewThreads(t.Context(), state.guard().Repository, 7)
	if err != nil {
		t.Fatalf("review threads: %v", err)
	}
	want := []forge.ReviewThread{
		{Path: "src/code.txt", Line: 4, Resolved: true, Comments: []forge.ReviewThreadComment{
			{User: githubFixtureBotLogin, Body: "**Blocking · High: Lost update**", CommitID: "old-head", ReviewID: 601},
			{User: "author", Body: "Fixed in the next push.", CommitID: "old-head", ReviewID: 602},
		}},
		{Path: "src/other.txt", Line: 12, Outdated: true, Comments: []forge.ReviewThreadComment{
			{User: githubFixtureBotLogin, Body: "**Blocking · High: Remainder dropped**", CommitID: "old-head", ReviewID: 601},
		}},
	}
	if !reflect.DeepEqual(threads, want) {
		t.Fatalf("review threads = %#v", threads)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.graphQLVariables) != 2 || state.graphQLVariables[0]["after"] != nil || state.graphQLVariables[1]["after"] != "cursor-1" ||
		state.graphQLVariables[0]["owner"] != "acme" || state.graphQLVariables[0]["repo"] != "widgets" || state.graphQLVariables[0]["pr"] != float64(7) {
		t.Fatalf("review-thread queries asked for %#v", state.graphQLVariables)
	}
}

func TestGitHubReviewThreadsFailOnAGraphQLError(t *testing.T) {
	state := newGitHubFixtureState(t)
	state.reviewThreadPages = []string{`{"data":null,"errors":[{"message":"Resource not accessible by integration"}]}`}
	if threads, err := state.adapter(t).ReviewThreads(t.Context(), state.guard().Repository, 7); err == nil {
		t.Fatalf("a GraphQL error read as threads: %#v", threads)
	}
}

func TestForgejoReviewThreadsGroupCommentsBySiteAndReadTheResolver(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := "[]"
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/pulls/17/reviews":
			if r.URL.Query().Get("page") == "1" {
				response = `[{"id":31,"comments_count":2,"user":{"login":"Minos"}},{"id":32,"comments_count":0,"user":{"login":"Minos"}},{"id":33,"comments_count":1,"user":{"login":"author"}},{"id":34,"comments_count":1,"user":{"login":"colleague"}}]`
			}
		case "/api/v1/repos/owner/repo/pulls/17/reviews/31/comments":
			response = `[{"id":1,"path":"a.go","position":4,"original_position":0,"body":"**Blocking · High: Lost update**","user":{"login":"Minos"},"resolver":{"login":"author"},"original_commit_id":"old-head","commit_id":"new-head","pull_request_review_id":31,"created_at":"2026-11-01T10:00:00Z"},
				{"id":2,"path":"b.go","position":0,"original_position":9,"body":"**Blocking · High: Remainder dropped**","user":{"login":"Minos"},"resolver":null,"commit_id":"old-head","pull_request_review_id":31,"created_at":"2026-11-01T10:00:00Z"}]`
		case "/api/v1/repos/owner/repo/pulls/17/reviews/33/comments":
			response = `[{"id":5,"path":"b.go","position":0,"original_position":9,"body":"Not a defect: the remainder is carried.","user":{"login":"author"},"resolver":null,"commit_id":"old-head","pull_request_review_id":33,"created_at":"2026-11-01T11:00:00Z"}]`
		case "/api/v1/repos/owner/repo/pulls/17/reviews/34/comments":
			response = `[{"id":7,"path":"c.go","position":2,"body":"A colleague's thread.","user":{"login":"colleague"},"resolver":null,"commit_id":"old-head","pull_request_review_id":34,"created_at":"2026-11-01T09:00:00Z"}]`
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	directory, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := forge.NewAdapter(forge.ScriptRunner{Directory: directory, APIBase: server.URL, Credential: "fixture-token"}, "Minos", "Minos")
	if err != nil {
		t.Fatal(err)
	}
	threads, err := adapter.ReviewThreads(t.Context(), forge.Repository{Owner: "owner", Name: "repo"}, 17)
	if err != nil {
		t.Fatalf("review threads: %v", err)
	}
	want := []forge.ReviewThread{
		{Path: "a.go", Line: 4, Resolved: true, Comments: []forge.ReviewThreadComment{
			{User: "Minos", Body: "**Blocking · High: Lost update**", CommitID: "old-head", ReviewID: 31},
		}},
		{Path: "b.go", Line: 9, Comments: []forge.ReviewThreadComment{
			{User: "Minos", Body: "**Blocking · High: Remainder dropped**", CommitID: "old-head", ReviewID: 31},
			{User: "author", Body: "Not a defect: the remainder is carried.", CommitID: "old-head", ReviewID: 33},
		}},
	}
	if !reflect.DeepEqual(threads, want) {
		t.Fatalf("review threads = %#v", threads)
	}
}

func TestForgeThreadsPrintsAnEmptyListForAPullRequestWithNoThreads(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, facts := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_PR", facts.PR)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)
	var out bytes.Buffer
	if err := ForgeCommand(t.Context(), []string{"threads"}, &out); err != nil {
		t.Fatalf("forge threads: %v", err)
	}
	var threads []forge.ReviewThread
	if err := json.Unmarshal(out.Bytes(), &threads); err != nil || !strings.HasPrefix(out.String(), "[]") {
		t.Fatalf("forge threads printed %q (%v)", out.String(), err)
	}
}
