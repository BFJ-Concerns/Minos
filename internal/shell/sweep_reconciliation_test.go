package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSweepDerivesFixFromCurrentHeadBotReview(t *testing.T) {
	repo := RepoConfig{Triggers: []TriggerRule{{Run: "fix", Actors: []string{serviceBotLogin}}}}
	facts := Facts{HeadSHA: "abcdef1234567890"}
	reviews := []Review{{ID: 17, State: "REQUEST_CHANGES", CommitID: facts.HeadSHA, User: serviceBotLogin}}

	action, ok := decideSweepAction(repo, facts, nil, reviews, "", "")
	if !ok || action.Run != RunFix {
		t.Fatalf("review-record action = %#v ok=%v, want fix", action, ok)
	}
	standingFacts := facts
	standingFacts.Labels = []string{LabelStandingFindings}
	if action, ok := decideSweepAction(repo, standingFacts, nil, reviews, "", ""); ok {
		t.Fatalf("Standing Findings unexpectedly re-fired action %#v", action)
	}

	fixContext, _ := StatusContext(RunFix)
	action, ok = decideSweepAction(repo, facts, []Status{{ID: 19, Context: fixContext, State: "success"}}, reviews, "", "")
	if ok {
		t.Fatalf("existing fix status did not suppress action: %#v", action)
	}
}

