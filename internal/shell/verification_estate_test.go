package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSpawnEnvironmentMatchesRunBodyContract(t *testing.T) {
	root := t.TempDir()
	cfg := ServiceConfig{Root: filepath.Join(root, "config")}
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Forges = map[string]ForgeConfig{
		"local": {Adaptation: filepath.Join(root, "adaptations")},
	}
	repo := RepoConfig{Forge: "local", Owner: "pump19", Repo: "subject"}
	repo.Adaptation.Skill = filepath.Join(root, "skills", "review")
	repo.Adaptation.Briefs = ".review"
	facts := Facts{
		Forge:   "local",
		Owner:   "pump19",
		Repo:    "subject",
		PR:      "42",
		HeadSHA: "abcdef1234567890",
		BaseRef: "main",
	}

	runDir := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
	unit := UnitName(facts, RunReview)
	env := envMap(runEnv(cfg, repo, facts, RunReview, runDir, unit, "pr-opened"))

	required := map[string]string{
		"PUMP19_RUN_DIR":    runDir,
		"PUMP19_RUN_KIND":   "review",
		"PUMP19_OCCASION":   "pr-opened",
		"PUMP19_FORGE":      "local",
		"PUMP19_REPO":       "pump19/subject",
		"PUMP19_OWNER":      "pump19",
		"PUMP19_REPO_NAME":  "subject",
		"PUMP19_PR":         "42",
		"PUMP19_HEAD_SHA":   "abcdef1234567890",
		"PUMP19_BASE_REF":   "main",
		"PUMP19_WORKSPACE":  runDir + ".workspace",
		"PUMP19_DIFF":       filepath.Join(runDir, "diff.patch"),
		"PUMP19_ADAPTATION": filepath.Join(root, "adaptations"),
		"PUMP19_SKILL":      filepath.Join(root, "skills", "review"),
		"PUMP19_BRIEFS":     ".review",
		"PUMP19_CONFIG":     cfg.Root,
		"PUMP19_UNIT":       unit,
	}
	for key, want := range required {
		if got := env[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if strings.Contains(env["PUMP19_RUN_DIR"], ".reaped-") {
		t.Fatalf("spawn handed a reaped evidence path as the live claim: %s", env["PUMP19_RUN_DIR"])
	}
}

func TestPartialCoverageNeverReconcilesAsConverged(t *testing.T) {
	drafts := false
	repo := RepoConfig{Triggers: []TriggerRule{
		{Run: "review", Authors: []string{"*"}, Drafts: &drafts},
		{Run: "fix", Actors: []string{"pump19"}},
	}}
	facts := Facts{
		Forge:  "local",
		Owner:  "pump19",
		Repo:   "subject",
		PR:     "42",
		Draft:  false,
		Labels: []string{LabelPartialCoverage},
	}
	reviewContext, _ := StatusContext(RunReview)

	decision, ok, err := reconcileDecision(context.Background(), repo, Adaptation{}, facts, []Status{{
		Context: reviewContext,
		State:   "failure",
		Creator: "pump19",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || decision != RunFix {
		t.Fatalf("partial coverage with findings should still drive fix, got ok=%v decision=%s", ok, decision)
	}

	decision, ok, err = reconcileDecision(context.Background(), repo, Adaptation{}, facts, []Status{{
		Context: reviewContext,
		State:   "error",
		Creator: "pump19",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("partial coverage without findings must not auto-fire, got decision=%s", decision)
	}
}

func TestReapFailsClosedWhenUnitCannotBeStopped(t *testing.T) {
	original := systemctlCommand
	systemctlCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) > 1 && args[1] == "stop" {
			return exec.CommandContext(ctx, "true")
		}
		return exec.CommandContext(ctx, "printf", "active\n")
	}
	t.Cleanup(func() {
		systemctlCommand = original
	})

	runDir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "meta.env"), []byte("PUMP19_UNIT=pump19-definitely-not-a-real-unit.service\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := reapRunDir(context.Background(), runDir)
	if err == nil {
		t.Fatal("expected reap to fail when the unit cannot be stopped")
	}
	if _, statErr := os.Stat(runDir); statErr != nil {
		t.Fatalf("run dir was released despite failed stop: %v", statErr)
	}
	matches, globErr := filepath.Glob(runDir + ".reaped-*")
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("failed reap left evidence rename behind: %v", matches)
	}
}

func TestReapReleasesPreBodyClaimWithoutMetadata(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := reapRunDir(context.Background(), runDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("canonical claim still exists after pre-body reap: %v", err)
	}
	matches, err := filepath.Glob(runDir + ".reaped-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one evidence directory, got %v", matches)
	}
}

func TestRunMetadataWriteFailureIsLoud(t *testing.T) {
	missingRunDir := filepath.Join(t.TempDir(), "missing", "run")
	if err := writeMeta(missingRunDir); err == nil {
		t.Fatal("expected metadata write failure for a missing run directory")
	}
}

func TestLiveRunStateTreatsWarmLogAsAliveAndQuietLogAsDead(t *testing.T) {
	root := t.TempDir()
	facts := Facts{Forge: "local", Owner: "pump19", Repo: "subject", PR: "42"}
	runDir := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "abcdef1234567890", RunReview)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(runDir, "run.log")
	if err := os.WriteFile(logPath, []byte("heartbeat\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, alive, err := liveRunState(root, facts, RunReview, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !alive {
		t.Fatal("warm run log should keep the run alive")
	}

	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatal(err)
	}
	_, alive, err = liveRunState(root, facts, RunReview, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("quiet run log should be reaped after the threshold")
	}
}

func envMap(pairs []string) map[string]string {
	values := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, _ := strings.Cut(pair, "=")
		values[key] = value
	}
	return values
}
