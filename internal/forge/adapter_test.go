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
