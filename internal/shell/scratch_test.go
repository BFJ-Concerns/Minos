package shell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSweepRunResidueRemovesDeadReadOnlyTreeAndKeepsLiveUnit(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "systemctl" {
			t.Fatalf("command = %s, want systemctl", name)
		}
		return []byte("minos-run-owner-live-pr2.service loaded active running Live run\n"), nil
	}

	cfg := scratchTestConfig(t)
	dead := filepath.Join(cfg.Runs.Dir, "minos-run-owner-dead-pr1-dead")
	live := filepath.Join(cfg.Runs.Dir, "minos-run-owner-live-pr2-live")
	t.Cleanup(func() { _ = removeRunDir(live) })
	for _, runDir := range []string{dead, live} {
		if err := os.MkdirAll(filepath.Join(runDir, "cache", "readonly"), 0o755); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(runDir, "cache", "readonly", "artifact")
		if err := os.WriteFile(file, []byte("cache\n"), 0o400); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(file), 0o500); err != nil {
			t.Fatal(err)
		}
	}

	_ = sweepRunResidue(t.Context(), cfg, nil)
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("dead run still exists: %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live run was removed: %v", err)
	}
}

func TestSweepRunResidueProtectsOnlyDirectoryNamedByValidHandoff(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }

	cfg := scratchTestConfig(t)
	facts := Facts{Owner: "owner", Repo: "repository", PR: "7", HeadSHA: "head"}
	preserved := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	residue := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-different")
	for _, runDir := range []string{preserved, residue} {
		if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeTestHandoff(t, cfg, facts, preserved, facts.HeadSHA, 0, []byte(`{"round":0,"confirmedUnfixed":[]}`))

	_ = sweepRunResidue(t.Context(), cfg, map[string]Facts{UnitName(facts): facts})
	if _, err := os.Stat(preserved); err != nil {
		t.Fatalf("valid handoff directory was removed: %v", err)
	}
	if _, err := os.Stat(residue); !os.IsNotExist(err) {
		t.Fatalf("different residue directory was protected: %v", err)
	}
}

func TestSweepRunResidueDoesNotProtectInvalidHandoff(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }

	cfg := scratchTestConfig(t)
	facts := Facts{Owner: "owner", Repo: "repository", PR: "8", HeadSHA: "current"}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-rejected")
	if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestHandoff(t, cfg, facts, runDir, "stale-head", 0, []byte(`{"round":0,"confirmedUnfixed":[]}`))

	_ = sweepRunResidue(t.Context(), cfg, map[string]Facts{UnitName(facts): facts})
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("invalid handoff protected residue: %v", err)
	}
}

func TestSweepRunResiduePreservesEvidenceWhenDurableSalvageFails(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }

	cfg := scratchTestConfig(t)
	cfg.Runs.FailuresRepo = filepath.Join(t.TempDir(), "missing-Minos-Annexe")
	runDir := filepath.Join(cfg.Runs.Dir, "minos-run-owner-repository-pr10-dead")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "failure.log"), []byte("only evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := sweepRunResidue(t.Context(), cfg, nil); err == nil {
		t.Fatal("sweep succeeded despite unavailable durable salvage target")
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("run evidence was deleted after salvage failed: %v", err)
	}
}

func TestAppendFailureDigestWritesFailuresMarkdown(t *testing.T) {
	cfg := scratchTestConfig(t)
	runDir := filepath.Join(cfg.Runs.Dir, "minos-run-owner-repository-pr9-dead")
	files := map[string]string{
		"failure.log": "stage=review cause=worker died\n",
		"home/.claude/projects/repository/session.jsonl": "{\"type\":\"assistant\",\"text\":\"last turn\"}\n",
		"ensemble-records/review/run.json":               "{\"status\":\"incomplete\"}\n",
	}
	for name, contents := range files {
		path := filepath.Join(runDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := appendFailureDigest(runDir, cfg.Runs.FailuresRepo); err != nil {
		t.Fatal(err)
	}
	digest, err := os.ReadFile(filepath.Join(cfg.Runs.FailuresRepo, "FAILURES.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Failures", "## ", filepath.Base(runDir), "### failure.log", "worker died",
		"### home/.claude/projects/repository/session.jsonl", "last turn",
		"### ensemble-records/review/run.json", "incomplete",
	} {
		if !strings.Contains(string(digest), want) {
			t.Fatalf("FAILURES.md omits %q:\n%s", want, digest)
		}
	}
}

func scratchTestConfig(t *testing.T) ServiceConfig {
	t.Helper()
	root := t.TempDir()
	var cfg ServiceConfig
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Runs.FailuresRepo = filepath.Join(root, "Minos-Annexe")
	if err := os.MkdirAll(cfg.Runs.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.Runs.FailuresRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Runs.FailuresRepo, "FAILURES.md"), []byte("# Failures\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}
