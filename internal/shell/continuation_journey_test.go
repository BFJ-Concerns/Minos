package shell

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreservedContinuationStartsAWorkingSuccessor(t *testing.T) {
	fixture := newRunBodyFixture(t)
	cfg := ServiceConfig{Root: fixture.configRoot}
	cfg.Runs.Dir = filepath.Join(fixture.root, "runs")
	cfg.Forges = map[string]ForgeConfig{"forgejo": {}}
	if err := os.MkdirAll(cfg.Runs.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repository", PR: "17", HeadSHA: "head-sha"}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, runOwnerMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "lead-complete"), []byte("continuation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "memory-pressure"), []byte("predecessor pressure\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handoff := writeTestHandoff(
		t, cfg, facts, runDir, facts.HeadSHA, 1,
		json.RawMessage(`{"round":2,"confirmedUnfixed":[]}`),
	)

	body := filepath.Join(fixture.root, "predecessor-body")
	writeScript(t, body, "#!/usr/bin/env sh\nexit 0\n")
	t.Setenv("MINOS_RUN_DIR", runDir)
	t.Setenv("MINOS_RUN_BODY", body)
	t.Setenv("MINOS_HANDOFF", handoff)
	if err := RunCommand(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("predecessor continuation was not preserved: %v", err)
	}

	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	var systemdArgs []string
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" {
			return nil, nil
		}
		systemdArgs = append([]string(nil), args...)
		return nil, nil
	}
	repo := RepoConfig{}
	repo.Adaptation.RunBody = filepath.Join("..", "..", "scripts", "run-body", "run-body")
	if _, err := SpawnRun(t.Context(), cfg, repo, facts); err != nil {
		t.Fatal(err)
	}
	environment := systemdEnvironment(t, systemdArgs)
	if environment["MINOS_RUN_DIR"] != runDir || environment["MINOS_RESUME"] != "true" {
		t.Fatalf("successor environment = %#v, want adopted run directory", environment)
	}

	fixture.runDir = runDir
	environment["MINOS_TEST_PENDING_STATE"] = "done"
	environment["MINOS_TEST_WAIT_POLLS"] = "2"
	environment["MINOS_TEST_TERMINAL_STATE"] = "failed"
	environment["MINOS_CLAUDE_POLL_SECONDS"] = "0"
	fixture.run(t, environment)

	attempts, err := os.ReadFile(fixture.record + ".attempts")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(attempts)); got != "3" {
		t.Fatalf("successor supervision polls = %s, want 3; stale completion state stopped the successor", got)
	}
	for _, stale := range []string{"lead-complete", "memory-pressure"} {
		if _, err := os.Stat(filepath.Join(runDir, stale)); !os.IsNotExist(err) {
			t.Fatalf("successor inherited stale %s or stat failed: %v", stale, err)
		}
	}
	fixture.assertProcessesStopped(t)
}
