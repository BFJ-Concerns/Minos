package shell

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestSpawnRunRemovesReadOnlyTreeWhenStartFails(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })

	var runDir string
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			return nil, nil
		case "systemd-run":
			for _, argument := range args {
				if strings.HasPrefix(argument, "MINOS_RUN_DIR=") {
					runDir = strings.TrimPrefix(argument, "MINOS_RUN_DIR=")
					break
				}
			}
			if runDir == "" {
				t.Fatal("systemd-run arguments omitted MINOS_RUN_DIR")
			}
			readOnlyDir := filepath.Join(runDir, "cache", "go", "modules", "example.test", "module@v1.0.0")
			if err := os.MkdirAll(readOnlyDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(readOnlyDir, "module.go"), []byte("package module\n"), 0o400); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(readOnlyDir, 0o500); err != nil {
				t.Fatal(err)
			}
			return []byte("start failed"), errors.New("exit 1")
		default:
			t.Fatalf("unexpected command %q", name)
			return nil, nil
		}
	}

	cfg := ServiceConfig{Root: "/etc/minos"}
	cfg.Runs.Dir = t.TempDir()
	cfg.Forges = map[string]ForgeConfig{"forgejo": {}}
	repo := RepoConfig{}
	repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"

	if _, err := SpawnRun(t.Context(), cfg, repo, Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "1"}); err == nil {
		t.Fatal("SpawnRun succeeded despite systemd-run failure")
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("failed run directory still exists or stat failed unexpectedly: %v", err)
	}
}

func TestSpawnRunReportsSuppressedForActiveUnit(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })

	var commands []string
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		commands = append(commands, name)
		return []byte("minos-run-other-repo-pr9.service loaded active running Minos lead\n"), nil
	}

	cfg := ServiceConfig{}
	cfg.Runs.Dir = t.TempDir()
	outcome, err := SpawnRun(t.Context(), cfg, RepoConfig{}, Facts{Owner: "owner", Repo: "repo", PR: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Outcome != SpawnSuppressed {
		t.Fatalf("outcome = %q, want %q", outcome, SpawnSuppressed)
	}
	if outcome.BlockingUnit != "minos-run-other-repo-pr9.service" {
		t.Fatalf("blocking unit = %q", outcome.BlockingUnit)
	}
	if !slices.Equal(commands, []string{"systemctl"}) {
		t.Fatalf("commands = %v, want only systemctl", commands)
	}
}

func TestSpawnRunReportsRequestedUnitForSystemdRunRace(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemctl" {
			return nil, nil
		}
		return []byte("Unit minos-run-owner-repo-pr1.service already exists."), errors.New("exit status 1")
	}

	cfg := ServiceConfig{Root: "/etc/minos", Forges: map[string]ForgeConfig{"forgejo": {}}}
	cfg.Runs.Dir = t.TempDir()
	repo := RepoConfig{}
	repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"
	result, err := SpawnRun(t.Context(), cfg, repo, Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != SpawnSuppressed {
		t.Fatalf("outcome = %q, want %q", result.Outcome, SpawnSuppressed)
	}
	if result.BlockingUnit != "minos-run-owner-repo-pr1.service" {
		t.Fatalf("blocking unit = %q", result.BlockingUnit)
	}
}

func TestSpawnRunAdoptsValidatedContinuationAndSeedsLoopRecord(t *testing.T) {
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

	cfg := ServiceConfig{Root: "/etc/minos"}
	cfg.Runs.Dir = t.TempDir()
	cfg.Forges = map[string]ForgeConfig{"forgejo": {}}
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "head"}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	runRecord := json.RawMessage(`{"round":3,"confirmedFixed":[{"key":"repaired"}],"confirmedUnfixed":[{"key":"known"}]}`)
	handoffFile := writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA, runRecord)

	outcome, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Outcome != SpawnStarted {
		t.Fatalf("outcome = %q, want %q", outcome, SpawnStarted)
	}
	for _, value := range []string{
		"MINOS_RUN_DIR=" + runDir,
		"MINOS_RESUME=true",
		"MINOS_HANDOFF=" + handoffFile,
		"MINOS_LOOP_RECORD=" + filepath.Join(runDir, "loop-record.json"),
	} {
		assertArgument(t, systemdArgs, value)
	}
	seed, err := os.ReadFile(filepath.Join(runDir, "loop-record.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(seed) != string(runRecord) {
		t.Fatalf("seed = %s, want verbatim %s", seed, runRecord)
	}
	if _, err := os.Stat(handoffFile); !os.IsNotExist(err) {
		t.Fatalf("consumed handoff still exists or stat failed: %v", err)
	}
}

