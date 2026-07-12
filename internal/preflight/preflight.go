// Package preflight proves that inexpensive deployment prerequisites are ready
// before Minos starts an agent lifecycle.
package preflight

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Result struct {
	Name       string  `json:"name"`
	Passed     bool    `json:"passed"`
	Diagnostic string  `json:"diagnostic"`
	Checks     []Check `json:"checks,omitempty"`
}

type Check struct {
	Name       string `json:"name"`
	Passed     bool   `json:"passed"`
	Diagnostic string `json:"diagnostic"`
}

type Report struct {
	Passed  bool     `json:"passed"`
	Results []Result `json:"results"`
}

type Probe interface {
	Run(context.Context) Result
}

func Run(ctx context.Context, probes []Probe) Report {
	report := Report{Passed: true, Results: make([]Result, 0, len(probes))}
	for _, probe := range probes {
		result := probe.Run(ctx)
		report.Results = append(report.Results, result)
		if !result.Passed {
			report.Passed = false
		}
	}
	return report
}

type commandRunner interface {
	CombinedOutput(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type CommandProbe struct {
	Name    string
	Command string
	Args    []string
	Timeout time.Duration
	Runner  commandRunner
	// ExposeOutput is reserved for callers that consume trusted, non-secret
	// command output. Deployment diagnostics default to redacted output.
	ExposeOutput bool
}

func (probe CommandProbe) Run(ctx context.Context) Result {
	runner := probe.Runner
	if runner == nil {
		runner = execRunner{}
	}
	if probe.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, probe.Timeout)
		defer cancel()
	}
	output, err := runner.CombinedOutput(ctx, probe.Command, probe.Args...)
	diagnostic := strings.TrimSpace(string(output))
	if err != nil {
		if !probe.ExposeOutput || diagnostic == "" {
			diagnostic = err.Error()
		} else {
			diagnostic = fmt.Sprintf("%s: %v", diagnostic, err)
		}
		return Result{Name: probe.Name, Diagnostic: diagnostic}
	}
	if !probe.ExposeOutput || diagnostic == "" {
		diagnostic = "command completed successfully"
	}
	return Result{Name: probe.Name, Passed: true, Diagnostic: diagnostic}
}
