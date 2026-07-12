package forge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bfj/minos/internal/product"
)

type recordingOwnership struct {
	err   error
	calls int
}

func TestDefaultMergeRetryBudgetIsSecondsScaleAndBounded(t *testing.T) {
	t.Parallel()

	adapter, err := NewAdapter(&recordingRunner{}, &recordingOwnership{}, "Minos")
	if err != nil {
		t.Fatal(err)
	}
	if len(adapter.mergeRetryDelays) != 7 {
		t.Fatalf("merge retry delays = %d, want 7", len(adapter.mergeRetryDelays))
	}
	var budget time.Duration
	for index, delay := range adapter.mergeRetryDelays {
		budget += delay
		if index > 0 && delay <= adapter.mergeRetryDelays[index-1] {
			t.Fatalf("merge retry delay %d = %s, want increasing backoff", index, delay)
		}
	}
	if budget != 29*time.Second+750*time.Millisecond {
		t.Fatalf("merge retry budget = %s, want 29.75s", budget)
	}
}

func (o *recordingOwnership) Check(context.Context, Ownership) error {
	o.calls++
	return o.err
}

func TestAdapterRejectsMutationBeforeRunnerWhenOwnershipIsLost(t *testing.T) {
	t.Parallel()

	ownership := &recordingOwnership{err: errors.New("lease lost")}
	runner := &recordingRunner{}
	adapter, err := NewAdapter(runner, ownership, "Minos")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}

	result := adapter.SetProductStatus(t.Context(), Guard{
		Ownership:   LifecycleOwnership(7),
		Repository:  Repository{Owner: "acme", Name: "widget"},
		PullRequest: 4,
		HeadSHA:     "head",
		TargetSHA:   "target",
	}, product.Working())

	if result.Outcome != WriteRejected || result.Reason != "ownership: lease lost" {
		t.Fatalf("SetProductStatus() = %#v, want ownership rejection", result)
	}
	if ownership.calls != 1 {
		t.Fatalf("ownership checks = %d, want 1", ownership.calls)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("runner received %d requests after ownership rejection", len(runner.requests))
	}
}

func TestSetProductStatusRejectsInvalidStateBeforeOwnershipOrRunner(t *testing.T) {
	t.Parallel()

	ownership := &recordingOwnership{}
	runner := &recordingRunner{}
	adapter, err := NewAdapter(runner, ownership, "Minos")
	if err != nil {
		t.Fatal(err)
	}

	result := adapter.SetProductStatus(t.Context(), Guard{}, product.State{})
	if result.Outcome != WriteRejected || !strings.Contains(result.Reason, "invalid product state") {
		t.Fatalf("SetProductStatus() = %#v, want invalid-state rejection", result)
	}
	if ownership.calls != 0 || len(runner.requests) != 0 {
		t.Fatalf("invalid state reached ownership=%d runner=%d", ownership.calls, len(runner.requests))
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
	out := r.outputs[0]
	r.outputs = r.outputs[1:]
	err := r.errors[0]
	r.errors = r.errors[1:]
	return out, err
}
