package shell

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

// Linux caps a single exec argument at 128 KiB (MAX_ARG_STRLEN), and forge
// payloads pass that in ordinary use: a page of open pull requests with long
// bodies, a review history across many rounds, a review carrying many inline
// findings. These cases drive each adaptation with payloads past the cap and
// require them to arrive intact.
const argumentCap = 128 * 1024

// textOfSize is prose of at least size bytes carrying the characters a shell
// or JSON round trip could mangle: quotes, backslashes, a dollar sign,
// newlines and multibyte text. It ends without a newline: the alert scripts
// drop a body's trailing newlines.
func textOfSize(seed string, size int) string {
	line := seed + ` — "quoted" \backslash\ $HOME 'single' ünïcødé` + "\n"
	return strings.Repeat(line, size/len(line)+1) + seed
}

func TestGitHubAdaptationCarriesPayloadsPastTheArgumentCap(t *testing.T) {
	t.Run("open pull request listing", func(t *testing.T) {
		state := newGitHubFixtureState(t)
		state.mu.Lock()
		for i := 1; i <= 20; i++ {
			state.pullRequests = append(state.pullRequests, map[string]any{
				"number": i, "body": textOfSize(fmt.Sprintf("pull request %d", i), argumentCap/10),
				"head": map[string]any{"sha": fmt.Sprintf("head-%d", i)}, "base": map[string]any{"ref": "main"},
			})
		}
		state.mu.Unlock()
		adaptation := Adaptation{Dir: state.adaptationPath, APIBase: state.server.URL, Credential: state.credential}
		facts, err := adaptation.ListOpenPRs(t.Context(), "github", "acme", "widgets")
		if err != nil {
			t.Fatalf("list open PRs: %v", err)
		}
		if len(facts) != 20 || facts[19].PR != "20" || facts[19].HeadSHA != "head-20" {
			t.Fatalf("open PR facts = %d records, want all 20", len(facts))
		}
	})

	t.Run("snapshot", func(t *testing.T) {
		state := newGitHubFixtureState(t)
		body := textOfSize("pull request body", argumentCap+1)
		reviewBody := textOfSize("earlier round", argumentCap/2)
		state.mu.Lock()
		state.pullRequest["body"] = body
		state.reviews = nil
		for i := range 3 {
			state.reviews = append(state.reviews, map[string]any{"id": 501 + i, "state": "COMMENTED", "commit_id": "feedfacefeedfacefeedfacefeedfacefeedface", "body": reviewBody, "user": map[string]any{"login": githubFixtureBotLogin}})
		}
		state.mu.Unlock()
		snapshot, err := state.adapter(t).Snapshot(t.Context(), forge.Repository{Owner: "acme", Name: "widgets"}, 7)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if snapshot.Body != body || len(snapshot.Reviews) != 3 || snapshot.Reviews[2].Body != reviewBody || len(snapshot.Statuses) != 101 {
			t.Fatalf("snapshot lost content: body intact %t, %d reviews, %d statuses", snapshot.Body == body, len(snapshot.Reviews), len(snapshot.Statuses))
		}
	})

	t.Run("review", func(t *testing.T) {
		state := newGitHubFixtureState(t)
		adapter := state.adapter(t)
		body := textOfSize("review summary", argumentCap+1)
		var comments []forge.ReviewComment
		for line := int64(3); line <= 8; line++ {
			comments = append(comments, forge.ReviewComment{Path: "src/code.txt", Body: textOfSize(fmt.Sprintf("finding on line %d", line), argumentCap/4), NewPosition: line})
		}
		result := adapter.PostReview(t.Context(), state.guard(), forge.ReviewRequestChanges, body, comments)
		if result.Outcome != forge.WriteApplied {
			t.Fatalf("post review = %#v", result)
		}
		writes, payload := state.reviewWriteFacts()
		if writes != 1 || payload["body"] != body || len(payload["comments"].([]any)) != 6 || payload["comments"].([]any)[5].(map[string]any)["body"] != comments[5].Body {
			t.Fatalf("review writes = %d, body intact %t", writes, payload["body"] == body)
		}
		if again := adapter.PostReview(t.Context(), state.guard(), forge.ReviewRequestChanges, body, comments); again.Outcome != forge.WriteApplied || again.Reason != "review already present" {
			t.Fatalf("repeated post = %#v, want the read-back to find the review", again)
		}
	})

	t.Run("filed issue", func(t *testing.T) {
		state := newGitHubFixtureState(t)
		adapter := state.adapter(t)
		body := textOfSize("filed finding", argumentCap+1) + "<!-- minos:entry_1 -->"
		for _, wantWritten := range []int{1, 0} {
			result := adapter.FileIssue(t.Context(), state.guard().Repository, "Finding", body)
			if result.Outcome != forge.WriteApplied || result.Written == nil || *result.Written != wantWritten {
				t.Fatalf("filing = %#v, want applied with written=%d", result, wantWritten)
			}
		}
		if writes := state.operationWrites(); len(writes) != 1 || writes[0].payload["body"] != body {
			t.Fatalf("filing wrote %d requests, body intact %t", len(writes), len(writes) == 1 && writes[0].payload["body"] == body)
		}
	})

	t.Run("alert", func(t *testing.T) {
		state := newGitHubFixtureState(t)
		adapter := state.adapter(t)
		first := textOfSize("first alert", argumentCap+1)
		repeat := textOfSize("repeat alert", argumentCap+1)
		githubApplied(t, adapter.Alert(t.Context(), state.guard().Repository, "Sweep failed", first))
		githubApplied(t, adapter.Alert(t.Context(), state.guard().Repository, "Sweep failed", repeat))
		writes := state.operationWrites()
		if len(writes) != 2 || writes[0].payload["body"] != first || !strings.HasSuffix(writes[1].path, "/comments") || writes[1].payload["body"] != repeat {
			t.Fatalf("alert writes = %d, want an issue then a comment with both bodies intact", len(writes))
		}
	})
}

