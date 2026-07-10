package shell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBeginRunClaimsOnlyCurrentUnfinishedHead(t *testing.T) {
	dir := t.TempDir()
	added := filepath.Join(dir, "added")
	writeScript(t, filepath.Join(dir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	writeScript(t, filepath.Join(dir, "get-pr-facts"), "#!/usr/bin/env sh\nprintf 'OCCASION=reconcile\\nOWNER=pump19\\nREPO=subject\\nPR=42\\nHEAD_SHA=abcdef\\nBASE_REF=main\\n'\n")
	writeScript(t, filepath.Join(dir, "add-label"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >'"+added+"'\n")

	if err := beginRun(context.Background(), Adaptation{Dir: dir}, Facts{Owner: "pump19", Repo: "subject", PR: "42", HeadSHA: "abcdef"}, RunReview); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(added)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Reviewing") {
		t.Fatalf("begin did not add Reviewing label:\n%s", data)
	}
}

func TestBeginRunYieldsBeforeLabelWhenHeadMoved(t *testing.T) {
	dir := t.TempDir()
	added := filepath.Join(dir, "added")
	writeScript(t, filepath.Join(dir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	writeScript(t, filepath.Join(dir, "get-pr-facts"), "#!/usr/bin/env sh\nprintf 'OCCASION=reconcile\\nOWNER=pump19\\nREPO=subject\\nPR=42\\nHEAD_SHA=new-head\\nBASE_REF=main\\n'\n")
	writeScript(t, filepath.Join(dir, "add-label"), "#!/usr/bin/env sh\nprintf called >'"+added+"'\n")

	if err := beginRun(context.Background(), Adaptation{Dir: dir}, Facts{Owner: "pump19", Repo: "subject", PR: "42", HeadSHA: "old-head"}, RunReview); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(added); !os.IsNotExist(err) {
		t.Fatalf("stale run added a label: %v", err)
	}
}

func TestReleaseRunRetainsLabelForNewerLiveRun(t *testing.T) {
	root := t.TempDir()
	facts := Facts{Forge: "local", Owner: "pump19", Repo: "subject", PR: "42", HeadSHA: "abcdef1234567890"}
	current := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
	newer := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "fedcba9876543210", RunReview)
	for _, dir := range []string{current, newer} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	writeRunMeta(t, current, facts.HeadSHA, started)
	writeRunMeta(t, newer, "fedcba9876543210", started.Add(time.Minute))
	t.Setenv("PUMP19_RUN_DIR", current)
	dir := t.TempDir()
	removed := filepath.Join(dir, "removed")
	writeScript(t, filepath.Join(dir, "remove-label"), "#!/usr/bin/env sh\nprintf called >'"+removed+"'\n")
	cfg := ServiceConfig{}
	cfg.Runs.Dir = root
	cfg.Sweep.LivenessThreshold.Duration = time.Hour

	if err := releaseRun(context.Background(), cfg, Adaptation{Dir: dir}, facts, RunReview); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(removed); !os.IsNotExist(err) {
		t.Fatalf("release removed a superseding run's label: %v", err)
	}
}
