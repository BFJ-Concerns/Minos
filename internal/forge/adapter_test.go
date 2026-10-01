package forge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"bfj/minos/internal/product"
)

func TestSnapshotNormalisesEmptyDependenciesForEncoding(t *testing.T) {
	runner := &recordingRunner{outputs: [][]byte{[]byte(`{
		"authenticated_user":"Minos",
		"repository":"owner/repo",
		"target_repository":"owner/repo",
		"pull_request":17,
		"author":"author",
		"head_sha":"head",
		"target_sha":"target",
		"target_branch":"main",
		"dependencies_available":true
	}`)}, errors: []error{nil}}
	adapter, err := NewAdapter(runner, "Minos", "Minos")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := adapter.Snapshot(t.Context(), Repository{Owner: "owner", Name: "repo"}, 17)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.OpenDependencies == nil {
		t.Fatal("OpenDependencies is nil")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"open_dependencies":[]`) {
		t.Fatalf("snapshot JSON = %s", encoded)
	}
}

func TestSetProductStatusRejectsInvalidState(t *testing.T) {
	runner := &recordingRunner{}
	adapter, err := NewAdapter(runner, "Minos", "Minos")
	if err != nil {
		t.Fatal(err)
	}
	result := adapter.SetProductStatus(t.Context(), Guard{}, product.State{})
	if result.Outcome != WriteRejected || !strings.Contains(result.Reason, "invalid product state") {
		t.Fatalf("result = %#v", result)
	}
	if len(runner.requests) != 0 {
		t.Fatal("invalid state reached forge runner")
	}
}

func TestIncompleteStatusUsesGuardedPullRequestAndTargetIdentity(t *testing.T) {
	runner := &recordingRunner{
		outputs: [][]byte{[]byte(`{"outcome":"applied"}`)},
		errors:  []error{nil},
	}
	adapter, err := NewAdapter(runner, "Minos", "Minos")
	if err != nil {
		t.Fatal(err)
	}
	guard := Guard{
		Repository:  Repository{Owner: "owner", Name: "repo"},
		PullRequest: 17,
		HeadSHA:     "head-sha",
		TargetSHA:   "target-sha",
	}
	result := adapter.SetProductStatus(t.Context(), guard, product.Incomplete())
	if result.Outcome != WriteApplied {
		t.Fatalf("result = %#v", result)
	}
	request := runner.requests[0]
	if request.Operation != "guarded-set-status" {
		t.Fatalf("operation = %q, want guarded-set-status", request.Operation)
	}
	want := []string{
		"owner", "repo", "17", "head-sha", "target-sha", "Minos",
		"Minos", "error", "Review incomplete",
	}
	if !slices.Equal(request.Arguments, want) {
		t.Fatalf("arguments = %v, want %v", request.Arguments, want)
	}
}

func TestClaimUsesServiceIdentity(t *testing.T) {
	runner := &recordingRunner{
		outputs: [][]byte{[]byte(`{"outcome":"applied"}`)},
		errors:  []error{nil},
	}
	adapter, err := NewAdapter(runner, "Minos", "Minos")
	if err != nil {
		t.Fatal(err)
	}
	result := adapter.Claim(t.Context(), Repository{Owner: "owner", Name: "repo"}, 17)
	if result.Outcome != WriteApplied {
		t.Fatalf("result = %#v", result)
	}
	request := runner.requests[0]
	if request.Operation != "claim" {
		t.Fatalf("operation = %q, want claim", request.Operation)
	}
	want := []string{"owner", "repo", "17", "Minos"}
	if !slices.Equal(request.Arguments, want) {
		t.Fatalf("arguments = %v, want %v", request.Arguments, want)
	}
}

func TestReactionUsesGuardedPullRequestIdentity(t *testing.T) {
	runner := &recordingRunner{
		outputs: [][]byte{[]byte(`{"outcome":"applied"}`)},
		errors:  []error{nil},
	}
	adapter, err := NewAdapter(runner, "Minos", "Minos")
	if err != nil {
		t.Fatal(err)
	}
	guard := Guard{
		Repository:  Repository{Owner: "owner", Name: "repo"},
		PullRequest: 17,
		HeadSHA:     "head-sha",
		TargetSHA:   "target-sha",
	}
	result := adapter.AddReaction(t.Context(), guard, "+1")
	if result.Outcome != WriteApplied {
		t.Fatalf("result = %#v", result)
	}
	request := runner.requests[0]
	if request.Operation != "guarded-add-reaction" {
		t.Fatalf("operation = %q, want guarded-add-reaction", request.Operation)
	}
	want := []string{"owner", "repo", "17", "head-sha", "target-sha", "Minos", "+1"}
	if !slices.Equal(request.Arguments, want) {
		t.Fatalf("arguments = %v, want %v", request.Arguments, want)
	}
}

func TestRemoveReactionUsesGuardedPullRequestIdentity(t *testing.T) {
	guard := Guard{
		Repository: Repository{Owner: "owner", Name: "repo"}, PullRequest: 17,
		HeadSHA: "head-sha", TargetSHA: "target-sha",
	}
	runner := &recordingRunner{outputs: [][]byte{[]byte(`{"outcome":"applied"}`)}, errors: []error{nil}}
	adapter, err := NewAdapter(runner, "Minos", "Minos")
	if err != nil {
		t.Fatal(err)
	}
	if result := adapter.RemoveReaction(t.Context(), guard, "eyes"); result.Outcome != WriteApplied {
		t.Fatalf("result = %#v", result)
	}
	request := runner.requests[0]
	if request.Operation != "guarded-remove-reaction" {
		t.Fatalf("operation = %q, want guarded-remove-reaction", request.Operation)
	}
	want := []string{"owner", "repo", "17", "head-sha", "target-sha", "Minos", "eyes"}
	if !slices.Equal(request.Arguments, want) {
		t.Fatalf("arguments = %v, want %v", request.Arguments, want)
	}
}

// Both operations must carry the caller's title and multiline body, while
// preserving their distinct operation name and service-login arguments.
func TestIssueWritesCarryPayloadAndOperation(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments []string
		call      func(*Adapter, context.Context, Repository, string, string) WriteResult
	}{
		{name: "alert", arguments: []string{"owner", "repo"}, call: (*Adapter).Alert},
		{name: "guarded-file-issue", arguments: []string{"owner", "repo", "Minos"}, call: (*Adapter).FileIssue},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{outputs: [][]byte{[]byte(`{"outcome":"applied"}`)}, errors: []error{nil}}
			adapter, err := NewAdapter(runner, "Minos")
			if err != nil {
				t.Fatal(err)
			}
			title := `Advisory "write" failure`
			body := "Two writers read the same revision.\nThe later update wins."
			test.call(adapter, t.Context(), Repository{Owner: "owner", Name: "repo"}, title, body)
			if len(runner.requests) != 1 {
				t.Fatalf("requests = %v, want one", runner.requests)
			}
			request := runner.requests[0]
			if request.Operation != test.name || !slices.Equal(request.Arguments, test.arguments) {
				t.Fatalf("operation=%q arguments=%v", request.Operation, request.Arguments)
			}
			payload, err := io.ReadAll(request.Stdin)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]string
			if err := json.Unmarshal(payload, &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got["title"] != title || got["body"] != body {
				t.Fatalf("issue payload = %s, want title=%q body=%q", payload, title, body)
			}
		})
	}
}

type recordingRunner struct {
	requests []RunRequest
	outputs  [][]byte
	errors   []error
}

func (r *recordingRunner) Run(_ context.Context, request RunRequest) ([]byte, error) {
	r.requests = append(r.requests, request)
	if len(r.outputs) == 0 {
		return nil, errors.New("unexpected runner call")
	}
	out, err := r.outputs[0], r.errors[0]
	r.outputs, r.errors = r.outputs[1:], r.errors[1:]
	return out, err
}
