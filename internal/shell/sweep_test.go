package shell

import (
	"os"
	"path/filepath"
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
	got, err := newestLiveRunDir(root, facts, RunReview)
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
	if err := os.Chtimes(oldRun, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(currentRun, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newRun, now, now); err != nil {
		t.Fatal(err)
	}
	if !newerLiveRunDirExists(root, facts, RunReview, currentRun) {
		t.Fatal("expected a later live run to count as newer")
	}
	if newerLiveRunDirExists(root, facts, RunReview, newRun) {
		t.Fatal("older live runs must not count as newer")
	}
}
