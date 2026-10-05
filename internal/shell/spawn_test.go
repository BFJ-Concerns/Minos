package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/BFJ-Concerns/Minos/deploy"
	"github.com/BFJ-Concerns/Minos/internal/forge"
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
	setTestRunCeilings(&cfg)
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
	setTestRunCeilings(&cfg)
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

func TestSpawnRunAdmitsUpToTheConfiguredConcurrency(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })

	var active []string
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			listing := ""
			for _, unit := range active {
				listing += unit + " loaded active running Minos lead\n"
			}
			return []byte(listing), nil
		case "systemd-run":
			unitFlag := slices.Index(args, "--unit")
			active = append(active, args[unitFlag+1]+".service")
			return nil, nil
		default:
			t.Fatalf("unexpected command %q", name)
			return nil, nil
		}
	}

	cfg := ServiceConfig{Root: "/etc/minos", Forges: map[string]ForgeConfig{"forgejo": {}}}
	setTestRunCeilings(&cfg)
	cfg.Runs.Dir = t.TempDir()
	cfg.Runs.MaxConcurrent = 2
	repo := RepoConfig{}
	repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"

	var outcomes []ReconcileDecision
	for _, pr := range []string{"1", "2", "3"} {
		result, err := SpawnRun(t.Context(), cfg, repo, Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: pr})
		if err != nil {
			t.Fatal(err)
		}
		outcomes = append(outcomes, result.Outcome)
		if pr == "3" && result.BlockingUnit != "minos-run-forgejo-owner-repo-pr1.service" {
			t.Fatalf("blocking unit = %q, want the lowest-named active unit", result.BlockingUnit)
		}
		if pr == "3" {
			want := "all 2/2 run slots are occupied by active units minos-run-forgejo-owner-repo-pr1.service, minos-run-forgejo-owner-repo-pr2.service"
			if result.Detail != want {
				t.Fatalf("cap suppression detail = %q, want %q", result.Detail, want)
			}
		}
	}
	if !slices.Equal(outcomes, []ReconcileDecision{SpawnStarted, SpawnStarted, SpawnSuppressed}) {
		t.Fatalf("outcomes = %v, want two starts then a suppression", outcomes)
	}
}

func TestSpawnRunSuppressesOwnLiveUnitWithoutConsumingItsHandoff(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })

	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "head"}
	unit := UnitName(facts)
	var commands []string
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		commands = append(commands, name)
		return []byte(unit + ".service loaded active running Minos lead\n"), nil
	}

	cfg := ServiceConfig{Root: "/etc/minos", Forges: map[string]ForgeConfig{"forgejo": {}}}
	setTestRunCeilings(&cfg)
	cfg.Runs.Dir = t.TempDir()
	cfg.Runs.MaxConcurrent = 2
	repo := RepoConfig{}
	repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"
	runDir := filepath.Join(cfg.Runs.Dir, unit+"-1")
	if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	handoffFile := writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA)

	result, err := SpawnRun(t.Context(), cfg, repo, facts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != SpawnSuppressed || result.BlockingUnit != unit+".service" {
		t.Fatalf("result = %+v, want suppression by its own live unit", result)
	}
	if !slices.Equal(commands, []string{"systemctl"}) {
		t.Fatalf("commands = %v, want only the active-unit check", commands)
	}
	if _, err := os.Stat(handoffFile); err != nil {
		t.Fatalf("continuation handoff was consumed by a suppressed admission: %v", err)
	}
}

