package shell

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBeginRunClaimsOnlyCurrentUnfinishedHead(t *testing.T) {
	dir := t.TempDir()
	runDir := t.TempDir()
	t.Setenv("MINOS_RUN_DIR", runDir)
	operations := filepath.Join(dir, "operations")
	writeScript(t, filepath.Join(dir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	writeScript(t, filepath.Join(dir, "get-pr-facts"), "#!/usr/bin/env sh\nprintf 'OCCASION=reconcile\\nOWNER=minos\\nREPO=subject\\nPR=42\\nHEAD_SHA=abcdef\\nBASE_REF=main\\n'\n")
	writeScript(t, filepath.Join(dir, "add-label"), "#!/usr/bin/env sh\nprintf 'add-label:%s\\n' \"$4\" >>'"+operations+"'\n")
	writeScript(t, filepath.Join(dir, "add-reaction"), "#!/usr/bin/env sh\nprintf 'add-reaction:%s\\n' \"$4\" >>'"+operations+"'\n")
	writeScript(t, filepath.Join(dir, "assign-if-missing"), "#!/usr/bin/env sh\nprintf 'assign-if-missing:%s\\n' \"$4\" >>'"+operations+"'\n")

	if err := beginRun(context.Background(), "TestBot", Adaptation{Dir: dir}, Facts{Owner: "minos", Repo: "subject", PR: "42", HeadSHA: "abcdef"}, RunReview); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(operations)
	if err != nil {
		t.Fatal(err)
	}
	want := "add-label:Reviewing\nadd-reaction:eyes\nassign-if-missing:TestBot\n"
	if string(data) != want {
		t.Fatalf("claim presence operations = %q, want %q", data, want)
	}
	if _, err := os.Stat(filepath.Join(runDir, forgeWritesAttemptedFile)); !os.IsNotExist(err) {
		t.Fatalf("replay-safe claim marked a publication write: %v", err)
	}
}

func TestBeginRunYieldsOnTerminalStatusButClaimsThroughPending(t *testing.T) {
	// A terminal minos/<kind> status on the head yields; a newest state of
	// "pending" is the operator's supersede marker and must admit a fresh claim
	// (a forge status cannot be deleted, only written over).
	cases := []struct {
		name   string
		states string // JSON array the fixture returns, newest by id
		want   string // "claimed" expects presence ops; "yield-terminal" expects none
	}{
		{"terminal-failure-yields", `[{"id":1,"context":"minos/review","state":"failure"}]`, "yield-terminal"},
		{"pending-supersede-claims", `[{"id":1,"context":"minos/review","state":"failure"},{"id":2,"context":"minos/review","state":"pending"}]`, "claimed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			runDir := t.TempDir()
			t.Setenv("MINOS_RUN_DIR", runDir)
			operations := filepath.Join(dir, "operations")
			writeScript(t, filepath.Join(dir, "get-statuses"), "#!/usr/bin/env sh\nprintf '%s\\n' '"+tc.states+"'\n")
			writeScript(t, filepath.Join(dir, "get-pr-facts"), "#!/usr/bin/env sh\nprintf 'OCCASION=reconcile\\nOWNER=minos\\nREPO=subject\\nPR=42\\nHEAD_SHA=abcdef\\nBASE_REF=main\\n'\n")
			writeScript(t, filepath.Join(dir, "add-label"), "#!/usr/bin/env sh\nprintf 'add-label:%s\\n' \"$4\" >>'"+operations+"'\n")
			writeScript(t, filepath.Join(dir, "add-reaction"), "#!/usr/bin/env sh\nprintf 'add-reaction:%s\\n' \"$4\" >>'"+operations+"'\n")
			writeScript(t, filepath.Join(dir, "assign-if-missing"), "#!/usr/bin/env sh\nprintf 'assign-if-missing:%s\\n' \"$4\" >>'"+operations+"'\n")

			outcome, err := claimRun(context.Background(), "TestBot", Adaptation{Dir: dir}, Facts{Owner: "minos", Repo: "subject", PR: "42", HeadSHA: "abcdef"}, RunReview)
			if err != nil {
				t.Fatal(err)
			}
			if outcome != tc.want {
				t.Fatalf("claim outcome = %q, want %q", outcome, tc.want)
			}
			_, opsErr := os.Stat(operations)
			if tc.want == "claimed" && opsErr != nil {
				t.Fatalf("a pending-superseded head did not claim presence: %v", opsErr)
			}
			if tc.want == "yield-terminal" && !os.IsNotExist(opsErr) {
				t.Fatalf("a terminal head claimed presence: %v", opsErr)
			}
		})
	}
}

func TestBeginRunYieldsBeforeLabelWhenHeadMoved(t *testing.T) {
	dir := t.TempDir()
	added := filepath.Join(dir, "added")
	writeScript(t, filepath.Join(dir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	writeScript(t, filepath.Join(dir, "get-pr-facts"), "#!/usr/bin/env sh\nprintf 'OCCASION=reconcile\\nOWNER=minos\\nREPO=subject\\nPR=42\\nHEAD_SHA=new-head\\nBASE_REF=main\\n'\n")
	writeScript(t, filepath.Join(dir, "add-label"), "#!/usr/bin/env sh\nprintf called >'"+added+"'\n")
	writeScript(t, filepath.Join(dir, "add-reaction"), "#!/usr/bin/env sh\nprintf called >'"+added+"'\n")
	writeScript(t, filepath.Join(dir, "assign-if-missing"), "#!/usr/bin/env sh\nprintf called >'"+added+"'\n")

	if err := beginRun(context.Background(), "TestBot", Adaptation{Dir: dir}, Facts{Owner: "minos", Repo: "subject", PR: "42", HeadSHA: "old-head"}, RunReview); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(added); !os.IsNotExist(err) {
		t.Fatalf("stale run added a label: %v", err)
	}
}

func TestReleaseRunRetainsLabelForNewerLiveRun(t *testing.T) {
	root := t.TempDir()
	facts := Facts{Forge: "local", Owner: "minos", Repo: "subject", PR: "42", HeadSHA: "abcdef1234567890"}
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
	t.Setenv("MINOS_RUN_DIR", current)
	dir := t.TempDir()
	removed := filepath.Join(dir, "removed")
	writeScript(t, filepath.Join(dir, "remove-label"), "#!/usr/bin/env sh\nprintf called >'"+removed+"'\n")
	writeScript(t, filepath.Join(dir, "remove-reaction"), "#!/usr/bin/env sh\nprintf called >'"+removed+"'\n")
	cfg := ServiceConfig{}
	cfg.Runs.Dir = root
	cfg.Sweep.LivenessThreshold.Duration = time.Hour

	if err := releaseRun(context.Background(), cfg, Adaptation{Dir: dir}, facts, RunReview); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(removed); !os.IsNotExist(err) {
		t.Fatalf("release removed a superseding run's presence: %v", err)
	}
}

func TestReleaseRunRemovesEyesBeforeStageLabel(t *testing.T) {
	root := t.TempDir()
	facts := Facts{Forge: "local", Owner: "minos", Repo: "subject", PR: "42", HeadSHA: "abcdef1234567890"}
	runDir := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunMeta(t, runDir, facts.HeadSHA, time.Now())
	t.Setenv("MINOS_RUN_DIR", runDir)

	adaptationDir := t.TempDir()
	operations := filepath.Join(adaptationDir, "operations")
	writeScript(t, filepath.Join(adaptationDir, "remove-reaction"), "#!/usr/bin/env sh\nprintf 'remove-reaction:%s\\n' \"$4\" >>'"+operations+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "remove-label"), "#!/usr/bin/env sh\nprintf 'remove-label:%s\\n' \"$4\" >>'"+operations+"'\n")
	cfg := ServiceConfig{}
	cfg.Runs.Dir = root
	cfg.Sweep.LivenessThreshold.Duration = time.Hour

	if err := releaseRun(context.Background(), cfg, Adaptation{Dir: adaptationDir}, facts, RunReview); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(operations)
	if err != nil {
		t.Fatal(err)
	}
	want := "remove-reaction:eyes\nremove-label:Reviewing\n"
	if string(data) != want {
		t.Fatalf("release operations = %q, want %q", data, want)
	}
	if _, err := os.Stat(filepath.Join(runDir, forgeWritesAttemptedFile)); !os.IsNotExist(err) {
		t.Fatalf("replay-safe release marked a publication write: %v", err)
	}
}

func TestReleaseRunPresenceKeepsStageLabelWhenEyesRemovalFails(t *testing.T) {
	adaptationDir := t.TempDir()
	removedLabel := filepath.Join(adaptationDir, "removed-label")
	writeScript(t, filepath.Join(adaptationDir, "remove-reaction"), "#!/usr/bin/env sh\nexit 1\n")
	writeScript(t, filepath.Join(adaptationDir, "remove-label"), "#!/usr/bin/env sh\nprintf called >'"+removedLabel+"'\n")
	facts := Facts{Owner: "minos", Repo: "subject", PR: "42"}

	if err := releaseRunPresence(t.Context(), Adaptation{Dir: adaptationDir}, facts, LabelReviewing); err == nil {
		t.Fatal("presence release succeeded after eyes removal failed")
	}
	if _, err := os.Stat(removedLabel); !os.IsNotExist(err) {
		t.Fatalf("stage label was removed without removing eyes: %v", err)
	}
}
