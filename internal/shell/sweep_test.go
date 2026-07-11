package shell

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewestLiveRunDirExcludesReapedEvidence(t *testing.T) {
	root := t.TempDir()
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7"}
	live := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "abcdef123456", RunReview)
	reaped := live + ".reaped-123"
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(reaped, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := newestLiveRunDir(root, facts, RunReview, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got != live {
		t.Fatalf("expected live dir %s, got %s", live, got)
	}
}

func TestLiveRunStateUsesDirectoryMTimeWhenLogMissing(t *testing.T) {
	root := t.TempDir()
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7"}
	live := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "abcdef123456", RunReview)
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(live, old, old); err != nil {
		t.Fatal(err)
	}
	dir, alive, err := liveRunState(root, facts, RunReview, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if dir != live || alive {
		t.Fatalf("expected stale live dir, got dir=%s alive=%v", dir, alive)
	}
}

func TestSweepReleasesEyesBeforeStageLabelOnEveryLabelledClaimRecovery(t *testing.T) {
	tests := []struct {
		name     string
		prepare  func(t *testing.T, runDir string)
		wantReap bool
	}{
		{
			name:    "orphaned",
			prepare: func(t *testing.T, runDir string) {},
		},
		{
			name: "retryable",
			prepare: func(t *testing.T, runDir string) {
				if err := os.MkdirAll(runDir, 0o755); err != nil {
					t.Fatal(err)
				}
				writeRunMeta(t, runDir, "abcdef1234567890", time.Now().Add(-2*time.Hour))
				writeQuietRunLog(t, runDir, time.Now().Add(-2*time.Hour))
				if err := os.WriteFile(filepath.Join(runDir, "retry.env"), []byte("PUMP19_RETRYABLE_FAILURE=1\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantReap: true,
		},
		{
			name: "dead",
			prepare: func(t *testing.T, runDir string) {
				if err := os.MkdirAll(runDir, 0o755); err != nil {
					t.Fatal(err)
				}
				writeRunMeta(t, runDir, "abcdef1234567890", time.Now().Add(-2*time.Hour))
				writeQuietRunLog(t, runDir, time.Now().Add(-2*time.Hour))
			},
			wantReap: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			cfg := ServiceConfig{}
			cfg.Runs.Dir = filepath.Join(root, "runs")
			cfg.Runs.MaxConcurrent = 2
			cfg.Sweep.LivenessThreshold.Duration = time.Hour
			facts := Facts{
				Forge:   "local",
				Owner:   "pump19",
				Repo:    "subject",
				PR:      "42",
				HeadSHA: "abcdef1234567890",
				Labels:  []string{LabelReviewing},
			}
			runDir := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
			tt.prepare(t, runDir)

			adaptationDir := filepath.Join(root, "adaptation")
			if err := os.Mkdir(adaptationDir, 0o755); err != nil {
				t.Fatal(err)
			}
			operations := filepath.Join(root, "operations")
			writeScript(t, filepath.Join(adaptationDir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
			writeScript(t, filepath.Join(adaptationDir, "remove-reaction"), "#!/usr/bin/env sh\nprintf 'remove-reaction:%s\\n' \"$4\" >>'"+operations+"'\n")
			writeScript(t, filepath.Join(adaptationDir, "remove-label"), "#!/usr/bin/env sh\nprintf 'remove-label:%s\\n' \"$4\" >>'"+operations+"'\n")
			logFile, err := os.Create(filepath.Join(root, "sweep.log"))
			if err != nil {
				t.Fatal(err)
			}

			err = sweepPR(t.Context(), cfg, RepoConfig{}, Adaptation{Dir: adaptationDir}, facts, logFile)
			if closeErr := logFile.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(operations)
			if err != nil {
				t.Fatal(err)
			}
			want := "remove-reaction:eyes\nremove-label:Reviewing\n"
			if string(data) != want {
				t.Fatalf("recovery operations = %q, want %q", data, want)
			}
			if tt.wantReap {
				if _, err := os.Stat(runDir); !os.IsNotExist(err) {
					t.Fatalf("canonical claim remains after recovery: %v", err)
				}
			}
		})
	}
}

func TestGuardsPassUsesStateDerivedActor(t *testing.T) {
	drafts := false
	repo := RepoConfig{Triggers: []TriggerRule{{
		Run:    "fix",
		Actors: []string{"pump19"},
		Drafts: &drafts,
	}}}
	facts := Facts{Draft: false}
	if !guardsPass(repo, RunFix, facts, "pump19") {
		t.Fatal("expected state-derived actor to satisfy fix guard")
	}
	if guardsPass(repo, RunFix, facts, "bob") {
		t.Fatal("unexpected human actor satisfying pump19-only fix guard")
	}
}

func TestSweepSuppressesLatchedHeadButNewHeadRuns(t *testing.T) {
	root := t.TempDir()
	cfg := ServiceConfig{}
	cfg.Root = root
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Runs.MaxConcurrent = 2
	cfg.Sweep.LivenessThreshold.Duration = time.Hour
	adaptationDir := filepath.Join(root, "adaptation")
	if err := os.MkdirAll(adaptationDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptationDir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	cfg.Forges = map[string]ForgeConfig{"local": {Adaptation: adaptationDir}}
	repo := RepoConfig{Forge: "local", Owner: "pump19", Repo: "subject", Triggers: []TriggerRule{{Run: "review", Authors: []string{"*"}}}}
	latched := Facts{Forge: "local", Owner: "pump19", Repo: "subject", PR: "42", HeadSHA: "aaaaaaaaaaaaaaaa", Author: "alice"}
	evidence := RunDir(cfg.Runs.Dir, latched.Forge, latched.Owner, latched.Repo, latched.PR, latched.HeadSHA, RunReview) + ".retry-5"
	if err := os.MkdirAll(evidence, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTerminalMarker(evidence, RunReview, latched.HeadSHA, "retry-exhausted"); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(root, "bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	spawned := filepath.Join(root, "spawned")
	writeScript(t, filepath.Join(fakeBin, "systemd-run"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >>'"+spawned+"'\n")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	logFile, err := os.Create(filepath.Join(root, "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	if err := sweepPR(t.Context(), cfg, repo, Adaptation{Dir: adaptationDir}, latched, logFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spawned); !os.IsNotExist(err) {
		t.Fatalf("latched same head spawned a run: %v", err)
	}

	fresh := latched
	fresh.HeadSHA = "bbbbbbbbbbbbbbbb"
	if err := sweepPR(t.Context(), cfg, repo, Adaptation{Dir: adaptationDir}, fresh, logFile); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(spawned)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "bbbbbbbbbbbb-review") {
		t.Fatalf("new head did not escape old latch:\n%s", data)
	}
}

func TestRetryWaveHonoursCapacityWithoutBurningDeferredAttempt(t *testing.T) {
	root := t.TempDir()
	activeCount := filepath.Join(root, "active-count")
	spawned := filepath.Join(root, "spawned")
	if err := os.WriteFile(activeCount, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	installAdmissionCommands(t, root, `
count=$(cat "$PUMP19_TEST_ACTIVE_COUNT")
i=0
while [ "$i" -lt "$count" ]; do
  printf 'pump19-run-%s.service loaded active running live\n' "$i"
  i=$((i + 1))
done
`, `
count=$(cat "$PUMP19_TEST_ACTIVE_COUNT")
count=$((count + 1))
printf '%s\n' "$count" >"$PUMP19_TEST_ACTIVE_COUNT"
printf 'spawned\n' >>"$PUMP19_TEST_SPAWNED"
`)
	t.Setenv("PUMP19_TEST_ACTIVE_COUNT", activeCount)
	t.Setenv("PUMP19_TEST_SPAWNED", spawned)

	cfg := ServiceConfig{Root: root}
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Runs.MaxConcurrent = 2
	cfg.Sweep.LivenessThreshold.Duration = time.Hour
	adaptationDir := filepath.Join(root, "adaptation")
	if err := os.MkdirAll(adaptationDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptationDir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "remove-reaction"), "#!/usr/bin/env sh\nexit 0\n")
	writeScript(t, filepath.Join(adaptationDir, "remove-label"), "#!/usr/bin/env sh\nexit 0\n")
	cfg.Forges = map[string]ForgeConfig{"local": {Adaptation: adaptationDir}}
	repo := RepoConfig{Forge: "local", Owner: "pump19", Repo: "subject", Triggers: []TriggerRule{{Run: "review", Authors: []string{"*"}}}}
	logPath := filepath.Join(root, "sweep.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}

	var runDirs []string
	var factsByRun []Facts
	for index, sha := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"} {
		facts := Facts{
			Forge: "local", Owner: "pump19", Repo: "subject", PR: string(rune('1' + index)),
			HeadSHA: sha, Author: "alice", Labels: []string{LabelReviewing},
		}
		factsByRun = append(factsByRun, facts)
		runDir := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
		runDirs = append(runDirs, runDir)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeRunMeta(t, runDir, sha, time.Now().Add(-2*time.Hour))
		writeQuietRunLog(t, runDir, time.Now().Add(-2*time.Hour))
		if err := os.WriteFile(filepath.Join(runDir, "retry.env"), []byte("PUMP19_RETRYABLE_FAILURE=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := sweepPR(t.Context(), cfg, repo, Adaptation{Dir: adaptationDir}, facts, logFile); err != nil {
			t.Fatal(err)
		}
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(spawned)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "spawned\n"); got != 2 {
		t.Fatalf("retry wave spawned %d runs, want 2", got)
	}
	for _, runDir := range runDirs[:2] {
		if _, err := os.Stat(runDir + ".retry-1"); err != nil {
			t.Fatalf("admitted retry was not archived at %s: %v", runDir, err)
		}
	}
	deferred := runDirs[2]
	if _, err := os.Stat(deferred); err != nil {
		t.Fatalf("deferred retry lost its canonical claim: %v", err)
	}
	if matches, err := filepath.Glob(deferred + ".retry-*"); err != nil || len(matches) != 0 {
		t.Fatalf("deferred retry burned attempt evidence: matches=%v err=%v", matches, err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "deferred review for pump19/subject#3 capacity=2") {
		t.Fatalf("sweep log lacks capacity deferral:\n%s", logData)
	}

	if err := os.WriteFile(activeCount, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logFile, err = os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := sweepPR(t.Context(), cfg, repo, Adaptation{Dir: adaptationDir}, factsByRun[2], logFile); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(deferred + ".retry-1"); err != nil {
		t.Fatalf("later sweep did not admit the deferred retry: %v", err)
	}
}

func TestReapTargetNameDoesNotOverwriteEvidence(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "run")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := reapedSuffix
	reapedSuffix = func() int64 { return 1 }
	t.Cleanup(func() {
		reapedSuffix = original
	})
	if err := os.Mkdir(dir+".reaped-1", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := reapRunDir(t.Context(), dir); err == nil {
		t.Fatal("expected reap to fail rather than overwrite existing evidence")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("canonical run dir should remain after collision: %v", err)
	}
}

func TestNewerLiveRunDirExistsMeansNewer(t *testing.T) {
	root := t.TempDir()
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7"}
	oldRun := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "111111111111", RunReview)
	currentRun := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "222222222222", RunReview)
	newRun := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "333333333333", RunReview)
	for _, dir := range []string{oldRun, currentRun, newRun} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	writeRunMeta(t, oldRun, "111111111111aaaa", now.Add(-2*time.Hour))
	writeRunMeta(t, currentRun, "222222222222bbbb", now.Add(-time.Hour))
	writeRunMeta(t, newRun, "333333333333cccc", now)
	// Directory mtimes can be stamped later by workspace preparation. Ordering
	// must follow meta.env, not the filesystem's latest incidental write.
	if err := os.Chtimes(oldRun, now.Add(time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !newerLiveRunDirExists(root, facts, RunReview, currentRun, time.Hour) {
		t.Fatal("expected a later live run to count as newer")
	}
	if newerLiveRunDirExists(root, facts, RunReview, newRun, time.Hour) {
		t.Fatal("older live runs must not count as newer")
	}
}

func TestNewestLiveRunDirUsesSubsecondStartedAt(t *testing.T) {
	root := t.TempDir()
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7"}
	lexicallyLaterOldRun := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "ffffffffffff", RunReview)
	lexicallyEarlierNewRun := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "111111111111", RunReview)
	for _, dir := range []string{lexicallyLaterOldRun, lexicallyEarlierNewRun} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	startedAt := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	writeRunMeta(t, lexicallyLaterOldRun, "ffffffffffffaaaa", startedAt)
	writeRunMeta(t, lexicallyEarlierNewRun, "111111111111bbbb", startedAt.Add(time.Nanosecond))

	got, err := newestLiveRunDir(root, facts, RunReview, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got != lexicallyEarlierNewRun {
		t.Fatalf("sub-second started_at should beat SHA/path text, got %s", got)
	}
}

func TestNewestLiveRunDirTreatsMissingMetadataAsNewest(t *testing.T) {
	root := t.TempDir()
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7"}
	metadatedRun := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "111111111111", RunReview)
	missingMetaRun := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "222222222222", RunReview)
	for _, dir := range []string{metadatedRun, missingMetaRun} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeRunMeta(t, metadatedRun, "111111111111aaaa", time.Now())

	got, err := newestLiveRunDir(root, facts, RunReview, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got != missingMetaRun {
		t.Fatalf("claim without meta.env should be treated as the live/newest claim, got %s", got)
	}
}

func TestNewestLiveRunDirDoesNotPreferStaleMissingMetadata(t *testing.T) {
	root := t.TempDir()
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7"}
	metadatedRun := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "111111111111", RunReview)
	missingMetaRun := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "222222222222", RunReview)
	for _, dir := range []string{metadatedRun, missingMetaRun} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeRunMeta(t, metadatedRun, "111111111111aaaa", time.Now())
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(missingMetaRun, old, old); err != nil {
		t.Fatal(err)
	}

	got, _, err := liveRunState(root, facts, RunReview, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got != metadatedRun {
		t.Fatalf("stale claim without meta.env outranked live metadata claim: %s", got)
	}
}

func TestLabelLessClaimReapsHistoricalHeadWithoutForgeReads(t *testing.T) {
	root := t.TempDir()
	cfg := ServiceConfig{}
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Runs.MaxConcurrent = 2
	cfg.Sweep.LivenessThreshold.Duration = time.Hour
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "222222222222bbbb"}
	oldRun := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, "111111111111aaaa", RunReview)
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(oldRun, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunMetaWithWorkspace(t, oldRun, "111111111111aaaa", time.Now().Add(-2*time.Hour), workspace)
	writeQuietRunLog(t, oldRun, time.Now().Add(-2*time.Hour))
	logFile, err := os.Create(filepath.Join(root, "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	if _, err := reapLabelLessClaims(t.Context(), cfg, RepoConfig{}, Adaptation{}, facts, nil, logFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldRun); !os.IsNotExist(err) {
		t.Fatalf("historical claim should release the canonical path: %v", err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("historical claim workspace should be removed: %v", err)
	}
	matches, err := filepath.Glob(oldRun + ".reaped-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one reaped evidence directory, got %v", matches)
	}
}

func TestRetryableFailureReleasePreservesBoundedEvidence(t *testing.T) {
	root := t.TempDir()
	cfg := ServiceConfig{}
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Runs.MaxConcurrent = 2
	cfg.Sweep.LivenessThreshold.Duration = time.Hour
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "222222222222bbbb"}
	runDir := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunMetaWithWorkspace(t, runDir, facts.HeadSHA, time.Now().Add(-2*time.Hour), workspace)
	writeQuietRunLog(t, runDir, time.Now().Add(-2*time.Hour))
	if err := os.WriteFile(filepath.Join(runDir, "retry.env"), []byte("PUMP19_RETRYABLE_FAILURE=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(root, "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	if _, err := reapLabelLessClaims(t.Context(), cfg, RepoConfig{}, Adaptation{}, facts, nil, logFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("retryable failure should release the canonical claim: %v", err)
	}
	retryDir := runDir + ".retry-1"
	if _, err := os.Stat(filepath.Join(retryDir, "retry.env")); err != nil {
		t.Fatalf("retry evidence should retain retry.env after release: %v", err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("retry release should clean the abandoned workspace: %v", err)
	}
	matches, err := filepath.Glob(runDir + ".retry-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("one expired attempt should leave one retry directory, got %v", matches)
	}
}

func TestConcurrentRetryReleaseIdentifiesTheLosingClaim(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			results <- releaseRetryableClaim(t.Context(), runDir, 1)
		}()
	}
	first, second := <-results, <-results
	if first != nil && second != nil {
		t.Fatalf("both retry releases failed: %v; %v", first, second)
	}
	loser := first
	if loser == nil {
		loser = second
	}
	if !errors.Is(loser, errRetryClaimAlreadyReleased) {
		t.Fatalf("losing release = %v, want retry-claim sentinel", loser)
	}
}

func TestRetryableFailurePreservesEveryAttemptWithoutExhaustionLatch(t *testing.T) {
	root := t.TempDir()
	cfg := ServiceConfig{}
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Runs.MaxConcurrent = 2
	cfg.Sweep.LivenessThreshold.Duration = time.Hour
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "222222222222bbbb"}
	runDir := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunMeta(t, runDir, facts.HeadSHA, time.Now().Add(-2*time.Hour))
	writeQuietRunLog(t, runDir, time.Now().Add(-2*time.Hour))
	if err := os.WriteFile(filepath.Join(runDir, "retry.env"), []byte("PUMP19_RETRYABLE_FAILURE=1\nPUMP19_FAILURE_AT=2026-07-11T09:00:00Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	retryDir := runDir + ".retry-1"
	if err := os.MkdirAll(retryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retryDir, "retry.env"), []byte("PUMP19_RETRYABLE_FAILURE=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(root, "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	if _, err := reapLabelLessClaims(t.Context(), cfg, RepoConfig{}, Adaptation{}, facts, nil, logFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("due retry claim should release the canonical path: %v", err)
	}
	if _, err := os.Stat(retryDir); err != nil {
		t.Fatalf("prior retry evidence should remain: %v", err)
	}
	secondRetryDir := runDir + ".retry-2"
	if _, err := os.Stat(filepath.Join(secondRetryDir, "retry.env")); err != nil {
		t.Fatalf("second attempt evidence should be preserved: %v", err)
	}
	matches, err := filepath.Glob(runDir + ".retry-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("retry evidence count = %d, want 2: %v", len(matches), matches)
	}
	if _, err := readTerminalMarker(secondRetryDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry evidence acquired a terminal latch: %v", err)
	}
}

func TestCrashAfterForgeWriteLatchesInternallyWithoutPRStatus(t *testing.T) {
	root := t.TempDir()
	cfg := ServiceConfig{}
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Runs.MaxConcurrent = 2
	cfg.Sweep.LivenessThreshold.Duration = time.Hour
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "222222222222bbbb"}
	runDir := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunMeta(t, runDir, facts.HeadSHA, time.Now().Add(-2*time.Hour))
	writeQuietRunLog(t, runDir, time.Now().Add(-2*time.Hour))
	if err := writeForgeWritesAttempted(runDir, "post-review"); err != nil {
		t.Fatal(err)
	}
	adaptationDir := filepath.Join(root, "adaptation")
	if err := os.Mkdir(adaptationDir, 0o755); err != nil {
		t.Fatal(err)
	}
	statusFile := filepath.Join(root, "status.args")
	writeScript(t, filepath.Join(adaptationDir, "set-status"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >'"+statusFile+"'\n")
	logFile, err := os.Create(filepath.Join(root, "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	statuses, err := reapLabelLessClaims(t.Context(), cfg, RepoConfig{}, Adaptation{Dir: adaptationDir}, facts, nil, logFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 0 {
		t.Fatalf("operational crash wrote in-memory PR status: %#v", statuses)
	}
	if _, err := os.Stat(statusFile); !os.IsNotExist(err) {
		t.Fatalf("operational crash wrote PR status: %v", err)
	}
	reaped, err := filepath.Glob(runDir + ".reaped-*")
	if err != nil || len(reaped) != 1 {
		t.Fatalf("reaped crash evidence = %v err=%v", reaped, err)
	}
	marker, err := readTerminalMarker(reaped[0])
	if err != nil || marker.Reason != "stale-after-forge-write" {
		t.Fatalf("terminal marker = %#v err=%v", marker, err)
	}
}

func TestUnreadableRunMetaSkipsOnlyThatClaim(t *testing.T) {
	root := t.TempDir()
	cfg := ServiceConfig{}
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Runs.MaxConcurrent = 2
	cfg.Sweep.LivenessThreshold.Duration = time.Hour
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "222222222222bbbb"}
	badRun := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, "111111111111aaaa", RunReview)
	goodRun := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
	for _, dir := range []string{badRun, goodRun} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeQuietRunLog(t, dir, time.Now().Add(-2*time.Hour))
	}
	if err := os.WriteFile(filepath.Join(badRun, "meta.env"), []byte("PUMP19_STARTED_AT=plainly-not-a-time\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRunMeta(t, goodRun, facts.HeadSHA, time.Now().Add(-2*time.Hour))
	logPath := filepath.Join(root, "sweep.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reapLabelLessClaims(t.Context(), cfg, RepoConfig{}, Adaptation{}, facts, nil, logFile); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(badRun); err != nil {
		t.Fatalf("bad metadata claim should remain for inspection: %v", err)
	}
	if _, err := os.Stat(goodRun); !os.IsNotExist(err) {
		t.Fatalf("good stale claim should still be reaped: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "skip claim with unreadable meta") {
		t.Fatalf("expected unreadable metadata skip in log, got:\n%s", data)
	}
}

func writeRunMeta(t *testing.T, runDir, headSHA string, startedAt time.Time) {
	t.Helper()
	writeRunMetaWithWorkspace(t, runDir, headSHA, startedAt, "")
}

func writeRunMetaWithWorkspace(t *testing.T, runDir, headSHA string, startedAt time.Time, workspace string) {
	t.Helper()
	data := "PUMP19_HEAD_SHA=" + headSHA + "\nPUMP19_STARTED_AT=" + startedAt.UTC().Format(time.RFC3339Nano) + "\n"
	if workspace != "" {
		data += "PUMP19_WORKSPACE=" + workspace + "\n"
	}
	if err := os.WriteFile(filepath.Join(runDir, "meta.env"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeQuietRunLog(t *testing.T, runDir string, at time.Time) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(runDir, "run.log"), []byte("quiet\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(runDir, "run.log"), at, at); err != nil {
		t.Fatal(err)
	}
}