func TestSpawnRunSharesTheMemoryEnvelopeBetweenConcurrentRuns(t *testing.T) {
	for _, maxConcurrent := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("max-concurrent-%d", maxConcurrent), func(t *testing.T) {
			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			var systemdArgs []string
			commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "systemd-run" {
					systemdArgs = args
				}
				return nil, nil
			}

			cfg := ServiceConfig{Root: "/etc/minos", Forges: map[string]ForgeConfig{"forgejo": {}}}
			setTestRunCeilings(&cfg)
			cfg.Runs.Dir = t.TempDir()
			cfg.Runs.MaxConcurrent = maxConcurrent
			repo := RepoConfig{}
			repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"
			if _, err := SpawnRun(t.Context(), cfg, repo, Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "1"}); err != nil {
				t.Fatal(err)
			}
			assertArgument(t, systemdArgs, "--slice=minos-runs.slice")
			assertArgument(t, systemdArgs, "--property=MemoryMax=22G")
		})
	}
}

func TestSpawnRunReportsRequestedUnitForSystemdRunRace(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemctl" {
			return nil, nil
		}
		return []byte("Unit minos-run-forgejo-owner-repo-pr1.service already exists."), errors.New("exit status 1")
	}

	cfg := ServiceConfig{Root: "/etc/minos", Forges: map[string]ForgeConfig{"forgejo": {}}}
	setTestRunCeilings(&cfg)
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
	if result.BlockingUnit != "minos-run-forgejo-owner-repo-pr1.service" {
		t.Fatalf("blocking unit = %q", result.BlockingUnit)
	}
}

