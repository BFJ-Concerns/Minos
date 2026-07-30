package forge

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"bfj/minos/internal/product"
)

func TestSetProductStatusRejectsInvalidState(t *testing.T) {
	runner := &recordingRunner{}
	adapter, err := NewAdapter(runner, "Minos")
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
	adapter, err := NewAdapter(runner, "Minos")
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
		OwnedStatusContext, "error", "Review incomplete",
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
	adapter, err := NewAdapter(runner, "Minos")
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

func TestCheckLogsUsesGuardedPullRequestIdentity(t *testing.T) {
	runner := &recordingRunner{
		outputs: [][]byte{[]byte(`{"head_sha":"head-sha","runs":[]}`)},
		errors:  []error{nil},
	}
	adapter, err := NewAdapter(runner, "Minos")
	if err != nil {
		t.Fatal(err)
	}
	guard := Guard{
		Repository:  Repository{Owner: "owner", Name: "repo"},
		PullRequest: 17,
		HeadSHA:     "head-sha",
		TargetSHA:   "target-sha",
	}
	evidence, err := adapter.CheckLogs(t.Context(), guard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(evidence), `"head_sha":"head-sha"`) {
		t.Fatalf("evidence = %s", evidence)
	}
	request := runner.requests[0]
	if request.Operation != "guarded-check-logs" {
		t.Fatalf("operation = %q, want guarded-check-logs", request.Operation)
	}
	want := []string{"owner", "repo", "17", "head-sha", "target-sha", "Minos"}
	if !slices.Equal(request.Arguments, want) {
		t.Fatalf("arguments = %v, want %v", request.Arguments, want)
	}
}

func TestReactionUsesGuardedPullRequestIdentity(t *testing.T) {
	runner := &recordingRunner{
		outputs: [][]byte{[]byte(`{"outcome":"applied"}`)},
		errors:  []error{nil},
	}
	adapter, err := NewAdapter(runner, "Minos")
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

func TestFinishingWritesUseGuardedPullRequestIdentity(t *testing.T) {
	guard := Guard{
		Repository: Repository{Owner: "owner", Name: "repo"}, PullRequest: 17,
		HeadSHA: "head-sha", TargetSHA: "target-sha",
	}
	tests := []struct {
		name      string
		operation string
		argument  string
		invoke    func(*Adapter) WriteResult
	}{
		{"remove reaction", "guarded-remove-reaction", "eyes", func(adapter *Adapter) WriteResult {
			return adapter.RemoveReaction(t.Context(), guard, "eyes")
		}},
		{"remove label", "guarded-remove-label", "Flaky Test", func(adapter *Adapter) WriteResult {
			return adapter.RemoveLabel(t.Context(), guard, "Flaky Test")
		}},
		{"delete source branch", "guarded-delete-source-branch", "feature", func(adapter *Adapter) WriteResult {
			return adapter.DeleteSourceBranch(t.Context(), guard, "feature")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{outputs: [][]byte{[]byte(`{"outcome":"applied"}`)}, errors: []error{nil}}
			adapter, err := NewAdapter(runner, "Minos")
			if err != nil {
				t.Fatal(err)
			}
			if result := test.invoke(adapter); result.Outcome != WriteApplied {
				t.Fatalf("result = %#v", result)
			}
			request := runner.requests[0]
			if request.Operation != test.operation {
				t.Fatalf("operation = %q, want %q", request.Operation, test.operation)
			}
			want := []string{"owner", "repo", "17", "head-sha", "target-sha", "Minos", test.argument}
			if !slices.Equal(request.Arguments, want) {
				t.Fatalf("arguments = %v, want %v", request.Arguments, want)
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
