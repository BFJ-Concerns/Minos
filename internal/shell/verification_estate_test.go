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

func TestSpawnEnvironmentMatchesRunBodyContract(t *testing.T) {
	root := t.TempDir()
	cfg := ServiceConfig{Root: filepath.Join(root, "config")}
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Forges = map[string]ForgeConfig{
		"local": {Adaptation: filepath.Join(root, "adaptations")},
	}
	repo := RepoConfig{Forge: "local", Owner: "pump19", Repo: "subject"}
	repo.Adaptation.Skill = filepath.Join(root, "skills", "review")
	repo.Adaptation.RunBody = filepath.Join(root, "bin", "run-agent")
	repo.Adaptation.Briefs = ".review"
	facts := Facts{
		Forge:   "local",
		Owner:   "pump19",
		Repo:    "subject",
		PR:      "42",
		HeadSHA: "abcdef1234567890",
		BaseRef: "main",
	}

	runDir := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
	unit := UnitName(facts, RunReview)
	env := envMap(runEnv(cfg, repo, facts, RunReview, runDir, unit, "pr-opened"))

	required := map[string]string{
		"PUMP19_RUN_DIR":    runDir,
		"PUMP19_RUN_KIND":   "review",
		"PUMP19_OCCASION":   "pr-opened",
		"PUMP19_FORGE":      "local",
		"PUMP19_REPO":       "pump19/subject",
		"PUMP19_OWNER":      "pump19",
		"PUMP19_REPO_NAME":  "subject",
		"PUMP19_PR":         "42",
		"PUMP19_HEAD_SHA":   "abcdef1234567890",
		"PUMP19_BASE_REF":   "main",
		"PUMP19_WORKSPACE":  filepath.Join(os.TempDir(), "pump19-workspaces", unit),
		"PUMP19_DIFF":       filepath.Join(runDir, "diff.patch"),
		"PUMP19_ADAPTATION": filepath.Join(root, "adaptations"),
		"PUMP19_SKILL":      filepath.Join(root, "skills", "review"),
		"PUMP19_RUN_BODY":   filepath.Join(root, "bin", "run-agent"),
		"PUMP19_BRIEFS":     ".review",
		"PUMP19_CONFIG":     cfg.Root,
		"PUMP19_UNIT":       unit,
	}
	for key, want := range required {
		if got := env[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if strings.Contains(env["PUMP19_RUN_DIR"], ".reaped-") {
		t.Fatalf("spawn handed a reaped evidence path as the live claim: %s", env["PUMP19_RUN_DIR"])
	}
	if strings.HasPrefix(env["PUMP19_WORKSPACE"], cfg.Runs.Dir) {
		t.Fatalf("workspace should be in temp storage, got %s", env["PUMP19_WORKSPACE"])
	}
}

func TestPartialCoverageNeverReconcilesAsConverged(t *testing.T) {
	drafts := false
	repo := RepoConfig{Triggers: []TriggerRule{
		{Run: "review", Authors: []string{"*"}, Drafts: &drafts},
		{Run: "fix", Actors: []string{"pump19"}},
	}}
	facts := Facts{
		Forge:  "local",
		Owner:  "pump19",
		Repo:   "subject",
		PR:     "42",
		Draft:  false,
		Labels: []string{LabelPartialCoverage},
	}
	reviewContext, _ := StatusContext(RunReview)

	decision, ok, err := reconcileDecision(context.Background(), repo, Adaptation{}, facts, []Status{{
		Context: reviewContext,
		State:   "failure",
		Creator: "pump19",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || decision != RunFix {
		t.Fatalf("partial coverage with findings should still drive fix, got ok=%v decision=%s", ok, decision)
	}

	decision, ok, err = reconcileDecision(context.Background(), repo, Adaptation{}, facts, []Status{{
		Context: reviewContext,
		State:   "error",
		Creator: "pump19",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("partial coverage without findings must not auto-fire, got decision=%s", decision)
	}
}

func TestStatusForContextMatchesForgejoCombinedOrdering(t *testing.T) {
	status, ok := statusForContext([]Status{
		{ID: 11, Context: "pump19/review", State: "success", CreatedUnix: 1234},
		{ID: 12, Context: "pump19/review", State: "error", CreatedUnix: 1234},
		{ID: 13, Context: "other", State: "success", CreatedUnix: 1235},
	}, "pump19/review")
	if !ok {
		t.Fatal("expected review status")
	}
	if status.State != "error" {
		t.Fatalf("same-second status tie should choose higher id, got %s", status.State)
	}

	status, ok = statusForContext([]Status{
		{ID: 14, Context: "pump19/review", State: "success", CreatedUnix: 1235},
		{ID: 15, Context: "pump19/review", State: "error", CreatedUnix: 1234},
	}, "pump19/review")
	if !ok {
		t.Fatal("expected review status")
	}
	if status.State != "success" {
		t.Fatalf("newer created_unix should win before id, got %s", status.State)
	}
}

func TestReconcileImplicationsAreOrderedAndActorSourced(t *testing.T) {
	drafts := false
	repo := RepoConfig{Triggers: []TriggerRule{
		{Run: "review", Authors: []string{"*"}, Drafts: &drafts},
		{Run: "fix", Actors: []string{"pump19"}},
		{Run: "finish", On: []string{"label-added:Ready"}, Actors: []string{"bob"}},
	}}
	facts := Facts{
		Forge:   "local",
		Owner:   "pump19",
		Repo:    "subject",
		PR:      "42",
		HeadSHA: "abcdef1234567890",
		Author:  "contributor",
		Draft:   false,
	}
	reviewContext, _ := StatusContext(RunReview)
	fixContext, _ := StatusContext(RunFix)
	finishContext, _ := StatusContext(RunFinish)
	adaptation := testAdaptationWithLabelActor(t, "bob")

	tests := []struct {
		name     string
		labels   []string
		statuses []Status
		want     RunKind
		wantOK   bool
	}{
		{name: "missing review fires review", want: RunReview, wantOK: true},
		{
			name:     "failed review fires fix from status creator",
			statuses: []Status{{Context: reviewContext, State: "failure", Creator: "pump19"}},
			want:     RunFix,
			wantOK:   true,
		},
		{
			name:     "existing fix status blocks duplicate fix",
			statuses: []Status{{Context: reviewContext, State: "failure", Creator: "pump19"}, {Context: fixContext, State: "success", Creator: "pump19"}},
		},
		{
			name:     "ready label fires finish from label actor",
			labels:   []string{LabelReady},
			statuses: []Status{{Context: reviewContext, State: "success", Creator: "pump19"}},
			want:     RunFinish,
			wantOK:   true,
		},
		{
			name:     "existing finish status blocks duplicate finish",
			labels:   []string{LabelReady},
			statuses: []Status{{Context: reviewContext, State: "success", Creator: "pump19"}, {Context: finishContext, State: "success", Creator: "bob"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := facts
			f.Labels = tt.labels
			got, ok, err := reconcileDecision(context.Background(), repo, adaptation, f, tt.statuses)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("decision = %s ok=%v, want %s ok=%v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestUnauthorisedReadyIsClearedBeforeReconcile(t *testing.T) {
	drafts := false
	repo := RepoConfig{Triggers: []TriggerRule{{Run: "finish", On: []string{"label-added:Ready"}, Actors: []string{"bob"}, Drafts: &drafts}}}
	facts := Facts{Owner: "pump19", Repo: "subject", PR: "42", Labels: []string{LabelReady}, Draft: false}
	adaptationDir := t.TempDir()
	removeFile := filepath.Join(adaptationDir, "removed.args")
	writeScript(t, filepath.Join(adaptationDir, "label-actor"), "#!/usr/bin/env sh\nprintf 'mallory\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "remove-label"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >'"+removeFile+"'\n")
	logFile, err := os.Create(filepath.Join(t.TempDir(), "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	got, err := clearUnauthorisedReady(context.Background(), repo, Adaptation{Dir: adaptationDir}, facts, logFile)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasLabel(LabelReady) {
		t.Fatal("unauthorised Ready should be removed from in-memory facts")
	}
	data, err := os.ReadFile(removeFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Ready") {
		t.Fatalf("remove-label was not called for Ready:\n%s", data)
	}
}

func TestAuthorisedReadyIsNotCleared(t *testing.T) {
	drafts := false
	repo := RepoConfig{Triggers: []TriggerRule{{Run: "finish", On: []string{"label-added:Ready"}, Actors: []string{"bob"}, Drafts: &drafts}}}
	facts := Facts{Owner: "pump19", Repo: "subject", PR: "42", Labels: []string{LabelReady}, Draft: false}
	adaptationDir := t.TempDir()
	removeFile := filepath.Join(adaptationDir, "removed.args")
	writeScript(t, filepath.Join(adaptationDir, "label-actor"), "#!/usr/bin/env sh\nprintf 'bob\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "remove-label"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >'"+removeFile+"'\n")
	logFile, err := os.Create(filepath.Join(t.TempDir(), "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	got, err := clearUnauthorisedReady(context.Background(), repo, Adaptation{Dir: adaptationDir}, facts, logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasLabel(LabelReady) {
		t.Fatal("authorised Ready should remain")
	}
	if _, err := os.Stat(removeFile); !os.IsNotExist(err) {
		t.Fatalf("remove-label should not be called for authorised Ready: %v", err)
	}
}

func TestReconcileReviewActorGuardIsReceiverPathOnly(t *testing.T) {
	drafts := false
	repo := RepoConfig{Triggers: []TriggerRule{{Run: "review", Authors: []string{"*"}, Actors: []string{"bob"}, Drafts: &drafts}}}
	facts := Facts{Author: "contributor"}
	decision, ok, err := reconcileDecision(context.Background(), repo, Adaptation{}, facts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || decision != RunReview {
		t.Fatalf("review actor guard should be receiver-path-only on reconcile: decision=%s ok=%v", decision, ok)
	}
}

func TestReapFailsClosedWhenUnitCannotBeStopped(t *testing.T) {
	original := systemctlCommand
	systemctlCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) > 1 && args[1] == "stop" {
			return exec.CommandContext(ctx, "true")
		}
		return exec.CommandContext(ctx, "printf", "active\n")
	}
	t.Cleanup(func() {
		systemctlCommand = original
	})

	runDir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "meta.env"), []byte("PUMP19_UNIT=pump19-definitely-not-a-real-unit.service\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := reapRunDir(context.Background(), runDir)
	if err == nil {
		t.Fatal("expected reap to fail when the unit cannot be stopped")
	}
	if _, statErr := os.Stat(runDir); statErr != nil {
		t.Fatalf("run dir was released despite failed stop: %v", statErr)
	}
	matches, globErr := filepath.Glob(runDir + ".reaped-*")
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("failed reap left evidence rename behind: %v", matches)
	}
}

func TestReapReleasesPreBodyClaimWithoutMetadata(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := reapRunDir(context.Background(), runDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("canonical claim still exists after pre-body reap: %v", err)
	}
	matches, err := filepath.Glob(runDir + ".reaped-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one evidence directory, got %v", matches)
	}
}

func TestReapFailsRatherThanOverwritingEvidence(t *testing.T) {
	original := reapedSuffix
	reapedSuffix = func() int64 { return 1 }
	t.Cleanup(func() {
		reapedSuffix = original
	})
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	if err := os.Mkdir(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runDir+".reaped-1", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := reapRunDir(context.Background(), runDir); err == nil {
		t.Fatal("expected reap collision to fail")
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("canonical claim was removed after collision: %v", err)
	}
}

func TestLabelLessClaimIsReapedWhenStaleAndUnfinished(t *testing.T) {
	root := t.TempDir()
	cfg := ServiceConfig{}
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Sweep.LivenessThreshold.Duration = time.Hour
	facts := Facts{Forge: "local", Owner: "pump19", Repo: "subject", PR: "42", HeadSHA: "abcdef1234567890"}
	runDir := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunReview)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(runDir, old, old); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(root, "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	if _, err := reapLabelLessClaims(context.Background(), cfg, Adaptation{}, facts, nil, logFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("label-less claim still present: %v", err)
	}
	matches, err := filepath.Glob(runDir + ".reaped-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one reaped claim, got %v", matches)
	}
}

func TestRunMetadataWriteFailureIsLoud(t *testing.T) {
	missingRunDir := filepath.Join(t.TempDir(), "missing", "run")
	if err := writeMeta(missingRunDir); err == nil {
		t.Fatal("expected metadata write failure for a missing run directory")
	}
}

func TestRunBodyCommandUsesRunBodyNotSkill(t *testing.T) {
	t.Setenv("PUMP19_SKILL", filepath.Join(t.TempDir(), "review-skill.md"))
	body := filepath.Join(t.TempDir(), "run-body")
	t.Setenv("PUMP19_RUN_BODY", body)
	cmd, err := runBodyCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != body {
		t.Fatalf("run body path = %q, want %q", cmd.Path, body)
	}
}

func TestRunWrapFailureRecordsStatusWhenBodyDidNot(t *testing.T) {
	dir := t.TempDir()
	statusFile := filepath.Join(dir, "status.args")
	writeScript(t, filepath.Join(dir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	writeScript(t, filepath.Join(dir, "set-status"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >'"+statusFile+"'\n")
	adaptation := Adaptation{Dir: dir}
	facts := Facts{Owner: "pump19", Repo: "subject", HeadSHA: "abcdef1234567890"}
	if err := recordRunWrapFailureStatus(context.Background(), adaptation, facts, RunReview, os.ErrInvalid); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(statusFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, "pump19/review\nerror") {
		t.Fatalf("set-status args did not record review error:\n%s", got)
	}
}

func TestRunWrapFailureDoesNotOverwriteBodyStatus(t *testing.T) {
	dir := t.TempDir()
	statusFile := filepath.Join(dir, "status.args")
	writeScript(t, filepath.Join(dir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[{\"context\":\"pump19/review\",\"state\":\"error\"}]\\n'\n")
	writeScript(t, filepath.Join(dir, "set-status"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >'"+statusFile+"'\n")
	adaptation := Adaptation{Dir: dir}
	facts := Facts{Owner: "pump19", Repo: "subject", HeadSHA: "abcdef1234567890"}
	if err := recordRunWrapFailureStatus(context.Background(), adaptation, facts, RunReview, os.ErrInvalid); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statusFile); !os.IsNotExist(err) {
		t.Fatalf("wrapper wrote status despite existing body status: %v", err)
	}
}

func TestRunWrapFailureDoesNotWriteStatusWhenStatusReadFails(t *testing.T) {
	dir := t.TempDir()
	statusFile := filepath.Join(dir, "status.args")
	writeScript(t, filepath.Join(dir, "get-statuses"), "#!/usr/bin/env sh\nexit 7\n")
	writeScript(t, filepath.Join(dir, "set-status"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >'"+statusFile+"'\n")
	adaptation := Adaptation{Dir: dir}
	facts := Facts{Owner: "pump19", Repo: "subject", HeadSHA: "abcdef1234567890"}
	if err := recordRunWrapFailureStatus(context.Background(), adaptation, facts, RunReview, os.ErrInvalid); err == nil {
		t.Fatal("expected wrapper status write to fail closed on status read failure")
	}
	if _, err := os.Stat(statusFile); !os.IsNotExist(err) {
		t.Fatalf("wrapper wrote status after status read failure: %v", err)
	}
}

func TestLiveRunStateTreatsWarmLogAsAliveAndQuietLogAsDead(t *testing.T) {
	root := t.TempDir()
	facts := Facts{Forge: "local", Owner: "pump19", Repo: "subject", PR: "42"}
	runDir := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "abcdef1234567890", RunReview)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(runDir, "run.log")
	if err := os.WriteFile(logPath, []byte("heartbeat\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, alive, err := liveRunState(root, facts, RunReview, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !alive {
		t.Fatal("warm run log should keep the run alive")
	}

	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatal(err)
	}
	_, alive, err = liveRunState(root, facts, RunReview, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("quiet run log should be reaped after the threshold")
	}
}

func envMap(pairs []string) map[string]string {
	values := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, _ := strings.Cut(pair, "=")
		values[key] = value
	}
	return values
}

func testAdaptationWithLabelActor(t *testing.T, actor string) Adaptation {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "label-actor")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env sh\nprintf '%s\\n' \""+actor+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Adaptation{Dir: dir}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
