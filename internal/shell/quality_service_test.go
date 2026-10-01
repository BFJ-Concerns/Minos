package shell

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func qualityLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &output
}

func TestQualityRunCommandKeepsUnreadableContinuation(t *testing.T) {
	for _, fault := range []string{"marker read", "marker open", "handoff stat"} {
		t.Run(fault, func(t *testing.T) {
			output := qualityLog(t)
			runDir := t.TempDir()
			marker := filepath.Join(runDir, "lead-complete")
			handoff := filepath.Join(t.TempDir(), "handoff")
			writeStatusFile(t, filepath.Join(runDir, runOwnerMarker), "")
			switch fault {
			case "marker read":
				if err := os.Mkdir(marker, 0o700); err != nil {
					t.Fatal(err)
				}
			case "marker open":
				if err := os.Symlink(marker, marker); err != nil {
					t.Fatal(err)
				}
			case "handoff stat":
				writeStatusFile(t, marker, "continuation\n")
				if err := os.Symlink(handoff, handoff); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("MINOS_RUN_DIR", runDir)
			t.Setenv("MINOS_HANDOFF", handoff)
			t.Setenv("MINOS_RUN_BODY", "/bin/true")
			if err := RunCommand(t.Context(), nil); err != nil {
				t.Fatalf("cleanup changed successful body outcome: %v", err)
			}
			if _, err := os.Stat(runDir); err != nil {
				t.Fatalf("unreadable continuation directory was deleted: %v", err)
			}
			if !strings.Contains(output.String(), "continuation state unreadable") || !strings.Contains(output.String(), runDir) {
				t.Fatalf("continuation diagnostic = %q, want reason and run path", output.String())
			}
		})
	}
}

func TestQualitySpawnRollbackReportsFailedRestoration(t *testing.T) {
	for _, adopted := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "adopted"}[adopted], func(t *testing.T) {
			output := qualityLog(t)
			cfg, facts, predecessor, _ := reviewContinuationFixture(t)
			verdictPath := filepath.Join(predecessor, "review-result.json")
			writeStatusFile(t, verdictPath, `{"status":"complete","reviewed":{"head":"head"}}`)
			handoff := writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA)
			if !adopted {
				writeStatusFile(t, handoff, `{"kind":"wrong"}`)
			}
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name == "systemctl" {
					return nil, nil
				}
				if err := os.Mkdir(verdictPath, 0o700); err != nil {
					t.Fatal(err)
				}
				writeStatusFile(t, filepath.Join(verdictPath, "obstruction"), "blocks restoration")
				return []byte("start failed"), errors.New("spawn failed")
			}
			_, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts)
			if err == nil || !strings.Contains(err.Error(), "spawn failed") {
				t.Fatalf("primary spawn error = %v", err)
			}
			if !strings.Contains(output.String(), "spawn rollback: restore") || !strings.Contains(output.String(), verdictPath) {
				t.Fatalf("rollback diagnostic = %q, want failed restoration path", output.String())
			}
		})
	}
}

func TestQualitySpawnRollbackReportsOwnershipCleanupFailure(t *testing.T) {
	output := qualityLog(t)
	cfg, facts, predecessor, _ := reviewContinuationFixture(t)
	writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA)
	marker := filepath.Join(predecessor, runOwnerMarker)
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemctl" {
			return nil, nil
		}
		if err := os.Remove(marker); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(marker, 0o700); err != nil {
			t.Fatal(err)
		}
		writeStatusFile(t, filepath.Join(marker, "obstruction"), "blocks removal")
		return nil, errors.New("spawn failed")
	}
	if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err == nil {
		t.Fatal("spawn failure lost")
	}
	if !strings.Contains(output.String(), "remove ownership marker") || !strings.Contains(output.String(), marker) {
		t.Fatalf("rollback diagnostic = %q, want ownership cleanup path", output.String())
	}
}

func TestQualityHandoffStructuralFailuresAreReported(t *testing.T) {
	for _, fault := range []string{"malformed", "invalid shape", "unreadable"} {
		t.Run(fault, func(t *testing.T) {
			output := qualityLog(t)
			cfg, facts, predecessor, _ := reviewContinuationFixture(t)
			handoff := writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA)
			switch fault {
			case "malformed":
				writeStatusFile(t, handoff, "{")
			case "invalid shape":
				writeStatusFile(t, handoff, `{"kind":"wrong"}`)
			case "unreadable":
				if err := os.Remove(handoff); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(handoff, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			err := expireInactiveRunHandoffs(t.Context(), cfg, nil)
			if err == nil || !strings.Contains(err.Error(), filepath.Base(handoff)) {
				t.Errorf("expiry error = %v, want rejected handoff path", err)
			}
			preserved := validHandoffRunDirs(cfg, map[string]Facts{UnitName(facts): facts})
			if preserved[predecessor] {
				t.Fatal("invalid handoff was preserved")
			}
			if !strings.Contains(output.String(), handoff) || !strings.Contains(output.String(), "reject preservation handoff") {
				t.Errorf("preservation diagnostic = %q, want rejected handoff path", output.String())
			}
		})
	}
}

func TestQualityHandoffValidationFailureIsReported(t *testing.T) {
	output := qualityLog(t)
	cfg, facts, predecessor, _ := reviewContinuationFixture(t)
	facts.Owner = "own/r"
	handoff := writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA)
	facts.Owner = "own?r"
	validHandoffRunDirs(cfg, map[string]Facts{UnitName(facts): facts})
	if !strings.Contains(output.String(), handoff) || !strings.Contains(output.String(), "reject preservation handoff") {
		t.Fatalf("validation diagnostic = %q, want rejected handoff path", output.String())
	}
}

func TestQualitySpawnRollbackReportsDirectoryCleanupFailure(t *testing.T) {
	output := qualityLog(t)
	cfg, facts, _, _ := reviewContinuationFixture(t)
	t.Cleanup(func() { _ = os.Chmod(cfg.Runs.Dir, 0o700) })
	var runDir string
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" {
			return nil, nil
		}
		for _, arg := range args {
			if strings.HasPrefix(arg, "MINOS_RUN_DIR=") {
				runDir = strings.TrimPrefix(arg, "MINOS_RUN_DIR=")
			}
		}
		if runDir == "" {
			t.Fatal("spawn omitted run directory")
		}
		if err := os.Chmod(cfg.Runs.Dir, 0o500); err != nil {
			t.Fatal(err)
		}
		return nil, errors.New("spawn failed")
	}
	if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err == nil {
		t.Fatal("spawn failure lost")
	}
	if !strings.Contains(output.String(), "remove run directory") || !strings.Contains(output.String(), runDir) {
		t.Fatalf("rollback diagnostic = %q, want failed directory cleanup path", output.String())
	}
}

func TestQualityRunCommandReportsCleanupWithoutChangingOutcome(t *testing.T) {
	output := qualityLog(t)
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStatusFile(t, filepath.Join(runDir, runOwnerMarker), "")
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINOS_RUN_DIR", runDir)
	t.Setenv("MINOS_RUN_BODY", "/bin/true")
	if err := RunCommand(t.Context(), nil); err != nil {
		t.Fatalf("cleanup changed body outcome: %v", err)
	}
	if !strings.Contains(output.String(), "remove run directory") || !strings.Contains(output.String(), runDir) {
		t.Fatalf("cleanup diagnostic = %q, want failed removal path", output.String())
	}
}