func TestConvergenceFirstDispatchAtCapAndBlockedDrainFallback(t *testing.T) {
	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	adaptationDir := filepath.Join(root, "adaptation")
	for _, dir := range []string{configRoot, filepath.Join(configRoot, "repos"), adaptationDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("fixture-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(root, "runs")
	sweepLog := filepath.Join(root, "sweep.log")
	service := "[listener]\nbind = \":0\"\n\n[forges.local]\nadaptation = \"" + adaptationDir + "\"\napi-base = \"http://forge.invalid\"\nwebhook-secret-file = \"" + secret + "\"\ncredential-file = \"" + secret + "\"\n\n[runs]\ndir = \"" + runs + "\"\nmax-concurrent = 1\n\n[sweep]\nliveness-threshold = \"1h\"\nlog = \"" + sweepLog + "\"\n"
	if err := os.WriteFile(filepath.Join(configRoot, "service.toml"), []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := "forge = \"local\"\nowner = \"pump19\"\nrepo = \"subject\"\n\n[adaptation]\nbuild = \"true\"\ntest = \"true\"\nskill = \"fixture\"\n\n[[trigger]]\nrun = \"review\"\non = [\"pr-opened\"]\nauthors = [\"*\"]\n"
	if err := os.WriteFile(filepath.Join(configRoot, "repos", "local--pump19--subject.toml"), []byte(repo), 0o644); err != nil {
		t.Fatal(err)
	}

	writeScript(t, filepath.Join(adaptationDir, "list-open-prs"), `#!/usr/bin/env sh
cat <<'EOF'
OCCASION=reconcile
OWNER=pump19
REPO=subject
PR=2
HEAD_SHA=bbbbbbbbbbbbbbbb
BASE_REF=main
AUTHOR=bob
DRAFT=false

OCCASION=reconcile
OWNER=pump19
REPO=subject
PR=1
HEAD_SHA=aaaaaaaaaaaaaaaa
BASE_REF=main
AUTHOR=alice
DRAFT=false
LABELS=Standing Findings
EOF
`)
	writeScript(t, filepath.Join(adaptationDir, "get-statuses"), `#!/usr/bin/env sh
if [ "$3" = aaaaaaaaaaaaaaaa ]; then
  printf '[{"id":7,"context":"pump19/fix","state":"success","creator":"Minos"}]\n'
else
  printf '[]\n'
fi
`)
	writeScript(t, filepath.Join(adaptationDir, "list-reviews"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")

	drainFacts := Facts{Forge: "local", Owner: "pump19", Repo: "subject", PR: "1", HeadSHA: "aaaaaaaaaaaaaaaa"}
	terminalDir := RunDir(runs, drainFacts.Forge, drainFacts.Owner, drainFacts.Repo, drainFacts.PR, drainFacts.HeadSHA, RunReview) + ".retry-1"
	if err := os.MkdirAll(terminalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTerminalMarker(terminalDir, RunReview, drainFacts.HeadSHA, "controlled-failure"); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(root, "bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	spawned := filepath.Join(root, "spawned")
	writeScript(t, filepath.Join(fakeBin, "systemctl"), "#!/usr/bin/env sh\nif [ -s '"+spawned+"' ]; then printf 'pump19-run-fixture.service loaded active running fixture\\n'; fi\n")
	writeScript(t, filepath.Join(fakeBin, "systemd-run"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >>'"+spawned+"'\n")
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	if err := SweepCommand(t.Context(), []string{"--config", configRoot}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(spawned)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "aaaaaaaaaaaa-review") || !strings.Contains(string(data), "bbbbbbbbbbbb-review") {
		t.Fatalf("blocked drain candidate prevented or replaced next dispatch:\n%s", data)
	}
	logData, err := os.ReadFile(sweepLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(logData), "terminal marker suppresses review for pump19/subject#1") > strings.Index(string(logData), "reconcile fires review for pump19/subject#2") {
		t.Fatalf("widen dispatch ran before higher-ranked blocked drain candidate:\n%s", logData)
	}

	// Remove only the explicit terminal latch and repeat with an empty admission
	// ledger. The freshly fixed head now takes the sole slot before the newer,
	// untouched PR can begin its first review.
	if err := os.Remove(filepath.Join(terminalDir, terminalMarkerFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(spawned); err != nil {
		t.Fatal(err)
	}
	if err := SweepCommand(t.Context(), []string{"--config", configRoot}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(spawned)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "aaaaaaaaaaaa-review") || strings.Contains(string(data), "bbbbbbbbbbbb-review") {
		t.Fatalf("sole admission slot did not go to the convergence run:\n%s", data)
	}
}

func TestSweepRetriesDeferredReadyOnlyWhenEveryGuardPasses(t *testing.T) {
	repo := RepoConfig{Policy: struct {
		AutoMerge bool `toml:"auto-merge"`
	}{AutoMerge: true}}
	facts := Facts{HeadSHA: "abcdef1234567890"}
	reviews := []Review{{ID: 21, State: "APPROVED", CommitID: facts.HeadSHA, User: serviceBotLogin}}

	for _, combined := range []string{"", "success"} {
		action, ok := decideSweepAction(repo, facts, nil, reviews, combined, "")
		if !ok || !action.ApplyReady {
			t.Fatalf("combined=%q action = %#v ok=%v, want Ready repair", combined, action, ok)
		}
	}

	blocked := []struct {
		name     string
		labels   []string
		combined string
	}{
		{name: "red head", combined: "failure"},
		{name: "pending head", combined: "pending"},
		{name: "ready already present", labels: []string{LabelReady}, combined: "success"},
		{name: "flaky pause arrived after approval", labels: []string{LabelFlakyTests}, combined: "success"},
	}
	for _, tt := range blocked {
		t.Run(tt.name, func(t *testing.T) {
			blockedFacts := facts
			blockedFacts.Labels = tt.labels
			if action, ok := decideSweepAction(repo, blockedFacts, nil, reviews, tt.combined, ""); ok {
				t.Fatalf("blocked Ready repair produced %#v", action)
			}
		})
	}
}

func TestSweepDispatchRanksDrainBeforeWidenAndOldestAmongPeers(t *testing.T) {
	fixContext, _ := StatusContext(RunFix)
	candidates := []sweepCandidate{
		{snapshot: sweepSnapshot{facts: Facts{PR: "40"}}},
		{snapshot: sweepSnapshot{facts: Facts{PR: "20"}, statuses: []Status{{Context: fixContext, State: "success"}}}},
		{snapshot: sweepSnapshot{facts: Facts{PR: "10", Labels: []string{LabelStandingFindings}}}},
		{snapshot: sweepSnapshot{facts: Facts{PR: "30"}}},
	}
	for index := range candidates {
		candidates[index].priority = classifySweepPriority(candidates[index].snapshot)
	}
	rankSweepCandidates(candidates)
	want := []string{"10", "20", "30", "40"}
	for index, pr := range want {
		if candidates[index].snapshot.facts.PR != pr {
			t.Fatalf("ranked PRs[%d] = %s, want %s", index, candidates[index].snapshot.facts.PR, pr)
		}
	}
}

func TestRetryBackoffUsesFailureMarkerAcrossSweepPasses(t *testing.T) {
	runDir := t.TempDir()
	failureAt := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	data := "PUMP19_RETRYABLE_FAILURE=1\nPUMP19_FAILURE_AT=" + failureAt.Format(time.RFC3339Nano) + "\n"
	if err := os.WriteFile(filepath.Join(runDir, "retry.env"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	if due, err := retryDue(runDir, 1, failureAt.Add(14*time.Minute+59*time.Second)); err != nil || due {
		t.Fatalf("first attempt became due early: due=%v err=%v", due, err)
	}
	if due, err := retryDue(runDir, 1, failureAt.Add(15*time.Minute)); err != nil || !due {
		t.Fatalf("first attempt not due at cadence: due=%v err=%v", due, err)
	}
	if due, err := retryDue(runDir, 3, failureAt.Add(59*time.Minute)); err != nil || due {
		t.Fatalf("third attempt became due before exponential delay: due=%v err=%v", due, err)
	}
	if due, err := retryDue(runDir, 3, failureAt.Add(time.Hour)); err != nil || !due {
		t.Fatalf("third attempt not due after exponential delay: due=%v err=%v", due, err)
	}
}

func TestFinishedMarkerOverridesFreshRunLogAtLivenessThreshold(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, "run.log"), []byte("apparently still exciting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	finishedAt := time.Now().Add(-2 * time.Hour).UTC()
	data := "PUMP19_FINISHED_VERSION=1\nPUMP19_FINISHED_AT=" + finishedAt.Format(time.RFC3339Nano) + "\n"
	if err := os.WriteFile(filepath.Join(runDir, "finished.env"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, alive, err := runState(runDir, time.Hour); err != nil || alive {
		t.Fatalf("stale finished wrapper remained alive: alive=%v err=%v", alive, err)
	}
	if _, alive, err := runState(runDir, 3*time.Hour); err != nil || !alive {
		t.Fatalf("recent finished wrapper was reaped early: alive=%v err=%v", alive, err)
	}
}

func TestSweepReapsFinishedUnitOnlyAfterLivenessThreshold(t *testing.T) {
	original := systemctlCommand
	unitActive := false
	stopCalls := 0
	systemctlCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) > 1 && args[1] == "stop" {
			stopCalls++
			unitActive = false
			return exec.CommandContext(ctx, "true")
		}
		if len(args) > 1 && args[1] == "show" {
			state := "inactive\n"
			if unitActive {
				state = "active\n"
			}
			return exec.CommandContext(ctx, "printf", state)
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { systemctlCommand = original })

	for _, tt := range []struct {
		name        string
		finishedAt  time.Time
		active      bool
		wantStopped bool
	}{
		{name: "within threshold", finishedAt: time.Now().Add(-30 * time.Minute), active: true},
		{name: "inactive past threshold", finishedAt: time.Now().Add(-2 * time.Hour)},
		{name: "active past threshold", finishedAt: time.Now().Add(-2 * time.Hour), active: true, wantStopped: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			unitActive = tt.active
			stopsBefore := stopCalls
			root := t.TempDir()
			cfg := ServiceConfig{}
			cfg.Runs.Dir = filepath.Join(root, "runs")
			cfg.Sweep.LivenessThreshold.Duration = time.Hour
			facts := Facts{Forge: "local", Owner: "pump19", Repo: "subject", PR: "42", HeadSHA: "abcdef1234567890"}
			runDir := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
			if err := os.MkdirAll(runDir, 0o755); err != nil {
				t.Fatal(err)
			}
			meta := "PUMP19_HEAD_SHA=" + facts.HeadSHA + "\nPUMP19_STARTED_AT=" + tt.finishedAt.Add(-time.Minute).UTC().Format(time.RFC3339Nano) + "\nPUMP19_UNIT=pump19-run-fixture.service\n"
			if err := os.WriteFile(filepath.Join(runDir, "meta.env"), []byte(meta), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(runDir, "run.log"), []byte("fresh but finished\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := writeFinishedMarker(runDir, tt.finishedAt, "success"); err != nil {
				t.Fatal(err)
			}
			reviewContext, _ := StatusContext(RunReview)
			logFile, err := os.Create(filepath.Join(root, "sweep.log"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = reapLabelLessClaims(t.Context(), cfg, RepoConfig{}, Adaptation{}, facts, []Status{{ID: 1, Context: reviewContext, State: "success"}}, logFile)
			if closeErr := logFile.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, statErr := os.Stat(runDir); statErr != nil {
				t.Fatalf("finished claim was released instead of preserved: %v", statErr)
			}
			stopped := stopCalls > stopsBefore
			if stopped != tt.wantStopped {
				t.Fatalf("unit stopped=%v, want %v", stopped, tt.wantStopped)
			}
		})
	}
}