func TestSpawnRunCarriesCompleteReviewVerdictAcrossContinuation(t *testing.T) {
	for _, rejectedHandoff := range []bool{false, true} {
		name := "accepted handoff"
		if rejectedHandoff {
			name = "rejected handoff"
		}
		t.Run(name, func(t *testing.T) {
			cfg, facts, predecessor, systemdArgs := reviewContinuationFixture(t)
			verdict := []byte(`{"status":"complete","reviewed":{"head":"head"},"findings":[]}`)
			if err := os.WriteFile(filepath.Join(predecessor, "review-result.json"), verdict, 0o600); err != nil {
				t.Fatal(err)
			}
			handoffFile := writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA, json.RawMessage(`{"round":1,"confirmedUnfixed":[]}`))
			if rejectedHandoff {
				if err := os.WriteFile(handoffFile, []byte(`{"kind":"wrong"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			stderr := captureStderr(t, func() {
				if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err != nil {
					t.Fatal(err)
				}
			})
			if !strings.Contains(stderr, "adopted completed review verdict") || !strings.Contains(stderr, facts.HeadSHA) {
				t.Fatalf("stderr does not report verdict adoption: %q", stderr)
			}
			successor := argumentValue(*systemdArgs, "MINOS_RUN_DIR")
			if rejectedHandoff && successor == predecessor {
				t.Fatalf("rejected handoff reused predecessor directory %q", predecessor)
			}
			carried, err := os.ReadFile(filepath.Join(successor, "carried-review-result.json"))
			if err != nil || string(carried) != string(verdict) {
				t.Fatalf("successor verdict = %s, err = %v; want %s", carried, err, verdict)
			}
			if _, err := os.Stat(filepath.Join(successor, "review-result.json")); !os.IsNotExist(err) {
				t.Fatalf("ordinary round result can still masquerade as a carried verdict: %v", err)
			}
			if rejectedHandoff {
				if _, err := os.Stat(handoffFile + ".rejected"); err != nil {
					t.Fatalf("strictly rejected handoff was not preserved: %v", err)
				}
			}
		})
	}
}

func TestSpawnRunRefusesAmbiguousPredecessorReviewVerdicts(t *testing.T) {
	cfg, facts, predecessor, systemdArgs := reviewContinuationFixture(t)
	verdict := []byte(`{"status":"complete","reviewed":{"head":"head"},"findings":[]}`)
	for _, runDir := range []string{
		predecessor,
		filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-second-predecessor"),
	} {
		if err := os.MkdirAll(runDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDir, "review-result.json"), verdict, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handoffFile := writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA, json.RawMessage(`{"round":1}`))
	if err := os.WriteFile(handoffFile, []byte(`{"kind":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stderr := captureStderr(t, func() {
		if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(stderr, "adopted completed review verdict") {
		t.Fatalf("stderr falsely reports ambiguous verdict adoption: %q", stderr)
	}
	successor := argumentValue(*systemdArgs, "MINOS_RUN_DIR")
	if _, err := os.Stat(filepath.Join(successor, "carried-review-result.json")); !os.IsNotExist(err) {
		t.Fatalf("ambiguous verdict remains successor-visible or stat failed: %v", err)
	}
}

func TestSpawnRunCarriesVerdictThroughSuccessiveRejectedHandoffs(t *testing.T) {
	cfg, facts, firstPredecessor, systemdArgs := reviewContinuationFixture(t)
	verdict := []byte(`{"status":"complete","reviewed":{"head":"head"},"findings":[]}`)
	if err := os.WriteFile(filepath.Join(firstPredecessor, "review-result.json"), verdict, 0o600); err != nil {
		t.Fatal(err)
	}
	firstHandoff := writeTestHandoff(t, cfg, facts, firstPredecessor, facts.HeadSHA, json.RawMessage(`{"round":1}`))
	if err := os.WriteFile(firstHandoff, []byte(`{"kind":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err != nil {
		t.Fatal(err)
	}
	firstSuccessor := argumentValue(*systemdArgs, "MINOS_RUN_DIR")
	if err := os.Rename(
		filepath.Join(firstSuccessor, "carried-review-result.json"),
		filepath.Join(firstSuccessor, "review-result.json"),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(firstSuccessor, runOwnerMarker)); err != nil {
		t.Fatal(err)
	}

	secondHandoff := writeTestHandoff(t, cfg, facts, firstSuccessor, facts.HeadSHA, json.RawMessage(`{"round":2}`))
	if err := os.WriteFile(secondHandoff, []byte(`{"kind":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t, func() {
		if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, "adopted completed review verdict") {
		t.Fatalf("second successor did not adopt the chain's verdict: %q", stderr)
	}
	secondSuccessor := argumentValue(*systemdArgs, "MINOS_RUN_DIR")
	if _, err := os.Stat(filepath.Join(secondSuccessor, "carried-review-result.json")); err != nil {
		t.Fatalf("second successor lacks carried verdict: %v", err)
	}
	if _, err := os.Stat(filepath.Join(firstPredecessor, "review-result.json")); !os.IsNotExist(err) {
		t.Fatalf("consumed first predecessor still competes as a candidate: %v", err)
	}
}

func TestSpawnRunRemovesStaleCarriedVerdictBeforeSuccessorLaunch(t *testing.T) {
	for _, rejectedHandoff := range []bool{false, true} {
		name := "accepted handoff"
		if rejectedHandoff {
			name = "rejected handoff"
		}
		t.Run(name, func(t *testing.T) {
			cfg, facts, predecessor, systemdArgs := reviewContinuationFixture(t)
			stale := filepath.Join(predecessor, "carried-review-result.json")
			if err := os.WriteFile(stale, []byte(`{"status":"complete","reviewed":{"head":"old-head"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			handoff := writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA, json.RawMessage(`{"round":1,"confirmedUnfixed":[]}`))
			if rejectedHandoff {
				if err := os.WriteFile(handoff, []byte(`{"kind":"wrong"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			stderr := captureStderr(t, func() {
				if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err != nil {
					t.Fatal(err)
				}
			})
			if strings.Contains(stderr, "adopted completed review verdict") {
				t.Fatalf("stderr falsely reports stale carry adoption: %q", stderr)
			}
			successor := argumentValue(*systemdArgs, "MINOS_RUN_DIR")
			if !rejectedHandoff && successor != predecessor {
				t.Fatalf("successor = %q, want adopted directory %q", successor, predecessor)
			}
			if _, err := os.Stat(stale); !os.IsNotExist(err) {
				t.Fatalf("stale carry remains after admission or stat failed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(successor, "carried-review-result.json")); !os.IsNotExist(err) {
				t.Fatalf("stale carry remains successor-visible or stat failed: %v", err)
			}
		})
	}
}

func TestSpawnRunLaunchesWhenSiblingStaleCarryCannotBeRemoved(t *testing.T) {
	cfg, facts, predecessor, systemdArgs := reviewContinuationFixture(t)
	stale := filepath.Join(predecessor, "carried-review-result.json")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "undeletable"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	handoff := writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA, json.RawMessage(`{"round":1}`))
	if err := os.WriteFile(handoff, []byte(`{"kind":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	outcome, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts)
	if err != nil {
		t.Fatalf("SpawnRun failed on an untrusted sibling carry: %v", err)
	}
	if outcome.Outcome != SpawnStarted {
		t.Fatalf("outcome = %q, want %q", outcome, SpawnStarted)
	}
	successor := argumentValue(*systemdArgs, "MINOS_RUN_DIR")
	if successor == "" || successor == predecessor {
		t.Fatalf("successor = %q, want a fresh launched run directory", successor)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("scan-side stale carry was unexpectedly removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(successor, "carried-review-result.json")); !os.IsNotExist(err) {
		t.Fatalf("stale sibling carry became successor-visible or stat failed: %v", err)
	}
}

func TestSpawnRunRestoresMovedVerdictWhenRejectedHandoffSpawnFails(t *testing.T) {
	cfg, facts, predecessor, _ := reviewContinuationFixture(t)
	verdictPath := filepath.Join(predecessor, "review-result.json")
	if err := os.WriteFile(verdictPath, []byte(`{"status":"complete","reviewed":{"head":"head"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	handoff := writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA, json.RawMessage(`{"round":1}`))
	if err := os.WriteFile(handoff, []byte(`{"kind":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemctl" {
			return nil, nil
		}
		return []byte("failed"), errors.New("spawn failed")
	}

	if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err == nil {
		t.Fatal("SpawnRun succeeded, want systemd-run failure")
	}
	if _, err := os.Stat(verdictPath); err != nil {
		t.Fatalf("spawn failure did not restore predecessor verdict: %v", err)
	}
}

func TestSpawnRunRestoresMovedVerdictWhenAcceptedHandoffSpawnFails(t *testing.T) {
	cfg, facts, predecessor, _ := reviewContinuationFixture(t)
	verdictPath := filepath.Join(predecessor, "review-result.json")
	if err := os.WriteFile(verdictPath, []byte(`{"status":"complete","reviewed":{"head":"head"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA, json.RawMessage(`{"round":1,"confirmedUnfixed":[]}`))
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemctl" {
			return nil, nil
		}
		return []byte("failed"), errors.New("spawn failed")
	}

	if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err == nil {
		t.Fatal("SpawnRun succeeded, want systemd-run failure")
	}
	if _, err := os.Stat(verdictPath); err != nil {
		t.Fatalf("spawn failure did not restore predecessor verdict: %v", err)
	}
}

func TestSpawnRunRefusesUntrustedReviewVerdicts(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		mode    os.FileMode
	}{
		{name: "absent"},
		{name: "unreadable", content: []byte(`{"status":"complete","reviewed":{"head":"head"}}`), mode: 0},
		{name: "malformed", content: []byte(`{"status":`), mode: 0o600},
		{name: "non-object", content: []byte(`[]`), mode: 0o600},
		{name: "non-complete", content: []byte(`{"status":"incomplete","reviewed":{"head":"head"}}`), mode: 0o600},
		{name: "different head", content: []byte(`{"status":"complete","reviewed":{"head":"foreign"}}`), mode: 0o600},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, facts, predecessor, systemdArgs := reviewContinuationFixture(t)
			if test.content != nil {
				path := filepath.Join(predecessor, "review-result.json")
				if err := os.WriteFile(path, test.content, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, test.mode); err != nil {
					t.Fatal(err)
				}
			}
			writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA, json.RawMessage(`{"round":1,"confirmedUnfixed":[]}`))
			stderr := captureStderr(t, func() {
				if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err != nil {
					t.Fatal(err)
				}
			})
			if strings.Contains(stderr, "adopted completed review verdict") {
				t.Fatalf("stderr falsely reports verdict adoption: %q", stderr)
			}
			successor := argumentValue(*systemdArgs, "MINOS_RUN_DIR")
			if _, err := os.Stat(filepath.Join(successor, "review-result.json")); !os.IsNotExist(err) {
				t.Fatalf("untrusted verdict remains successor-visible or stat failed: %v", err)
			}
		})
	}
}

func reviewContinuationFixture(t *testing.T) (ServiceConfig, Facts, string, *[]string) {
	t.Helper()
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
	cfg := ServiceConfig{Root: "/etc/minos"}
	cfg.Runs.Dir = t.TempDir()
	cfg.Forges = map[string]ForgeConfig{"forgejo": {}}
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "head"}
	predecessor := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-predecessor")
	if err := os.MkdirAll(filepath.Join(predecessor, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return cfg, facts, predecessor, &systemdArgs
}

func captureStderr(t *testing.T, action func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = writer
	action()
	os.Stderr = original
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func TestSpawnRunKeepsValidLoopRecordWhenHeadMoved(t *testing.T) {
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

	cfg := ServiceConfig{Root: "/etc/minos"}
	cfg.Runs.Dir = t.TempDir()
	cfg.Forges = map[string]ForgeConfig{"forgejo": {}}
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "new-head"}
	oldRunDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	if err := os.MkdirAll(filepath.Join(oldRunDir, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	runRecord := json.RawMessage(`{"round":2,"confirmedUnfixed":[]}`)
	writeTestHandoff(t, cfg, facts, oldRunDir, "old-head", runRecord)

	if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err != nil {
		t.Fatal(err)
	}
	runDir := argumentValue(systemdArgs, "MINOS_RUN_DIR")
	if runDir == "" || runDir == oldRunDir {
		t.Fatalf("fresh run directory = %q, old = %q", runDir, oldRunDir)
	}
	if slices.Contains(systemdArgs, "MINOS_RESUME=true") {
		t.Fatalf("moved head unexpectedly resumed: %v", systemdArgs)
	}
	seed, err := os.ReadFile(filepath.Join(runDir, "loop-record.json"))
	if err != nil || string(seed) != string(runRecord) {
		t.Fatalf("fresh run seed = %s, err = %v", seed, err)
	}
}

func TestSpawnRunRejectsMalformedHandoffAndStartsFresh(t *testing.T) {
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
	cfg := ServiceConfig{Root: "/etc/minos"}
	cfg.Runs.Dir = t.TempDir()
	cfg.Forges = map[string]ForgeConfig{"forgejo": {}}
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "head"}
	handoffFile := handoffPath(cfg.Runs.Dir, UnitName(facts))
	if err := os.MkdirAll(filepath.Dir(handoffFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handoffFile, []byte(`{"kind":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handoffFile + ".rejected"); err != nil {
		t.Fatalf("rejected handoff was not preserved: %v", err)
	}
	runDir := argumentValue(systemdArgs, "MINOS_RUN_DIR")
	if _, err := os.Stat(filepath.Join(runDir, "loop-record.json")); !os.IsNotExist(err) {
		t.Fatalf("malformed handoff seeded a loop record or stat failed: %v", err)
	}
}

func TestSpawnRunDoesNotDeleteAdoptedDirectoryWhenSystemdStartFails(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemctl" {
			return nil, nil
		}
		return []byte("start failed"), errors.New("exit 1")
	}
	cfg := ServiceConfig{Root: "/etc/minos"}
	cfg.Runs.Dir = t.TempDir()
	cfg.Forges = map[string]ForgeConfig{"forgejo": {}}
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "head"}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA, json.RawMessage(`{"round":1,"confirmedUnfixed":[]}`))
	if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err == nil {
		t.Fatal("SpawnRun succeeded despite systemd-run failure")
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("adopted directory was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, runOwnerMarker)); !os.IsNotExist(err) {
		t.Fatalf("ownership marker remains after failed start or stat failed: %v", err)
	}
}

func TestSpawnRunExportsRunContractAndHardTimeout(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })

	var systemdArgs []string
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			return nil, nil
		case "systemd-run":
			systemdArgs = append([]string(nil), args...)
			return nil, nil
		default:
			t.Fatalf("unexpected command %q", name)
			return nil, nil
		}
	}

	cfg := ServiceConfig{Root: "/etc/minos"}
	cfg.Runs.Dir = t.TempDir()
	cfg.Ensemble.ConcurrencyClaude = 10
	cfg.Ensemble.ConcurrencyCodex = 6
	cfg.Forges = map[string]ForgeConfig{
		"forgejo": {APIBase: "http://forge.local", CredentialFile: "/etc/minos/forge.token"},
	}
	repo := RepoConfig{}
	repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"
	repo.Adaptation.Build = "make build"
	repo.Adaptation.Test = "make test"
	repo.Policy.AutoMerge = true
	repo.Review.Threshold = "Medium"
	repo.Review.MaximumRounds = 7
	facts := Facts{
		Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7",
		HeadSHA: "head", BaseSHA: "target", BaseRef: "main", HeadRef: "feature",
	}

	outcome, err := SpawnRun(t.Context(), cfg, repo, facts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Outcome != SpawnStarted {
		t.Fatalf("outcome = %q, want %q", outcome, SpawnStarted)
	}
	assertArgument(t, systemdArgs, "--property=ExitType=main")
	assertArgument(t, systemdArgs, "--property=KillMode=control-group")
	assertArgument(t, systemdArgs, "--property=RuntimeMaxSec=12h")
	assertArgument(t, systemdArgs, "--property=MemoryMax=14G")
	for _, value := range []string{
		"MINOS_CONFIG=/etc/minos",
		"MINOS_FORGE=forgejo",
		"MINOS_OWNER=owner",
		"MINOS_REPO_NAME=repo",
		"MINOS_PR=7",
		"MINOS_HEAD_SHA=head",
		"MINOS_TARGET_SHA=target",
		"MINOS_BASE_REF=main",
		"MINOS_HEAD_BRANCH=feature",
		"MINOS_API_BASE=http://forge.local",
		"MINOS_CREDENTIAL_FILE=/etc/minos/forge.token",
		"MINOS_BUILD_CMD=make build",
		"MINOS_TEST_CMD=make test",
		"MINOS_RUN_BODY=/opt/minos/run-body/run-body",
		"MINOS_AUTO_MERGE=true",
		"MINOS_REVIEW_THRESHOLD=Medium",
		"MINOS_MAX_ROUNDS=7",
		"ENSEMBLE_CONCURRENCY_CLAUDE=10",
		"ENSEMBLE_CONCURRENCY_CODEX=6",
	} {
		assertArgument(t, systemdArgs, "--setenv")
		assertArgument(t, systemdArgs, value)
	}
	var runDir string
	for _, arg := range systemdArgs {
		if strings.HasPrefix(arg, "MINOS_RUN_DIR=") {
			runDir = strings.TrimPrefix(arg, "MINOS_RUN_DIR=")
			break
		}
	}
	if runDir == "" {
		t.Fatalf("systemd-run arguments omit MINOS_RUN_DIR: %v", systemdArgs)
	}
	for _, value := range []string{
		"MINOS_HANDOFF=" + handoffPath(cfg.Runs.Dir, UnitName(facts)),
		"MINOS_LOOP_RECORD=" + filepath.Join(runDir, "loop-record.json"),
	} {
		assertArgument(t, systemdArgs, value)
	}
	if _, err := os.Stat(filepath.Join(runDir, runOwnerMarker)); err != nil {
		t.Fatalf("run ownership marker was not created: %v", err)
	}
	if !slices.ContainsFunc(systemdArgs, func(arg string) bool {
		return strings.HasPrefix(arg, "MINOS_WORKSPACE=")
	}) {
		t.Fatalf("systemd-run arguments omit MINOS_WORKSPACE: %v", systemdArgs)
	}
	if !slices.ContainsFunc(systemdArgs, func(arg string) bool {
		return strings.HasPrefix(arg, "MINOS_ORIENTATION=")
	}) {
		t.Fatalf("systemd-run arguments omit MINOS_ORIENTATION: %v", systemdArgs)
	}
}

func TestSpawnRunSerialisesConcurrentAdmissionAgainstSystemdFacts(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })

	var mu sync.Mutex
	active := false
	starts := 0
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch name {
		case "systemctl":
			if active {
				return []byte("minos-run-owner-repo-pr1.service loaded active running Minos lead\n"), nil
			}
			return nil, nil
		case "systemd-run":
			active = true
			starts++
			return nil, nil
		default:
			t.Fatalf("unexpected command %q", name)
			return nil, nil
		}
	}

	cfg := ServiceConfig{Root: "/etc/minos"}
	cfg.Runs.Dir = t.TempDir()
	cfg.Forges = map[string]ForgeConfig{"forgejo": {}}
	repo := RepoConfig{}
	repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"

	results := make(chan SpawnResult, 2)
	errors := make(chan error, 2)
	var group sync.WaitGroup
	for _, pr := range []string{"1", "2"} {
		group.Add(1)
		go func() {
			defer group.Done()
			outcome, err := SpawnRun(t.Context(), cfg, repo, Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: pr})
			results <- outcome
			errors <- err
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var outcomes []ReconcileDecision
	for outcome := range results {
		outcomes = append(outcomes, outcome.Outcome)
	}
	slices.Sort(outcomes)
	if !slices.Equal(outcomes, []ReconcileDecision{SpawnStarted, SpawnSuppressed}) {
		t.Fatalf("outcomes = %v, want one start and one suppression", outcomes)
	}
	if starts != 1 {
		t.Fatalf("systemd starts = %d, want one", starts)
	}
}

func assertArgument(t *testing.T, arguments []string, want string) {
	t.Helper()
	if !slices.Contains(arguments, want) {
		t.Fatalf("arguments omit %q: %v", want, arguments)
	}
}

func argumentValue(arguments []string, key string) string {
	prefix := key + "="
	for _, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			return strings.TrimPrefix(argument, prefix)
		}
	}
	return ""
}

func writeTestHandoff(t *testing.T, cfg ServiceConfig, facts Facts, runDir, head string, record json.RawMessage) string {
	t.Helper()
	path := handoffPath(cfg.Runs.Dir, UnitName(facts))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(runHandoff{
		Kind:        runHandoffKind,
		PullRequest: handoffPull{Owner: facts.Owner, Repo: facts.Repo, Number: facts.PR},
		Head:        head, RunDir: runDir, StoppedAt: "test boundary",
		WrittenAt: "2026-08-03T21:00:00Z", RunRecord: record,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunHandoffKeepsExistingShapeCompatibility(t *testing.T) {
	base := `{"kind":"minos-run-handoff-v1","pullRequest":{"owner":"owner","repo":"repository","number":"17"},"head":"head","runDir":"/runs/run","stoppedAt":"stage","writtenAt":"fixture","runRecord":{"round":0,"confirmedUnfixed":[]}}`
	tests := map[string]string{
		"unknown field":       strings.TrimSuffix(base, "}") + `,"futureField":true}`,
		"trailing content":    base + ` {"ignored":true}`,
		"empty existing data": `{"kind":"minos-run-handoff-v1","pullRequest":{"owner":"owner","repo":"repository","number":"17"},"head":"","runDir":"","stoppedAt":"","writtenAt":"fixture","runRecord":{"round":0,"confirmedUnfixed":[]}}`,
		"non-RFC timestamp":   base,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "handoff.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readRunHandoffStructure(path); err != nil {
				t.Fatalf("existing handoff shape rejected: %v", err)
			}
		})
	}
}

func TestRunHandoffRejectsInvalidProgress(t *testing.T) {
	base := `{"kind":"minos-run-handoff-v1","pullRequest":{"owner":"owner","repo":"repository","number":"17"},"head":"head","runDir":"/runs/run","stoppedAt":"stage","writtenAt":"fixture","runRecord":{"round":2,"confirmedUnfixed":[]},"progress":{"stage":"review","round":2,"head":"head","latestReview":7}}`
	tests := map[string]struct {
		old  string
		new  string
		want string
	}{
		"empty progress stage":         {`"stage":"review"`, `"stage":" "`, "progress.stage must be non-empty"},
		"negative progress round":      {`"progress":{"stage":"review","round":2`, `"progress":{"stage":"review","round":-1`, "progress.round must be a non-negative integer"},
		"empty progress head":          {`"head":"head","latestReview"`, `"head":" ","latestReview"`, "progress.head must be non-empty"},
		"negative latest review":       {`"latestReview":7`, `"latestReview":-1`, "progress.latestReview must be a non-negative integer"},
		"progress round mismatch":      {`"progress":{"stage":"review","round":2`, `"progress":{"stage":"review","round":3`, "progress.round must match runRecord.round"},
		"progress head mismatch":       {`"head":"head","latestReview"`, `"head":"other","latestReview"`, "progress.head must match head"},
		"predecessor without progress": {`"progress":`, `"predecessorProgress":`, "predecessorProgress requires progress"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			assertRejectedHandoff(t, strings.Replace(base, test.old, test.new, 1), test.want)
		})
	}
}

func TestRunHandoffRejectsMalformedConfirmedFixed(t *testing.T) {
	base := `{"kind":"minos-run-handoff-v1","pullRequest":{"owner":"owner","repo":"repository","number":"17"},"head":"head","runDir":"/runs/run","stoppedAt":"stage","writtenAt":"fixture","runRecord":{"round":0,"confirmedFixed":null,"confirmedUnfixed":[]}}`
	assertRejectedHandoff(t, base, "runRecord.confirmedFixed must be an array")
}

func TestRunHandoffRejectsInvalidPredecessorProgress(t *testing.T) {
	base := `{"kind":"minos-run-handoff-v1","pullRequest":{"owner":"owner","repo":"repository","number":"17"},"head":"head","runDir":"/runs/run","stoppedAt":"stage","writtenAt":"fixture","runRecord":{"round":2,"confirmedUnfixed":[]},"progress":{"stage":"review","round":2,"head":"head","latestReview":7},"predecessorProgress":{"stage":"review","round":1,"head":"old-head","latestReview":6}}`
	tests := map[string]struct {
		old  string
		new  string
		want string
	}{
		"empty stage":            {`"stage":"review","round":1`, `"stage":" ","round":1`, "predecessorProgress.stage must be non-empty"},
		"negative round":         {`"round":1,"head":"old-head"`, `"round":-1,"head":"old-head"`, "predecessorProgress.round must be a non-negative integer"},
		"empty head":             {`"head":"old-head","latestReview":6}}`, `"head":" ","latestReview":6}}`, "predecessorProgress.head must be non-empty"},
		"negative latest review": {`"latestReview":6}}`, `"latestReview":-1}}`, "predecessorProgress.latestReview must be a non-negative integer"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			assertRejectedHandoff(t, strings.Replace(base, test.old, test.new, 1), test.want)
		})
	}
}

func assertRejectedHandoff(t *testing.T, content, want string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readRunHandoffStructure(path)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want %q", err, want)
	}
}
