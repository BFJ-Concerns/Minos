package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type probeFunc func(context.Context) Result

func (fn probeFunc) Run(ctx context.Context) Result { return fn(ctx) }

func TestRunReportsEveryProbeAndFailsAggregate(t *testing.T) {
	probes := []Probe{
		probeFunc(func(context.Context) Result { return Result{Name: "forge", Passed: true, Diagnostic: "identity minos"} }),
		probeFunc(func(context.Context) Result {
			return Result{Name: "engine", Passed: false, Diagnostic: "backend unavailable"}
		}),
	}

	report := Run(context.Background(), probes)
	if report.Passed {
		t.Fatal("aggregate passed despite a failed probe")
	}
	if len(report.Results) != 2 || report.Results[1].Name != "engine" {
		t.Fatalf("unexpected results: %#v", report.Results)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("report is not JSON: %s", data)
	}
}

func TestCommandProbeIncludesExitFailureWithoutEnvironmentValues(t *testing.T) {
	runner := runnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("subscription denied"), errors.New("exit status 1")
	})
	probe := CommandProbe{Name: "engine", Command: "claude", Args: []string{"auth", "status"}, Runner: runner, ExposeOutput: true}
	result := probe.Run(context.Background())
	if result.Passed || result.Diagnostic != "subscription denied: exit status 1" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCommandProbeRedactsOutputByDefault(t *testing.T) {
	runner := runnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("credential=do-not-log-this"), errors.New("exit status 1")
	})
	result := CommandProbe{Name: "registry", Command: "registry", Runner: runner}.Run(context.Background())
	if result.Diagnostic != "exit status 1" {
		t.Fatalf("command output leaked into diagnostic: %q", result.Diagnostic)
	}
}

type runnerFunc func(context.Context, string, ...string) ([]byte, error)

func (fn runnerFunc) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return fn(ctx, name, args...)
}
