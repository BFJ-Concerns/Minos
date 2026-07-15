package forge

import (
	"context"
	"errors"
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
