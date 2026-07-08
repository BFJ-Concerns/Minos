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
	target := dir + ".reaped-" + "1"
	if isReapedRunDir(target) == false {
		t.Fatal("reaped evidence directory should be recognised")
	}
}
