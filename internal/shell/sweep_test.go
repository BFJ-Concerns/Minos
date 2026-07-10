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
	if _, err := reapLabelLessClaims(t.Context(), cfg, Adaptation{}, facts, nil, logFile); err != nil {
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
	if _, err := reapLabelLessClaims(t.Context(), cfg, Adaptation{}, facts, nil, logFile); err != nil {
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
	if len(matches) != maxRetryableRunWrapRetries {
		t.Fatalf("retry evidence should be bounded by retry cap, got %v", matches)
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

func TestRetryableFailureExhaustionWritesErrorAndPreservesSecondAttempt(t *testing.T) {
	root := t.TempDir()
	cfg := ServiceConfig{}
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Sweep.LivenessThreshold.Duration = time.Hour
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "222222222222bbbb"}
	runDir := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunMeta(t, runDir, facts.HeadSHA, time.Now().Add(-2*time.Hour))
	writeQuietRunLog(t, runDir, time.Now().Add(-2*time.Hour))
	if err := os.WriteFile(filepath.Join(runDir, "retry.env"), []byte("PUMP19_RETRYABLE_FAILURE=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	retryDir := runDir + ".retry-1"
	if err := os.MkdirAll(retryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retryDir, "retry.env"), []byte("PUMP19_RETRYABLE_FAILURE=1\n"), 0o644); err != nil {
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
	if _, err := reapLabelLessClaims(t.Context(), cfg, Adaptation{Dir: adaptationDir}, facts, nil, logFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("exhausted retry claim should be discarded after error status: %v", err)
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
	if len(matches) != maxRetryableRunWrapRetries+1 {
		t.Fatalf("exhaustion should preserve exactly two attempts, got %v", matches)
	}
	data, err := os.ReadFile(statusFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, "pump19/review\nerror") {
		t.Fatalf("set-status args did not record retry exhaustion:\n%s", got)
	}
}

func TestSecondUnmarkedCrashWritesTerminalStatus(t *testing.T) {
	root := t.TempDir()
	cfg := ServiceConfig{}
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Sweep.LivenessThreshold.Duration = time.Hour
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7", HeadSHA: "222222222222bbbb"}
	runDir := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunMeta(t, runDir, facts.HeadSHA, time.Now().Add(-2*time.Hour))
	writeQuietRunLog(t, runDir, time.Now().Add(-2*time.Hour))
	if err := os.Mkdir(runDir+".reaped-1", 0o755); err != nil {
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

	statuses, err := reapLabelLessClaims(t.Context(), cfg, Adaptation{Dir: adaptationDir}, facts, nil, logFile)
	if err != nil {
		t.Fatal(err)
	}
	status, ok := statusForContext(statuses, "pump19/review")
	if !ok || status.State != "error" {
		t.Fatalf("second unmarked crash did not become terminal: %#v", statuses)
	}
	data, err := os.ReadFile(statusFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "pump19/review\nerror") {
		t.Fatalf("terminal status was not written:\n%s", data)
	}
}

func TestUnreadableRunMetaSkipsOnlyThatClaim(t *testing.T) {
	root := t.TempDir()
	cfg := ServiceConfig{}
	cfg.Runs.Dir = filepath.Join(root, "runs")
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
	if _, err := reapLabelLessClaims(t.Context(), cfg, Adaptation{}, facts, nil, logFile); err != nil {
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