func TestForgejoAdaptationCarriesPayloadsPastTheArgumentCap(t *testing.T) {
	forgejoAdapter := func(t *testing.T, state *forgejoFixtureState) *forge.Adapter {
		t.Helper()
		adapter, err := forge.NewAdapter(forge.ScriptRunner{Directory: state.adaptationPath, APIBase: state.server.URL, Credential: "fixture-token"}, "Minos", "Minos")
		if err != nil {
			t.Fatal(err)
		}
		return adapter
	}
	subject := forge.Repository{Owner: "minos-e2e-owner", Name: "subject"}

	t.Run("snapshot", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		body := textOfSize("pull request body", argumentCap+1)
		reviewBody := textOfSize("earlier round", argumentCap/2)
		state.changePullRequest(func(pullRequest map[string]any) { pullRequest["body"] = body })
		var reviews []map[string]any
		for i := range 3 {
			reviews = append(reviews, map[string]any{"id": float64(i + 1), "state": "COMMENT", "commit_id": state.headSHA(), "body": reviewBody, "user": map[string]any{"login": "Minos"}})
		}
		state.setReviews(reviews)
		state.mu.Lock()
		number := int64(state.pullRequest["number"].(float64))
		state.mu.Unlock()
		snapshot, err := forgejoAdapter(t, state).Snapshot(t.Context(), subject, number)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if snapshot.Body != body || len(snapshot.Reviews) != 3 || snapshot.Reviews[2].Body != reviewBody {
			t.Fatalf("snapshot lost content: body intact %t, %d reviews", snapshot.Body == body, len(snapshot.Reviews))
		}
	})

	t.Run("review", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.setDiffNewSide(map[string][][2]int64{"src/code.go": {{1, 10}}})
		adapter := forgejoAdapter(t, state)
		state.mu.Lock()
		guard := forge.Guard{Repository: subject, PullRequest: int64(state.pullRequest["number"].(float64))}
		state.mu.Unlock()
		guard.HeadSHA, guard.TargetSHA = state.headSHA(), state.targetSHA()
		body := textOfSize("review summary", argumentCap+1)
		var comments []forge.ReviewComment
		for line := int64(1); line <= 6; line++ {
			comments = append(comments, forge.ReviewComment{Path: "src/code.go", Body: textOfSize(fmt.Sprintf("finding on line %d", line), argumentCap/4), NewPosition: line})
		}
		result := adapter.PostReview(t.Context(), guard, forge.ReviewRequestChanges, body, comments)
		if result.Outcome != forge.WriteApplied {
			t.Fatalf("post review = %#v", result)
		}
		state.mu.Lock()
		payloads := state.reviewPayloads
		state.mu.Unlock()
		if len(payloads) != 1 || payloads[0]["body"] != body || payloads[0]["comments"].([]any)[5].(map[string]any)["body"] != comments[5].Body {
			t.Fatalf("review writes = %d, body intact %t", len(payloads), len(payloads) == 1 && payloads[0]["body"] == body)
		}
	})

	t.Run("filed issue", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		adapter := forgejoAdapter(t, state)
		body := textOfSize("filed finding", argumentCap+1) + "<!-- minos:entry_1 -->"
		plans := forge.Repository{Owner: "minos-e2e-owner", Name: "subject-plans"}
		for _, wantWritten := range []int{1, 0} {
			result := adapter.FileIssue(t.Context(), plans, "Finding", body)
			if result.Outcome != forge.WriteApplied || result.Written == nil || *result.Written != wantWritten {
				t.Fatalf("filing = %#v, want applied with written=%d", result, wantWritten)
			}
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if len(state.issueCreateRequests) != 1 || state.issueCreateRequests[0]["body"] != body {
			t.Fatalf("filing wrote %d requests", len(state.issueCreateRequests))
		}
	})

	t.Run("alert", func(t *testing.T) {
		// The open-issue listing the alert searches for its title is itself
		// past the cap, and so is the alert's body.
		var listing []map[string]any
		for i := 1; i <= 20; i++ {
			listing = append(listing, map[string]any{"number": i, "title": fmt.Sprintf("unrelated %d", i), "body": textOfSize("unrelated", argumentCap/10)})
		}
		var mu sync.Mutex
		var created []map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/issues":
				page := listing
				if r.URL.Query().Get("page") != "1" {
					page = []map[string]any{}
				}
				json.NewEncoder(w).Encode(page)
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/owner/repo/issues":
				var payload map[string]any
				raw, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Errorf("issue payload is not JSON: %v", err)
				}
				mu.Lock()
				created = append(created, payload)
				mu.Unlock()
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, `{"number":21}`)
			default:
				http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			}
		}))
		defer server.Close()
		script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "alert"))
		if err != nil {
			t.Fatal(err)
		}
		body := textOfSize("operator alert", argumentCap+1)
		input, _ := json.Marshal(map[string]string{"title": "Sweep failed", "body": body})
		cmd := exec.CommandContext(t.Context(), script, "owner", "repo")
		cmd.Env = append(os.Environ(), "MINOS_API_BASE="+server.URL, "MINOS_FORGE_CREDENTIAL=fixture-token")
		cmd.Stdin = strings.NewReader(string(input))
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("alert: %v; stdout = %s", err, output)
		}
		var result map[string]any
		if err := json.Unmarshal(output, &result); err != nil || result["outcome"] != "applied" {
			t.Fatalf("alert result = %s (%v), want applied", output, err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(created) != 1 || created[0]["title"] != "Sweep failed" || created[0]["body"] != body {
			t.Fatalf("alert created %d issues, body intact %t", len(created), len(created) == 1 && created[0]["body"] == body)
		}
	})
}
