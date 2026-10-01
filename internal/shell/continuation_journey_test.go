package shell

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"bfj/minos/internal/forge"
)

func TestContinuationProgressBoundaryStopsOnlyAStalledSuccessor(t *testing.T) {
	t.Run("stalled stop removes the configured in-flight label", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		defineMarkerLabels(state)
		state.mu.Lock()
		state.setPullRequestLabels(state.repositoryLabels[1:2])
		state.mu.Unlock()
		state.reactions = []string{"eyes"}
		cfg, repo, facts := state.service(t)
		repo.Markers.InFlight = &forge.Marker{Label: "minos/reviewing"}
		facts.HeadSHA, facts.BaseSHA = state.headSHA(), state.targetSHA()
		progress := handoffProgress{Stage: "review", Head: facts.HeadSHA, LatestReview: 7}
		writeProgressHandoff(t, cfg, facts, progress, &progress)
		cfg.Runs.FailureLog = filepath.Join(t.TempDir(), "failures.log")

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "systemctl" {
				return nil, nil
			}
			t.Fatalf("stalled continuation invoked %s", name)
			return nil, nil
		}

		outcome, err := SpawnRun(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Outcome != SpawnAttention {
			t.Fatalf("outcome = %q, want %q", outcome, SpawnAttention)
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if !slices.Equal(state.writeSequence, []string{"status:Changes need attention", "label-remove:minos/reviewing"}) {
			t.Fatalf("guarded writes = %v, want the in-flight label removed and no reaction touched", state.writeSequence)
		}
		if labels := state.pullRequestLabelNames(); len(labels) != 0 || !slices.Equal(state.reactions, []string{"eyes"}) {
			t.Fatalf("labels = %v, reactions = %v", labels, state.reactions)
		}
	})

	t.Run("same stage and publication ends as attention", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, facts := state.service(t)
		facts.HeadSHA, facts.BaseSHA = state.headSHA(), state.targetSHA()
		state.reactions = []string{"eyes"}
		progress := handoffProgress{Stage: "review", Head: facts.HeadSHA, LatestReview: 7}
		runDir, handoffFile := writeProgressHandoff(t, cfg, facts, progress, &progress)
		failureLog := filepath.Join(t.TempDir(), "failures.log")
		cfg.Runs.FailureLog = failureLog

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "systemctl" {
				return nil, nil
			}
			t.Fatalf("stalled continuation invoked %s", name)
			return nil, nil
		}

		outcome, err := SpawnRun(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Outcome != SpawnAttention {
			t.Fatalf("outcome = %q, want %q", outcome, SpawnAttention)
		}
		wantDetail := `successor made no progress beyond stage "review" and published no new head or review`
		if outcome.Detail != wantDetail {
			t.Fatalf("detail = %q, want %q", outcome.Detail, wantDetail)
		}
		if !slices.Equal(state.writeSequence, []string{"status:Changes need attention", "reaction-remove:eyes"}) {
			t.Fatalf("guarded writes = %v", state.writeSequence)
		}
		assertContainsFile(t, failureLog, "pull_request="+facts.Owner+"/"+facts.Repo+"#"+facts.PR)
		assertContainsFile(t, failureLog, "stage=continuation-progress")
		assertContainsFile(t, failureLog, "published no new head or review")
		failureData, err := os.ReadFile(failureLog)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^timestamp=[^ ]+ pull_request=[^ ]+ head=[^ ]+ stage=continuation-progress cause=.+\n$`).Match(failureData) {
			t.Fatalf("failure line has unexpected shape: %q", failureData)
		}
		assertContainsFile(t, filepath.Join(runDir, "lead-complete"), "non-clean")
		if _, err := os.Stat(handoffFile); !os.IsNotExist(err) {
			t.Fatalf("stalled handoff remains or stat failed: %v", err)
		}
	})

	t.Run("missing run directory still ends as attention", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, facts := state.service(t)
		facts.HeadSHA, facts.BaseSHA = state.headSHA(), state.targetSHA()
		state.reactions = []string{"eyes"}
		progress := handoffProgress{Stage: "review", Head: facts.HeadSHA, LatestReview: 7}
		runDir, handoffFile := writeProgressHandoff(t, cfg, facts, progress, &progress)
		if err := os.RemoveAll(runDir); err != nil {
			t.Fatal(err)
		}
		failureLog := filepath.Join(t.TempDir(), "failures.log")
		cfg.Runs.FailureLog = failureLog

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "systemctl" {
				return nil, nil
			}
			t.Fatalf("stalled continuation with missing run directory invoked %s", name)
			return nil, nil
		}

		outcome, err := SpawnRun(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Outcome != SpawnAttention {
			t.Fatalf("outcome = %q, want %q", outcome, SpawnAttention)
		}
		if !slices.Equal(state.writeSequence, []string{"status:Changes need attention", "reaction-remove:eyes"}) {
			t.Fatalf("guarded writes = %v", state.writeSequence)
		}
		assertContainsFile(t, failureLog, "stage=continuation-progress")
		if _, err := os.Stat(handoffFile); !os.IsNotExist(err) {
			t.Fatalf("stalled handoff remains or stat failed: %v", err)
		}
	})

	t.Run("moved pull request head permits a successor", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, facts := state.service(t)
		facts.HeadSHA, facts.BaseSHA = state.headSHA(), state.targetSHA()
		oldFacts := facts
		oldFacts.HeadSHA = "old-head-sha"
		progress := handoffProgress{Stage: "review", Head: oldFacts.HeadSHA, LatestReview: 7}
		_, _ = writeProgressHandoff(t, cfg, oldFacts, progress, &progress)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		starts := 0
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "systemctl" {
				return nil, nil
			}
			starts++
			return nil, nil
		}

		outcome, err := SpawnRun(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Outcome != SpawnStarted || starts != 1 {
			t.Fatalf("moved-head outcome = %q, starts = %d; want %q and one start", outcome, starts, SpawnStarted)
		}
		if len(state.writeSequence) != 0 {
			t.Fatalf("moved-head continuation wrote terminal products: %v", state.writeSequence)
		}
	})

	t.Run("failure log is best effort", func(t *testing.T) {
		for _, test := range []struct {
			name string
			path func(*testing.T) string
		}{
			{name: "unset", path: func(*testing.T) string { return "" }},
			{name: "unwritable", path: func(t *testing.T) string { return t.TempDir() }},
		} {
			t.Run(test.name, func(t *testing.T) {
				state := newForgejoFixtureState(t)
				cfg, repo, facts := state.service(t)
				facts.HeadSHA, facts.BaseSHA = state.headSHA(), state.targetSHA()
				state.reactions = []string{"eyes"}
				progress := handoffProgress{Stage: "review", Head: facts.HeadSHA, LatestReview: 7}
				_, handoffFile := writeProgressHandoff(t, cfg, facts, progress, &progress)
				cfg.Runs.FailureLog = test.path(t)

				original := commandCombinedOutput
				t.Cleanup(func() { commandCombinedOutput = original })
				commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
					if name == "systemctl" {
						return nil, nil
					}
					t.Fatalf("stalled continuation invoked %s", name)
					return nil, nil
				}

				outcome, err := SpawnRun(t.Context(), cfg, repo, facts)
				if err != nil {
					t.Fatal(err)
				}
				if outcome.Outcome != SpawnAttention {
					t.Fatalf("outcome = %q, want %q", outcome, SpawnAttention)
				}
				if !slices.Equal(state.writeSequence, []string{"status:Changes need attention", "reaction-remove:eyes"}) {
					t.Fatalf("guarded writes = %v", state.writeSequence)
				}
				if _, err := os.Stat(handoffFile); !os.IsNotExist(err) {
					t.Fatalf("stalled handoff remains or stat failed: %v", err)
				}
			})
		}
	})

	t.Run("empty run directory writes no completion marker", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, facts := state.service(t)
		facts.HeadSHA, facts.BaseSHA = state.headSHA(), state.targetSHA()
		state.reactions = []string{"eyes"}
		progress := handoffProgress{Stage: "review", Head: facts.HeadSHA, LatestReview: 7}
		_, handoffFile := writeProgressHandoff(t, cfg, facts, progress, &progress)
		data, err := os.ReadFile(handoffFile)
		if err != nil {
			t.Fatal(err)
		}
		var handoff runHandoff
		if err := json.Unmarshal(data, &handoff); err != nil {
			t.Fatal(err)
		}
		handoff.RunDir = ""
		data, err = json.Marshal(handoff)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(handoffFile, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.Runs.FailureLog = ""
		workDir := t.TempDir()
		t.Chdir(workDir)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "systemctl" {
				return nil, nil
			}
			t.Fatalf("stalled continuation invoked %s", name)
			return nil, nil
		}

		outcome, err := SpawnRun(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Outcome != SpawnAttention {
			t.Fatalf("outcome = %q, want %q", outcome, SpawnAttention)
		}
		if _, err := os.Stat(filepath.Join(workDir, "lead-complete")); !os.IsNotExist(err) {
			t.Fatalf("empty run directory wrote lead-complete in the working directory: %v", err)
		}
	})

	t.Run("foreign run directory writes no completion marker", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, facts := state.service(t)
		facts.HeadSHA, facts.BaseSHA = state.headSHA(), state.targetSHA()
		state.reactions = []string{"eyes"}
		progress := handoffProgress{Stage: "review", Head: facts.HeadSHA, LatestReview: 7}
		_, handoffFile := writeProgressHandoff(t, cfg, facts, progress, &progress)
		foreignRunDir := filepath.Join(cfg.Runs.Dir, "foreign-unit-existing")
		if err := os.MkdirAll(foreignRunDir, 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(handoffFile)
		if err != nil {
			t.Fatal(err)
		}
		var handoff runHandoff
		if err := json.Unmarshal(data, &handoff); err != nil {
			t.Fatal(err)
		}
		handoff.RunDir = foreignRunDir
		data, err = json.Marshal(handoff)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(handoffFile, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.Runs.FailureLog = ""

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "systemctl" {
				return nil, nil
			}
			t.Fatalf("stalled continuation invoked %s", name)
			return nil, nil
		}

		outcome, err := SpawnRun(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Outcome != SpawnAttention {
			t.Fatalf("outcome = %q, want %q", outcome, SpawnAttention)
		}
		if !slices.Equal(state.writeSequence, []string{"status:Changes need attention", "reaction-remove:eyes"}) {
			t.Fatalf("guarded writes = %v", state.writeSequence)
		}
		if _, err := os.Stat(filepath.Join(foreignRunDir, "lead-complete")); !os.IsNotExist(err) {
			t.Fatalf("foreign run directory received lead-complete: %v", err)
		}
		if _, err := os.Stat(handoffFile); !os.IsNotExist(err) {
			t.Fatalf("stalled handoff remains or stat failed: %v", err)
		}
	})

	tests := []struct {
		name        string
		predecessor *handoffProgress
		current     handoffProgress
	}{
		{name: "different stage", predecessor: &handoffProgress{Stage: "review", Head: "head", LatestReview: 7}, current: handoffProgress{Stage: "fix", Head: "head", LatestReview: 7}},
		{name: "published head", predecessor: &handoffProgress{Stage: "review", Head: "old-head", LatestReview: 7}, current: handoffProgress{Stage: "review", Head: "head", LatestReview: 7}},
		{name: "published review", predecessor: &handoffProgress{Stage: "review", Head: "head", LatestReview: 7}, current: handoffProgress{Stage: "review", Head: "head", LatestReview: 8}},
		{name: "legacy progress is unknown", current: handoffProgress{Stage: "review", Head: "head", LatestReview: 7}},
	}
	for _, test := range tests {
		t.Run(test.name+" permits a successor", func(t *testing.T) {
			state := newForgejoFixtureState(t)
			cfg, repo, facts := state.service(t)
			facts.HeadSHA, facts.BaseSHA = state.headSHA(), state.targetSHA()
			test.current.Head = facts.HeadSHA
			if test.predecessor != nil && test.predecessor.Head == "head" {
				test.predecessor.Head = facts.HeadSHA
			}
			_, _ = writeProgressHandoff(t, cfg, facts, test.current, test.predecessor)

			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			starts := 0
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name == "systemctl" {
					return nil, nil
				}
				starts++
				return nil, nil
			}
			outcome, err := SpawnRun(t.Context(), cfg, repo, facts)
			if err != nil {
				t.Fatal(err)
			}
			if test.predecessor != nil && outcome.Outcome != SpawnContinued {
				t.Fatalf("outcome = %q, want %q", outcome, SpawnContinued)
			}
			if test.predecessor == nil && outcome.Outcome != SpawnStarted {
				t.Fatalf("unknown legacy outcome = %q, want %q", outcome, SpawnStarted)
			}
			if starts != 1 {
				t.Fatalf("outcome = %q, starts = %d", outcome, starts)
			}
			if len(state.writeSequence) != 0 {
				t.Fatalf("advancing continuation wrote terminal products: %v", state.writeSequence)
			}
		})
	}
}

func writeProgressHandoff(t *testing.T, cfg ServiceConfig, facts Facts, progress handoffProgress, predecessor *handoffProgress) (string, string) {
	t.Helper()
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA)
	data, err := json.Marshal(runHandoff{
		Kind: runHandoffKind, PullRequest: handoffPull{Owner: facts.Owner, Repo: facts.Repo, Number: facts.PR},
		Head: facts.HeadSHA, RunDir: runDir, StoppedAt: progress.Stage, WrittenAt: "2026-08-03T21:00:00Z",
		Predecessor: predecessor, Progress: &progress,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return runDir, path
}

func TestPreservedContinuationStartsAWorkingSuccessor(t *testing.T) {
	fixture := newRunBodyFixture(t)
	cfg := ServiceConfig{Root: fixture.configRoot}
	setTestRunCeilings(&cfg)
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
	handoff := writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA)

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