func TestSpawnRunRefusesContinuationDirectorySymlinkEscapingRunsDirectory(t *testing.T) {
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

	cfg := ServiceConfig{Root: "/etc/minos", Forges: map[string]ForgeConfig{"forgejo": {}}}
	setTestRunCeilings(&cfg)
	cfg.Runs.Dir = t.TempDir()
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "head"}
	escaped := filepath.Join(t.TempDir(), UnitName(facts)+"-preserved")
	if err := os.MkdirAll(filepath.Join(escaped, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	apparent := filepath.Join(cfg.Runs.Dir, filepath.Base(escaped))
	if err := os.Symlink(escaped, apparent); err != nil {
		t.Fatal(err)
	}
	writeTestHandoff(t, cfg, facts, apparent, facts.HeadSHA)

	stderr := captureStderr(t, func() {
		if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, "continuation workspace not reused") || !strings.Contains(stderr, "runDir is not a direct, unit-named child of runs.dir") {
		t.Fatalf("continuation refusal = %q", stderr)
	}
	if got := argumentValue(systemdArgs, "MINOS_RUN_DIR"); got == "" || got == apparent {
		t.Fatalf("fresh run directory = %q, escaping continuation = %q", got, apparent)
	}
	if slices.Contains(systemdArgs, "MINOS_RESUME=true") {
		t.Fatalf("escaping continuation unexpectedly resumed: %v", systemdArgs)
	}
}

func TestAdoptableRunDirectoryReportsMissingDirectory(t *testing.T) {
	cfg := ServiceConfig{}
	cfg.Runs.Dir = t.TempDir()
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "head"}
	handoff := &runHandoff{RunDir: filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-reaped")}

	reason, ok := adoptableRunDirectory(cfg, UnitName(facts), facts, handoff)
	if ok || reason != "runDir does not exist as a directory" {
		t.Fatalf("adoption = (%q, %t), want missing-directory refusal", reason, ok)
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
			handoffFile := writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA)
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
				t.Fatalf("ordinary review result can still masquerade as a carried verdict: %v", err)
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
	handoffFile := writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA)
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
	firstHandoff := writeTestHandoff(t, cfg, facts, firstPredecessor, facts.HeadSHA)
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

	secondHandoff := writeTestHandoff(t, cfg, facts, firstSuccessor, facts.HeadSHA)
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

func TestSpawnRunDemotesHeadMismatchedVerdictsForAdoptedContinuation(t *testing.T) {
	for _, source := range []string{"carried-review-result.json", "review-result.json"} {
		t.Run(source, func(t *testing.T) {
			cfg, facts, predecessor, systemdArgs := reviewContinuationFixture(t)
			verdict := []byte(`{"status":"complete","reviewed":{"head":"old-head"},"findings":[]}`)
			if err := os.WriteFile(filepath.Join(predecessor, source), verdict, 0o600); err != nil {
				t.Fatal(err)
			}
			writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA)

			stderr := captureStderr(t, func() {
				if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err != nil {
					t.Fatal(err)
				}
			})
			if strings.Contains(stderr, "adopted completed review verdict") {
				t.Fatalf("stderr falsely reports stale carry adoption: %q", stderr)
			}
			successor := argumentValue(*systemdArgs, "MINOS_RUN_DIR")
			if successor != predecessor {
				t.Fatalf("successor = %q, want adopted directory %q", successor, predecessor)
			}
			preserved, err := os.ReadFile(filepath.Join(successor, staleReviewResultName))
			if err != nil || string(preserved) != string(verdict) {
				t.Fatalf("stale verdict = %s, err = %v; want preserved %s", preserved, err, verdict)
			}
			if _, err := os.Stat(filepath.Join(successor, source)); !os.IsNotExist(err) {
				t.Fatalf("head-mismatched verdict still competes as %s: %v", source, err)
			}
			if _, err := os.Stat(filepath.Join(successor, "carried-review-result.json")); !os.IsNotExist(err) {
				t.Fatalf("head-mismatched verdict remains carried or stat failed: %v", err)
			}
		})
	}
}

func TestSpawnRunPrefersExactVerdictOverLeftoverStaleCarry(t *testing.T) {
	cfg, facts, predecessor, systemdArgs := reviewContinuationFixture(t)
	if err := os.WriteFile(filepath.Join(predecessor, staleReviewResultName),
		[]byte(`{"status":"complete","reviewed":{"head":"old-head"},"findings":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	verdict := []byte(`{"status":"complete","reviewed":{"head":"head"},"findings":[]}`)
	if err := os.WriteFile(filepath.Join(predecessor, "review-result.json"), verdict, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA)

	stderr := captureStderr(t, func() {
		if _, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, "adopted completed review verdict") {
		t.Fatalf("stderr does not report exact verdict adoption: %q", stderr)
	}
	successor := argumentValue(*systemdArgs, "MINOS_RUN_DIR")
	carried, err := os.ReadFile(filepath.Join(successor, "carried-review-result.json"))
	if err != nil || string(carried) != string(verdict) {
		t.Fatalf("successor verdict = %s, err = %v; want %s", carried, err, verdict)
	}
	if _, err := os.Stat(filepath.Join(successor, staleReviewResultName)); !os.IsNotExist(err) {
		t.Fatalf("superseded stale carry survives beside an exact one: %v", err)
	}
}

func TestSpawnRunRemovesHeadMismatchedCarryWhenHandoffIsRejected(t *testing.T) {
	cfg, facts, predecessor, systemdArgs := reviewContinuationFixture(t)
	stale := filepath.Join(predecessor, "carried-review-result.json")
	if err := os.WriteFile(stale, []byte(`{"status":"complete","reviewed":{"head":"old-head"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	handoff := writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA)
	if err := os.WriteFile(handoff, []byte(`{"kind":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
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
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale carry remains after admission or stat failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(successor, "carried-review-result.json")); !os.IsNotExist(err) {
		t.Fatalf("stale carry remains successor-visible or stat failed: %v", err)
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
	handoff := writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA)
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
	handoff := writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA)
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
	writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA)
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
		name       string
		content    []byte
		unreadable bool
	}{
		{name: "absent"},
		{name: "unreadable", unreadable: true},
		{name: "malformed", content: []byte(`{"status":`)},
		{name: "non-object", content: []byte(`[]`)},
		{name: "non-complete", content: []byte(`{"status":"incomplete","reviewed":{"head":"head"}}`)},
		{name: "different head", content: []byte(`{"status":"complete","reviewed":{"head":"foreign"}}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, facts, predecessor, systemdArgs := reviewContinuationFixture(t)
			path := filepath.Join(predecessor, "review-result.json")
			if test.unreadable {
				// A directory at the verdict path fails the read for any
				// caller, root included — chmod 0 does not bind for root.
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if test.content != nil {
				if err := os.WriteFile(path, test.content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writeTestHandoff(t, cfg, facts, predecessor, facts.HeadSHA)
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
	setTestRunCeilings(&cfg)
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
	writerClosed := false
	os.Stderr = writer
	defer func() {
		os.Stderr = original
		if !writerClosed {
			if err := writer.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	defer func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	}()
	action()
	os.Stderr = original
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writerClosed = true
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func TestSpawnRunRejectsMalformedHandoffAndStartsFresh(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemctl" {
			return nil, nil
		}
		return nil, nil
	}
	cfg := ServiceConfig{Root: "/etc/minos"}
	setTestRunCeilings(&cfg)
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
	setTestRunCeilings(&cfg)
	cfg.Runs.Dir = t.TempDir()
	cfg.Forges = map[string]ForgeConfig{"forgejo": {}}
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "head"}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA)
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

	cfg := loadServiceConfigWith(t, t.TempDir(), "")
	cfg.Root = "/etc/minos"
	cfg.Runs.Dir = t.TempDir()
	cfg.Service.CommitAuthorName = "Reviewer Bot"
	cfg.Service.CommitAuthorEmail = "reviewer@example.invalid"
	cfg.Service.StatusContext = "Review Bot"
	cfg.Routing.Verifier = RoleRouting{Engine: "codex", Model: "gpt-6-astra", Effort: "low"}
	cfg.Ensemble.ConcurrencyClaude = 10
	cfg.Ensemble.ConcurrencyCodex = 6
	cfg.Ensemble.AgentCeiling = 12
	cfg.Forges = map[string]ForgeConfig{
		"forgejo": {APIBase: "http://forge.local", CredentialFile: "/etc/minos/forge.token"},
	}
	repo := RepoConfig{}
	repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"
	repo.Review.Threshold = "Medium"
	repo.GuidanceSources = []GuidanceSource{
		{Repository: "owner/repo-plans", Path: "README.md"},
		{Path: "docs/intent.md"},
	}
	repo.FilingDestination = FilingDestination{Kind: FilingKindFile, Repository: "owner/repo-plans", Path: "ISSUES.md"}
	repo.Markers = Markers{
		InFlight:  &forge.Marker{Label: "minos/reviewing"},
		Clean:     &forge.Marker{Reaction: "+1"},
		Attention: &forge.Marker{Label: "minos/attention"},
	}
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
	assertArgument(t, systemdArgs, "--property=RuntimeMaxSec=43200.000000000s")
	assertArgument(t, systemdArgs, "--slice=minos-runs.slice")
	assertArgument(t, systemdArgs, "--property=MemoryMax=22G")
	assertArgument(t, systemdArgs, "--property=OnSuccess=minos-sweep-after-run@minos-run-forgejo-owner-repo-pr7.service")
	for _, argument := range systemdArgs {
		if strings.HasPrefix(argument, "--property=OnFailure=") {
			t.Fatalf("run unit must not trigger reconciliation on failure: %q", argument)
		}
	}
	environment := systemdEnvironment(t, systemdArgs)
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
		"MINOS_RUN_BODY=/opt/minos/run-body/run-body",
		"MINOS_REVIEW_THRESHOLD=Medium",
		"MINOS_COMMIT_AUTHOR_NAME=Reviewer Bot",
		"MINOS_COMMIT_AUTHOR_EMAIL=reviewer@example.invalid",
		"MINOS_STATUS_CONTEXT=Review Bot",
		`MINOS_GUIDANCE_SOURCES=[{"repository":"owner/repo-plans","path":"README.md"},{"path":"docs/intent.md"}]`,
		`MINOS_FILING_DESTINATION={"kind":"file","repository":"owner/repo-plans","path":"ISSUES.md"}`,
		`MINOS_ROUTING={"verifier":{"engine":"codex","model":"gpt-6-astra","effort":"low"}}`,
		"ENSEMBLE_CONCURRENCY_CLAUDE=10",
		"ENSEMBLE_CONCURRENCY_CODEX=6",
		"ENSEMBLE_AGENT_CEILING=12",
	} {
		name, want, _ := strings.Cut(value, "=")
		if got, present := environment[name]; !present || got != want {
			t.Fatalf("run environment %s = %q, present = %t; want %q", name, got, present, want)
		}
	}
	// The markers knob is asserted through the --setenv pairs themselves,
	// so a value that reached the arguments without becoming run
	// environment fails here.
	if markers := environment["MINOS_MARKERS"]; markers != `{"in-flight":{"label":"minos/reviewing"},"clean":{"reaction":"+1"},"attention":{"label":"minos/attention"}}` {
		t.Fatalf("run environment MINOS_MARKERS = %q", markers)
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
	assertArgument(t, systemdArgs, "MINOS_HANDOFF="+handoffPath(cfg.Runs.Dir, UnitName(facts)))
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

func TestSpawnRunExportsUnsetStructuredKnobsAsTheirEmptyForms(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	var systemdArgs []string
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemd-run" {
			systemdArgs = append([]string(nil), args...)
		}
		return nil, nil
	}
	cfg := ServiceConfig{Root: "/etc/minos"}
	setTestRunCeilings(&cfg)
	cfg.Runs.Dir = t.TempDir()
	cfg.Ensemble.ConcurrencyClaude = 1
	cfg.Ensemble.ConcurrencyCodex = 1
	cfg.Forges = map[string]ForgeConfig{"forgejo": {}}
	repo := RepoConfig{}
	repo.FilingDestination = FilingDestination{Kind: FilingKindNone}
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "head", BaseSHA: "target"}

	if _, err := SpawnRun(t.Context(), cfg, repo, facts); err != nil {
		t.Fatal(err)
	}
	assertArgument(t, systemdArgs, "MINOS_GUIDANCE_SOURCES=[]")
	assertArgument(t, systemdArgs, `MINOS_FILING_DESTINATION={"kind":"none"}`)
	assertArgument(t, systemdArgs, "MINOS_ROUTING={}")
}

func TestSpawnRunHoldsConcurrentAdmissionToTheConfiguredCount(t *testing.T) {
	for _, test := range []struct {
		name          string
		maxConcurrent int
		racers        []string
		want          []ReconcileDecision
	}{
		{name: "one", maxConcurrent: 1, racers: []string{"1", "2"}, want: []ReconcileDecision{SpawnStarted, SpawnSuppressed}},
		{name: "two", maxConcurrent: 2, racers: []string{"1", "2", "3"}, want: []ReconcileDecision{SpawnStarted, SpawnStarted, SpawnSuppressed}},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })

			var mu sync.Mutex
			var active []string
			commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
				mu.Lock()
				defer mu.Unlock()
				switch name {
				case "systemctl":
					listing := ""
					for _, unit := range active {
						listing += unit + " loaded active running Minos lead\n"
					}
					return []byte(listing), nil
				case "systemd-run":
					unitFlag := slices.Index(args, "--unit")
					active = append(active, args[unitFlag+1]+".service")
					return nil, nil
				default:
					t.Fatalf("unexpected command %q", name)
					return nil, nil
				}
			}

			cfg := ServiceConfig{Root: "/etc/minos"}
			setTestRunCeilings(&cfg)
			cfg.Runs.Dir = t.TempDir()
			cfg.Runs.MaxConcurrent = test.maxConcurrent
			cfg.Forges = map[string]ForgeConfig{"forgejo": {}}
			repo := RepoConfig{}
			repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"

			results := make(chan SpawnResult, len(test.racers))
			errors := make(chan error, len(test.racers))
			var group sync.WaitGroup
			for _, pr := range test.racers {
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
			slices.Sort(test.want)
			if !slices.Equal(outcomes, test.want) {
				t.Fatalf("outcomes = %v, want %v", outcomes, test.want)
			}
			if len(active) != test.maxConcurrent {
				t.Fatalf("systemd starts = %d, want %d", len(active), test.maxConcurrent)
			}
		})
	}
}

// The exit hook names an instance of a template the binary ships. The user
// manager keeps a unit loaded while it is active or failed, and while an
// exit hook it fired names a unit that stays loaded; the exited run unit is
// collected only once the instance is, so the template must unload when it
// fails and must fire no exit hook of its own.
func TestSpawnRunExitHookNamesTheShippedSweepTemplate(t *testing.T) {
	hook := sweepAfterRunUnit("minos-run-forgejo-owner-repo-pr7")
	template, instance, found := strings.Cut(hook, "@")
	if !found || instance != "minos-run-forgejo-owner-repo-pr7.service" {
		t.Fatalf("exit hook %q is not an instance named after the run unit", hook)
	}
	content, err := fs.ReadFile(deploy.Units, "systemd/user/"+template+"@.service")
	if err != nil {
		t.Fatalf("the exit hook names a template the binary does not ship: %v", err)
	}
	if !strings.Contains(string(content), "\nExecStart=/usr/bin/systemctl --user start --no-block minos-sweep.service\n") {
		t.Fatalf("the shipped template does not enqueue the canonical sweep and return:\n%s", content)
	}
	if !strings.Contains(string(content), "\nCollectMode=inactive-or-failed\n") {
		t.Fatalf("the shipped template keeps a failed instance loaded, and the exited run unit with it:\n%s", content)
	}
	for _, directive := range []string{"OnSuccess=", "OnFailure="} {
		if strings.Contains(string(content), "\n"+directive) {
			t.Fatalf("the shipped template declares %s; a fired exit hook naming a unit that stays loaded keeps the instance, and through it the exited run unit, loaded:\n%s", directive, content)
		}
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

func writeTestHandoff(t *testing.T, cfg ServiceConfig, facts Facts, runDir, head string) string {
	t.Helper()
	path := handoffPath(cfg.Runs.Dir, UnitName(facts))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(runHandoff{
		Kind:        runHandoffKind,
		PullRequest: handoffPull{Owner: facts.Owner, Repo: facts.Repo, Number: facts.PR},
		Head:        head, RunDir: runDir, StoppedAt: "test boundary",
		WrittenAt: "2026-08-03T21:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunHandoffShapeToleranceMatchesTheDeclaredContract(t *testing.T) {
	base := `{"kind":"minos-run-handoff-v1","pullRequest":{"owner":"owner","repo":"repository","number":"17"},"head":"head","runDir":"/runs/run","stoppedAt":"stage","writtenAt":"fixture"}`
	accepted := map[string]string{
		"trailing content":    base + ` {"ignored":true}`,
		"empty existing data": `{"kind":"minos-run-handoff-v1","pullRequest":{"owner":"owner","repo":"repository","number":"17"},"head":"","runDir":"","stoppedAt":"","writtenAt":"fixture"}`,
		"non-RFC timestamp":   base,
	}
	for name, content := range accepted {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "handoff.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := readRunHandoffStructure(path)
			if err != nil {
				t.Fatalf("declared handoff shape rejected: %v", err)
			}
		})
	}
	// The lifecycle tells the lead field names are validated strictly. A
	// misspelled optional field silently dropping progress is the hazard the
	// strict decode removes; schema evolution rides the versioned kind.
	t.Run("unknown field", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "handoff.json")
		content := strings.TrimSuffix(base, "}") + `,"futureField":true}`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readRunHandoffStructure(path); err == nil {
			t.Fatal("unknown handoff field was accepted")
		}
	})
}

func TestRunHandoffRejectsInvalidProgress(t *testing.T) {
	base := `{"kind":"minos-run-handoff-v1","pullRequest":{"owner":"owner","repo":"repository","number":"17"},"head":"head","runDir":"/runs/run","stoppedAt":"stage","writtenAt":"fixture","progress":{"stage":"review","head":"head","latestReview":7}}`
	tests := map[string]struct {
		old  string
		new  string
		want string
	}{
		"empty progress stage":         {`"stage":"review"`, `"stage":" "`, "progress.stage must be non-empty"},
		"empty progress head":          {`"head":"head","latestReview"`, `"head":" ","latestReview"`, "progress.head must be non-empty"},
		"negative latest review":       {`"latestReview":7`, `"latestReview":-1`, "progress.latestReview must be a non-negative integer"},
		"progress head mismatch":       {`"head":"head","latestReview"`, `"head":"other","latestReview"`, "progress.head must match head"},
		"predecessor without progress": {`"progress":`, `"predecessorProgress":`, "predecessorProgress requires progress"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			assertRejectedHandoff(t, strings.Replace(base, test.old, test.new, 1), test.want)
		})
	}
}

func TestRunHandoffRejectsInvalidPredecessorProgress(t *testing.T) {
	base := `{"kind":"minos-run-handoff-v1","pullRequest":{"owner":"owner","repo":"repository","number":"17"},"head":"head","runDir":"/runs/run","stoppedAt":"stage","writtenAt":"fixture","progress":{"stage":"review","head":"head","latestReview":7},"predecessorProgress":{"stage":"review","head":"old-head","latestReview":6}}`
	tests := map[string]struct {
		old  string
		new  string
		want string
	}{
		"empty stage":            {`"stage":"review","head":"old-head"`, `"stage":" ","head":"old-head"`, "predecessorProgress.stage must be non-empty"},
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

func TestSpawnRunUsesConfiguredRunCeilings(t *testing.T) {
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

	root := t.TempDir()
	contents := strings.Replace(testServiceConfig, "[runs]\n", "[runs]\nmemory-envelope-gib = 8\nduration-ceiling = \"90m\"\npressure-threshold-percent = 60\n", 1)
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Runs.Dir = t.TempDir()
	cfg.Forges = map[string]ForgeConfig{
		"forgejo": {APIBase: "http://forge.local", CredentialFile: "/etc/minos/forge.token"},
	}

	repo := RepoConfig{}
	repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"
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
	for _, argument := range []string{
		"--property=MemoryMax=8G",
		"--property=RuntimeMaxSec=5400.000000000s",
		"MINOS_PRESSURE_THRESHOLD_PERCENT=60",
	} {
		t.Run(argument, func(t *testing.T) { assertArgument(t, systemdArgs, argument) })
	}
}

// Fatal test actions use Goexit, which must still restore the shared stderr.
func TestCaptureStderrRestoresAfterGoexit(t *testing.T) {
	original := os.Stderr
	var captured *os.File
	var pipe string
	var inspectErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		captureStderr(t, func() {
			captured = os.Stderr
			if runtime.GOOS == "linux" {
				pipe, inspectErr = os.Readlink(fmt.Sprintf("/proc/self/fd/%d", captured.Fd()))
			}
			runtime.Goexit()
		})
	}()
	<-done
	// Restore even if the assertion fails, so this regression cannot poison peers.
	t.Cleanup(func() { os.Stderr = original })
	if os.Stderr != original {
		t.Fatal("stderr was not restored after Goexit")
	}
	if _, err := captured.WriteString("after exit"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("capture writer after Goexit = %v, want closed pipe", err)
	}
	if runtime.GOOS != "linux" {
		return // Stderr and writer closure remain covered on every platform.
	}
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if os.IsNotExist(err) { // ReadDir's own descriptor may already be closed.
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if target == pipe {
			t.Fatalf("capture pipe %s remains open at descriptor %s after Goexit", pipe, entry.Name())
		}
	}
}

func TestSpawnRunExportsForgeWebBase(t *testing.T) {
	for _, webBase := range []string{"https://forge.example/forge/", ""} {
		t.Run(fmt.Sprintf("web-base=%q", webBase), func(t *testing.T) {
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
			cfg := loadServiceConfigWith(t, t.TempDir(), "")
			cfg.Runs.Dir = t.TempDir()
			forge := cfg.Forges["local"]
			forge.WebBase = webBase
			cfg.Forges["local"] = forge
			repo := RepoConfig{}
			repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"
			facts := Facts{Forge: "local", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "head", BaseSHA: "target"}
			if _, err := SpawnRun(t.Context(), cfg, repo, facts); err != nil {
				t.Fatal(err)
			}
			env := systemdEnvironment(t, systemdArgs)
			if got, present := env["MINOS_WEB_BASE"]; !present || got != webBase {
				t.Fatalf("MINOS_WEB_BASE = %q, present = %t; want %q", got, present, webBase)
			}
		})
	}
}

func TestUnitNameIncludesForge(t *testing.T) {
	first := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7"}
	second := first
	second.Forge = "github"
	if UnitName(first) == UnitName(second) {
		t.Fatalf("two forges collide at unit %q", UnitName(first))
	}
	if got := UnitName(first); got != "minos-run-forgejo-owner-repo-pr7" {
		t.Fatalf("unit name = %q, want forge-key prefix", got)
	}
}

func TestSpawnRunSeparatesForgeNamespaces(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	var active, runDirs, handoffs []string
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			var listing strings.Builder
			for _, unit := range active {
				matched, err := filepath.Match(args[len(args)-1], unit)
				if err != nil {
					t.Fatal(err)
				}
				if matched {
					fmt.Fprintf(&listing, "%s loaded active running Minos lead\n", unit)
				}
			}
			return []byte(listing.String()), nil
		case "systemd-run":
			for i, arg := range args {
				if arg == "--unit" && i+1 < len(args) {
					active = append(active, args[i+1]+".service")
				}
				if strings.HasPrefix(arg, "MINOS_RUN_DIR=") {
					runDirs = append(runDirs, strings.TrimPrefix(arg, "MINOS_RUN_DIR="))
				}
				if strings.HasPrefix(arg, "MINOS_HANDOFF=") {
					handoffs = append(handoffs, strings.TrimPrefix(arg, "MINOS_HANDOFF="))
				}
			}
			return nil, nil
		default:
			t.Fatalf("unexpected command %q", name)
			return nil, nil
		}
	}
	cfg := ServiceConfig{Root: "/etc/minos", Forges: map[string]ForgeConfig{"forgejo": {}, "github": {}}}
	setTestRunCeilings(&cfg)
	cfg.Runs.MaxConcurrent = 2
	cfg.Runs.Dir = t.TempDir()
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7"}
	for _, key := range []string{"forgejo", "github"} {
		facts.Forge = key
		result, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts)
		if err != nil || result.Outcome != SpawnStarted {
			t.Fatalf("spawn on %s = %#v, err=%v, want separate live run", key, result, err)
		}
	}
	if len(runDirs) != 2 || runDirs[0] == runDirs[1] || len(handoffs) != 2 || handoffs[0] == handoffs[1] {
		t.Fatalf("forge namespaces overlap: run directories=%v handoffs=%v", runDirs, handoffs)
	}
	for i, key := range []string{"forgejo", "github"} {
		prefix := "minos-run-" + key + "-owner-repo-pr7"
		if !strings.HasPrefix(filepath.Base(runDirs[i]), prefix+"-") || filepath.Base(handoffs[i]) != prefix+".json" {
			t.Fatalf("run directories=%v handoffs=%v omit forge key %s", runDirs, handoffs, key)
		}
	}
	result, err := SpawnRun(t.Context(), cfg, RepoConfig{}, facts)
	if err != nil || result.Outcome != SpawnSuppressed || result.BlockingUnit != active[1] || len(active) != 2 {
		t.Fatalf("new-name active unit detection = %#v, err=%v, active=%v", result, err, active)
	}
}
