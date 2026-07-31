package shell

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"bfj/minos/internal/forge"
)

func TestSweepDecisionMessageExposesDeferrals(t *testing.T) {
	facts := Facts{Owner: "owner", Repo: "repository", PR: "12"}
	if got := sweepDecisionMessage(facts, "deferred: open dependencies: owner/prerequisite#7"); got != "owner/repository#12: deferred: open dependencies: owner/prerequisite#7" {
		t.Fatalf("message = %q", got)
	}
	if got := sweepDecisionMessage(facts, "nothing"); got != "" {
		t.Fatalf("nothing message = %q, want empty", got)
	}
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
	handoff := writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA, 0, []byte(`{"round":0,"confirmedUnfixed":[]}`))

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
	handoff := writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA, 0, []byte(`{"round":0,"confirmedUnfixed":[]}`))

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
	handoff := writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA, 0, []byte(`{"round":0,"confirmedUnfixed":[]}`))

	if err := expireInactiveRunHandoffs(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handoff); !os.IsNotExist(err) {
		t.Fatalf("unconfigured repository handoff still exists: %v", err)
	}
}
