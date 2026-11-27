package shell

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
)

// summaryForge is a Forgejo that serves what the summary comment's read and
// guarded write ask of it: the authenticated user, the pull request and its
// conversation comments, which it lets a caller add to and edit.
type summaryForge struct {
	mu       sync.Mutex
	head     string
	comments []map[string]any
	posts    int
	edits    int
}

func (f *summaryForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	const comments = "/api/v1/repos/owner/repo/issues/17/comments"
	const comment = "/api/v1/repos/owner/repo/issues/comments/"
	w.Header().Set("Content-Type", "application/json")
	var payload struct {
		Body string `json:"body"`
	}
	if r.Method != http.MethodGet {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad JSON", http.StatusBadRequest)
			return
		}
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user":
		_, _ = w.Write([]byte(`{"login":"Minos"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/pulls/17":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 17, "state": "open", "merged": false,
			"head": map[string]any{"sha": f.head},
			"base": map[string]any{"repo": map[string]any{"full_name": "owner/repo"}},
		})
	case r.Method == http.MethodGet && r.URL.Path == comments:
		if r.URL.Query().Get("page") != "1" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_ = json.NewEncoder(w).Encode(append([]map[string]any{}, f.comments...))
	case r.Method == http.MethodPost && r.URL.Path == comments:
		f.posts++
		f.comments = append(f.comments, map[string]any{"id": 100 + len(f.comments), "user": map[string]any{"login": "Minos"}, "body": payload.Body})
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, comment):
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, comment))
		for _, held := range f.comments {
			if held["id"] == id {
				f.edits++
				held["body"] = payload.Body
				_, _ = w.Write([]byte(`{}`))
				return
			}
		}
		http.NotFound(w, r)
	default:
		http.Error(w, fmt.Sprintf("unexpected request %s %s", r.Method, r.URL), http.StatusNotFound)
	}
}

func TestForgejoSummaryCommentIsCreatedOnceThenEditedInPlace(t *testing.T) {
	const first, second = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	fixture := &summaryForge{head: first, comments: []map[string]any{
		// Neither is the service's summary comment: one is not the
		// service's, the other does not open with the record.
		{"id": 7, "user": map[string]any{"login": "author"}, "body": product.SummaryOpening + " head=" + first + " target=target state=clean -->\nQuoted."},
		{"id": 8, "user": map[string]any{"login": "Minos"}, "body": "A note.\n" + product.SummaryOpening + " head=" + first + " target=target state=clean -->"},
	}}
	adapter := forgejoCommentsAdapter(t, fixture.ServeHTTP)
	guard := forge.Guard{Repository: forge.Repository{Owner: "owner", Name: "repo"}, PullRequest: 17, HeadSHA: first, TargetSHA: "target"}
	write := func(state product.SummaryState, text string) forge.WriteResult {
		t.Helper()
		result, err := writeSummary(t.Context(), adapter, guard, state, text)
		if err != nil {
			t.Fatalf("write %s: %v", state, err)
		}
		return result
	}
	held := func() (product.SummaryRecord, string) {
		t.Helper()
		comment, err := adapter.SummaryComment(t.Context(), guard.Repository, guard.PullRequest)
		if err != nil || comment == nil {
			t.Fatalf("summary comment = %#v, %v", comment, err)
		}
		record, found := product.ParseSummary(comment.Body)
		if !found {
			t.Fatalf("summary comment does not open with a record: %q", comment.Body)
		}
		_, text, _ := strings.Cut(comment.Body, "\n")
		return record, text
	}

	if comment, err := adapter.SummaryComment(t.Context(), guard.Repository, guard.PullRequest); err != nil || comment != nil {
		t.Fatalf("a pull request with no summary comment read as %#v, %v", comment, err)
	}
	if result := write(product.SummaryInspecting, "Inspecting."); result.Outcome != forge.WriteApplied {
		t.Fatalf("first write = %#v", result)
	}
	if result := write(product.SummaryClean, "Clean.\n\nNothing blocks.\n"); result.Outcome != forge.WriteApplied {
		t.Fatalf("second write = %#v", result)
	}
	if result := write(product.SummaryClean, "Clean.\n\nNothing blocks.\n"); result.Outcome != forge.WriteApplied || result.Reason != "summary comment already current" {
		t.Fatalf("repeated write = %#v", result)
	}
	if record, text := held(); record != (product.SummaryRecord{Head: first, Target: "target", State: product.SummaryClean}) || text != "Clean.\n\nNothing blocks." {
		t.Fatalf("held = %#v, %q", record, text)
	}
	if fixture.posts != 1 || fixture.edits != 1 || len(fixture.comments) != 3 {
		t.Fatalf("%d posts and %d edits left %d comments, want one post and one edit", fixture.posts, fixture.edits, len(fixture.comments))
	}

	// The head moves: a write for the head that was is refused and leaves
	// the comment alone, and the next head's claim carries the finished
	// head forward.
	fixture.head = second
	if result := write(product.SummaryIncomplete, "Incomplete."); result.Outcome != forge.WriteRejected || result.Reason != "head moved" {
		t.Fatalf("write for a moved head = %#v", result)
	}
	guard.HeadSHA = second
	if result := write(product.SummaryInspecting, "Inspecting."); result.Outcome != forge.WriteApplied {
		t.Fatalf("next head's write = %#v", result)
	}
	want := product.SummaryRecord{Head: second, Target: "target", State: product.SummaryInspecting, FinishedHead: first, FinishedState: product.SummaryClean}
	if record, _ := held(); record != want {
		t.Fatalf("held = %#v, want %#v", record, want)
	}
	if fixture.posts != 1 || fixture.edits != 2 {
		t.Fatalf("%d posts and %d edits, want the one comment edited again", fixture.posts, fixture.edits)
	}
}

func TestWriteSummaryRefusesARecordForAnotherHead(t *testing.T) {
	adapter := forgejoCommentsAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
	})
	guard := forge.Guard{Repository: forge.Repository{Owner: "owner", Name: "repo"}, PullRequest: 17, HeadSHA: "head", TargetSHA: "target"}
	for name, body := range map[string]string{
		"no record":               "Clean.",
		"a record for other head": product.SummaryOpening + " head=other target=target state=clean -->\nClean.",
	} {
		if result := adapter.WriteSummary(t.Context(), guard, body); result.Outcome != forge.WriteRejected {
			t.Fatalf("%s = %#v", name, result)
		}
	}
}

func TestHeadCompletionReadsTheSummaryCommentThenTheReviewAndStatus(t *testing.T) {
	summary := func(head string, state product.SummaryState) *forge.SummaryComment {
		body, err := product.ComposeSummary(product.SummaryRecord{Head: head, Target: "target", State: state}, "Text.")
		if err != nil {
			t.Fatal(err)
		}
		return &forge.SummaryComment{ID: 9, Body: body}
	}
	requestChanges := []forge.Review{{ID: 3, User: "Minos", CommitID: "head", State: "REQUEST_CHANGES"}}
	cleanStatus := []forge.Status{{ID: 4, Context: "Minos", Creator: "Minos", State: forge.StatusSuccess, Description: product.Clean().Description()}}
	tests := []struct {
		name      string
		snapshot  forge.Snapshot
		want      product.State
		completed bool
	}{
		{name: "a clean summary of this head", snapshot: forge.Snapshot{Summary: summary("head", product.SummaryClean)}, want: product.Clean(), completed: true},
		{name: "changes requested by the summary alone", snapshot: forge.Snapshot{Summary: summary("head", product.SummaryChangesRequested)}, want: product.Attention(), completed: true},
		{name: "an incomplete summary", snapshot: forge.Snapshot{Summary: summary("head", product.SummaryIncomplete)}},
		{name: "a clean summary of the head that was", snapshot: forge.Snapshot{Summary: summary("earlier", product.SummaryClean)}},
		{name: "a finished summary over an older review of the head", snapshot: forge.Snapshot{Summary: summary("head", product.SummaryClean), Reviews: requestChanges}, want: product.Clean(), completed: true},
		{name: "a review of the head and no summary", snapshot: forge.Snapshot{Reviews: requestChanges}, want: product.Attention(), completed: true},
		{name: "a status on the head and no summary", snapshot: forge.Snapshot{Statuses: cleanStatus}, want: product.Clean(), completed: true},
		{name: "a status on the head under a summary of the head that was", snapshot: forge.Snapshot{Summary: summary("earlier", product.SummaryChangesRequested), Statuses: cleanStatus}, want: product.Clean(), completed: true},
		{name: "a comment whose record is gone", snapshot: forge.Snapshot{Summary: &forge.SummaryComment{ID: 9, Body: "Edited by hand."}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.snapshot.HeadSHA = "head"
			if got, completed := headCompletion(test.snapshot, "Minos", "Minos"); got != test.want || completed != test.completed {
				t.Fatalf("headCompletion = (%#v, %t), want (%#v, %t)", got, completed, test.want, test.completed)
			}
		})
	}
}

// Each adaptation finds the summary comment by the record's opening, which
// the product package owns; the scripts state it once each, in common.sh.
func TestAdaptationsFindTheSummaryCommentByTheRecordsOpening(t *testing.T) {
	for _, adaptation := range []string{"forgejo", "github"} {
		script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "adaptations", adaptation, "common.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if want := "summary_opening='" + product.SummaryOpening + "'\n"; !strings.Contains(string(script), want) {
			t.Fatalf("%s/common.sh does not set %q", adaptation, strings.TrimSpace(want))
		}
	}
}

func TestGitHubSummaryCommentIsCreatedOnceEditedInPlaceAndReadByTheSnapshot(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter := state.adapter(t)
	guard := state.guard()
	if snapshot, err := adapter.Snapshot(t.Context(), guard.Repository, guard.PullRequest); err != nil || snapshot.Summary != nil {
		t.Fatalf("a pull request with no summary comment: summary %#v, %v", snapshot.Summary, err)
	}
	for _, state := range []product.SummaryState{product.SummaryInspecting, product.SummaryChangesRequested} {
		if result, err := writeSummary(t.Context(), adapter, guard, state, "Text for "+string(state)+"."); err != nil || result.Outcome != forge.WriteApplied {
			t.Fatalf("write %s = %#v, %v", state, result, err)
		}
	}
	var methods []string
	for _, write := range state.writes {
		methods = append(methods, write.method)
	}
	if len(state.issueComments) != 1 || strings.Join(methods, " ") != "POST PATCH" {
		t.Fatalf("%v left %d comments, want one comment posted and then edited", methods, len(state.issueComments))
	}
	snapshot, err := adapter.Snapshot(t.Context(), guard.Repository, guard.PullRequest)
	if err != nil {
		t.Fatal(err)
	}
	record, found := snapshot.SummaryRecord()
	if want := (product.SummaryRecord{Head: guard.HeadSHA, Target: guard.TargetSHA, State: product.SummaryChangesRequested}); !found || record != want {
		t.Fatalf("snapshot's summary record = (%#v, %t), want %#v", record, found, want)
	}
	if completion, completed := headCompletion(snapshot, githubFixtureBotLogin, "Minos"); !completed || completion != product.Attention() {
		t.Fatalf("head completion = (%#v, %t)", completion, completed)
	}
	state.moveHead("9999999999999999999999999999999999999999")
	moved, err := adapter.Snapshot(t.Context(), guard.Repository, guard.PullRequest)
	if err != nil {
		t.Fatal(err)
	}
	if completion, completed := headCompletion(moved, githubFixtureBotLogin, "Minos"); completed {
		t.Fatalf("a moved head still reads as completed: %#v", completion)
	}
}
