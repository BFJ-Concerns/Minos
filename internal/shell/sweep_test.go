package shell

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"bfj/minos/internal/forge"
)

func TestSweepDecisionMessage(t *testing.T) {
	facts := Facts{Owner: "owner", Repo: "repository", PR: "12"}
	tests := []struct {
		name   string
		result ReconcileResult
		want   string
	}{
		{name: "started", result: ReconcileResult{Decision: SpawnStarted}, want: "started owner/repository#12"},
		{name: "deferred", result: ReconcileResult{Decision: "deferred: open dependencies: owner/prerequisite#7"}, want: "owner/repository#12: deferred: open dependencies: owner/prerequisite#7"},
		{name: "suppressed", result: ReconcileResult{Decision: SpawnSuppressed, BlockingUnit: "minos-run-other-repo-pr9.service"}, want: "owner/repository#12: suppressed by active unit minos-run-other-repo-pr9.service"},
		{name: "continued", result: ReconcileResult{Decision: SpawnContinued}, want: "owner/repository#12: continued previous run"},
		{name: "recovered", result: ReconcileResult{Decision: ReconcileRecovered}, want: "owner/repository#12: recovered terminal Minos status"},
		{name: "nothing", result: ReconcileResult{Decision: ReconcileNothing}, want: "owner/repository#12: nothing to do"},
		{name: "attention", result: ReconcileResult{Decision: SpawnAttention, Detail: `successor made no progress beyond stage "review" round 2 and published no new head or review`}, want: `owner/repository#12: attention: stopped stalled continuation because its successor made no progress beyond stage "review" round 2 and published no new head or review`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sweepDecisionMessage(facts, test.result); got != test.want {
				t.Fatalf("message = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSweepDecisionMessageCoversReachableVocabulary(t *testing.T) {
	facts := Facts{Owner: "owner", Repo: "repository", PR: "12"}
	results := []ReconcileResult{{Decision: ReconcileDecision(deferredDecisionPrefix + "reason")}}
	for _, decision := range declaredReconcileDecisions(t) {
		result := ReconcileResult{Decision: decision}
		switch decision {
		case SpawnSuppressed:
			result.BlockingUnit = "minos-run-other-repo-pr9.service"
		case SpawnAttention:
			result.Detail = `successor made no progress beyond stage "review" round 2 and published no new head or review`
		}
		results = append(results, result)
	}
	for _, result := range results {
		if got := sweepDecisionMessage(facts, result); got == "" {
			t.Errorf("decision %q returned an empty message", result.Decision)
		}
	}
}

func declaredReconcileDecisions(t *testing.T) []ReconcileDecision {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var decisions []ReconcileDecision
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		typeName, ok := spec.Type.(*ast.Ident)
		if !ok || typeName.Name != "ReconcileDecision" {
			return true
		}
		literal, ok := spec.Values[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			t.Fatalf("ReconcileDecision %s does not have a string literal value", spec.Names[0].Name)
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Fatalf("parse ReconcileDecision %s: %v", spec.Names[0].Name, err)
		}
		decisions = append(decisions, ReconcileDecision(value))
		return true
	})
	if len(decisions) == 0 {
		t.Fatal("service.go declares no ReconcileDecision constants")
	}
	return decisions
}

func TestTerminalPullRequestExpiresHandoffBeforeFollowingResidueSweep(t *testing.T) {
	originalCommand := commandCombinedOutput
	originalSnapshot := inspectHandoffSnapshot
	t.Cleanup(func() {
		commandCombinedOutput = originalCommand
		inspectHandoffSnapshot = originalSnapshot
	})
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	inspectHandoffSnapshot = func(context.Context, ServiceConfig, Facts) (forge.Snapshot, error) {
		return forge.Snapshot{State: "closed"}, nil
	}
	cfg := scratchTestConfig(t)
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repository", PR: "21", HeadSHA: "head"}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	writeTestFile(t, filepath.Join(runDir, "workspace", ".git", "HEAD"), "fixture\n")
	handoff := writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA, []byte(`{"round":0,"confirmedUnfixed":[]}`))

	if err := sweepRunResidue(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if err := expireInactiveRunHandoffs(t.Context(), cfg, []RepoConfig{{Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handoff); !os.IsNotExist(err) {
		t.Fatalf("terminal pull request handoff still exists: %v", err)
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("terminal pull request residue was removed in the same pass: %v", err)
	}
	if err := sweepRunResidue(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("terminal pull request residue survived the following pass: %v", err)
	}
}

func TestOpenPullRequestMissingFromListingKeepsHandoff(t *testing.T) {
	originalSnapshot := inspectHandoffSnapshot
	t.Cleanup(func() { inspectHandoffSnapshot = originalSnapshot })
	inspectHandoffSnapshot = func(context.Context, ServiceConfig, Facts) (forge.Snapshot, error) {
		return forge.Snapshot{State: "open"}, nil
	}
	cfg := scratchTestConfig(t)
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repository", PR: "22", HeadSHA: "head"}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	writeTestFile(t, filepath.Join(runDir, "workspace", ".git", "HEAD"), "fixture\n")
	handoff := writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA, []byte(`{"round":0,"confirmedUnfixed":[]}`))

	if err := expireInactiveRunHandoffs(t.Context(), cfg, []RepoConfig{{Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handoff); err != nil {
		t.Fatalf("open pull request missing from listing lost its handoff: %v", err)
	}
}

func TestUnconfiguredRepositoryExpiresHandoff(t *testing.T) {
	cfg := scratchTestConfig(t)
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "removed-repository", PR: "23", HeadSHA: "head"}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	writeTestFile(t, filepath.Join(runDir, "workspace", ".git", "HEAD"), "fixture\n")
	handoff := writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA, []byte(`{"round":0,"confirmedUnfixed":[]}`))

	if err := expireInactiveRunHandoffs(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handoff); !os.IsNotExist(err) {
		t.Fatalf("unconfigured repository handoff still exists: %v", err)
	}
}
