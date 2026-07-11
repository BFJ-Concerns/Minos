package shell

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebhookBodyAtExactLimitReachesAuthentication(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/hooks/local", strings.NewReader(strings.Repeat("x", int(maxWebhookBodyBytes))))
	response := httptest.NewRecorder()

	err := handleHook(t.Context(), receiverTestConfig(t, t.TempDir()), response, request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("exact-limit body status = %d, want authentication rejection %d", response.Code, http.StatusUnauthorized)
	}
	if strings.Contains(response.Body.String(), "too large") {
		t.Fatalf("exact-limit body was rejected by the size gate: %q", response.Body.String())
	}
}

func TestSpawnEnvironmentMatchesRunBodyContract(t *testing.T) {
	root := t.TempDir()
	cfg := ServiceConfig{Root: filepath.Join(root, "config")}
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Runs.MaxConcurrent = 2
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

	decision, ok := reconcileDecision(repo, facts, []Status{{
		Context: reviewContext,
		State:   "failure",
		Creator: "pump19",
	}}, "")
	if !ok || decision != RunFix {
		t.Fatalf("partial coverage with findings should still drive fix, got ok=%v decision=%s", ok, decision)
	}

	decision, ok = reconcileDecision(repo, facts, []Status{{
		Context: reviewContext,
		State:   "error",
		Creator: "pump19",
	}}, "")
	if ok {
		t.Fatalf("partial coverage without findings must not auto-fire, got decision=%s", decision)
	}
}

func TestStatusForContextUsesForgejoStatusIDs(t *testing.T) {
	status, ok := statusForContext([]Status{
		{ID: 11, Context: "pump19/review", State: "success"},
		{ID: 12, Context: "pump19/review", State: "error"},
		{ID: 13, Context: "other", State: "success"},
	}, "pump19/review")
	if !ok {
		t.Fatal("expected review status")
	}
	if status.State != "error" {
		t.Fatalf("higher Forgejo status id should win, got %s", status.State)
	}
}

func TestGetStatusesRejectsMissingForgejoStatusID(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, filepath.Join(dir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[{\"context\":\"pump19/review\",\"state\":\"error\"}]\\n'\n")
	_, err := (Adaptation{Dir: dir}).GetStatuses(context.Background(), "pump19", "subject", "abcdef")
	if err == nil || !strings.Contains(err.Error(), "positive id") {
		t.Fatalf("missing status id error = %v", err)
	}
}

func TestReconcileImplicationsAreOrderedAndActorSourced(t *testing.T) {
	drafts := false
	repo := RepoConfig{Triggers: []TriggerRule{
		{Run: "flaky", On: []string{"label-added:Flaky Tests"}, Actors: []string{"ci-bot"}},
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
	flakyContext, _ := StatusContext(RunFlaky)
	tests := []struct {
		name     string
		labels   []string
		statuses []Status
		want     RunKind
		wantOK   bool
	}{
		{name: "missing review fires review", want: RunReview, wantOK: true},
		{
			name:   "standing flaky label fires repair before review",
			labels: []string{LabelFlakyTests},
			want:   RunFlaky,
			wantOK: true,
		},
		{
			name:     "completed flaky head does not refire",
			labels:   []string{LabelFlakyTests},
			statuses: []Status{{Context: flakyContext, State: "success", Creator: "pump19"}, {Context: reviewContext, State: "success", Creator: "pump19"}},
		},
		{
			name:     "labelled failed review fires fix from status creator",
			labels:   []string{LabelStandingFindings},
			statuses: []Status{{Context: reviewContext, State: "failure", Creator: "pump19"}},
			want:     RunFix,
			wantOK:   true,
		},
		{
			name:     "existing fix status blocks duplicate fix",
			labels:   []string{LabelStandingFindings},
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
			f.Actor = "ci-bot"
			got, ok := reconcileDecision(repo, f, tt.statuses, "bob")
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("decision = %s ok=%v, want %s ok=%v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestUnconfiguredFlakyLabelPausesWithoutStartingRepairOrRefiringReview(t *testing.T) {
	repo := RepoConfig{Triggers: []TriggerRule{{Run: "review", Authors: []string{"*"}}}}
	reviewContext, _ := StatusContext(RunReview)
	facts := Facts{Labels: []string{LabelFlakyTests}, Author: "contributor"}

	if decision, ok := reconcileDecision(repo, facts, []Status{{Context: reviewContext, State: "success"}}, ""); ok {
		t.Fatalf("unconfigured flaky label unexpectedly fired %s", decision)
	}
}

func TestPausedFlakyReviewSuccessIsTerminalForTheHead(t *testing.T) {
	repo := RepoConfig{Triggers: []TriggerRule{
		{Run: "flaky", Actors: []string{"ci-bot"}},
		{Run: "review", Authors: []string{"*"}},
	}}
	reviewContext, _ := StatusContext(RunReview)
	facts := Facts{Labels: []string{LabelFlakyTests}, Author: "contributor", Actor: "ci-bot"}
	statuses := []Status{{Context: reviewContext, State: "success", Creator: "pump19"}}

	// The standing label may start its repair run, but the paused review's
	// success status remains terminal: absence of Converged never implies review.
	if decision, ok := reconcileDecision(repo, facts, statuses, ""); !ok || decision != RunFlaky {
		t.Fatalf("paused head decision = %s ok=%v, want flaky repair only", decision, ok)
	}
	facts.Labels = nil
	if decision, ok := reconcileDecision(repo, facts, statuses, ""); ok {
		t.Fatalf("paused review success unexpectedly re-fired %s after pause lifted", decision)
	}
}

func TestPersistentlyFailingFlakyRepairContinuesPastFormerAttemptLimit(t *testing.T) {
	root := t.TempDir()
	facts := Facts{
		Forge:   "local",
		Owner:   "pump19",
		Repo:    "subject",
		PR:      "18",
		HeadSHA: "abcdef1234567890",
		Labels:  []string{LabelFlakyTests},
		Actor:   "ci-bot",
	}
	runDir := RunDir(filepath.Join(root, "runs"), facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunFlaky)
	for attempt := 1; attempt <= 5; attempt++ {
		if err := os.MkdirAll(runDir+fmt.Sprintf(".retry-%d", attempt), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "retry.env"), []byte("PUMP19_RETRYABLE_FAILURE=1\nPUMP19_FAILURE_AT=2026-07-11T09:00:00Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(root, "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	_, handled, released, err := handleRetryableClaim(t.Context(), facts, RunFlaky, runDir, nil, logFile)
	if err != nil || !handled || !released {
		t.Fatalf("sixth attempt handled=%v released=%v err=%v", handled, released, err)
	}
	if _, err := os.Stat(runDir + ".retry-6"); err != nil {
		t.Fatalf("sixth attempt evidence was not preserved: %v", err)
	}
	if _, err := readTerminalMarker(runDir + ".retry-6"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("persistent retry acquired a terminal latch: %v", err)
	}
}

func TestStandingFlakyLabelUsesRecordedActorToStartRepair(t *testing.T) {
	repo := RepoConfig{Triggers: []TriggerRule{{Run: "flaky", Actors: []string{"bob"}}}}
	facts := Facts{Owner: "pump19", Repo: "subject", PR: "18", Labels: []string{LabelFlakyTests}}
	dir := t.TempDir()
	writeScript(t, filepath.Join(dir, "label-actor"), "#!/usr/bin/env sh\nprintf 'bob\\n'\n")

	actor, err := resolveFlakyActor(t.Context(), repo, Adaptation{Dir: dir}, facts)
	if err != nil {
		t.Fatal(err)
	}
	facts.Actor = actor
	if decision, ok := reconcileDecision(repo, facts, nil, ""); !ok || decision != RunFlaky {
		t.Fatalf("standing label implication = %s ok=%v, want flaky", decision, ok)
	}
}

func TestFlakyCrashRecoveryKeepsErrorsOffThePR(t *testing.T) {
	root := t.TempDir()
	statusFile := filepath.Join(root, "status.args")
	adaptationDir := filepath.Join(root, "adaptation")
	if err := os.Mkdir(adaptationDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptationDir, "set-status"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >'"+statusFile+"'\n")
	facts := Facts{Forge: "local", Owner: "pump19", Repo: "subject", PR: "18", HeadSHA: "abcdef1234567890", Labels: []string{LabelFlakyTests, LabelRepairingFlaky}}

	crashed := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, RunFlaky)
	if err := os.MkdirAll(crashed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crashed, "meta.env"), []byte("PUMP19_HEAD_SHA="+facts.HeadSHA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(crashed+".reaped-1", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, terminal, err := terminaliseUnsafeCrash(facts, RunFlaky, crashed, nil); err != nil || terminal {
		t.Fatalf("flaky repeated crash terminal=%v err=%v", terminal, err)
	}
	if _, err := os.Stat(statusFile); !os.IsNotExist(err) {
		t.Fatalf("flaky repeated crash wrote PR status: %v", err)
	}

	retry := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "fedcba9876543210", RunFlaky)
	if err := os.MkdirAll(retry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retry, "meta.env"), []byte("PUMP19_HEAD_SHA=fedcba9876543210\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retry, "retry.env"), []byte("PUMP19_RETRYABLE_FAILURE=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(retry+".retry-1", 0o755); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(root, "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	_, handled, released, err := handleRetryableClaim(t.Context(), facts, RunFlaky, retry, nil, logFile)
	if closeErr := logFile.Close(); err == nil {
		err = closeErr
	}
	if err != nil || !handled || !released {
		t.Fatalf("flaky retry handled=%v released=%v err=%v", handled, released, err)
	}
	if _, err := os.Stat(statusFile); !os.IsNotExist(err) {
		t.Fatalf("flaky retry exhaustion wrote PR status: %v", err)
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

	got, err := clearUnauthorisedReady(context.Background(), repo, Adaptation{Dir: adaptationDir}, facts, "mallory", logFile)
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
	repo := RepoConfig{Triggers: []TriggerRule{{Run: "finish", On: []string{"label-added:Ready"}, Actors: []string{"bob"}, Authors: []string{"alice"}, Drafts: &drafts}}}
	// Clearance is solely an actor-authorisation repair. Draft and author guards
	// still decide whether finish fires, but must not relabel an authorised act.
	facts := Facts{Owner: "pump19", Repo: "subject", PR: "42", Author: "mallory", Labels: []string{LabelReady}, Draft: true}
	adaptationDir := t.TempDir()
	removeFile := filepath.Join(adaptationDir, "removed.args")
	writeScript(t, filepath.Join(adaptationDir, "label-actor"), "#!/usr/bin/env sh\nprintf 'bob\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "remove-label"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >'"+removeFile+"'\n")
	logFile, err := os.Create(filepath.Join(t.TempDir(), "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	got, err := clearUnauthorisedReady(context.Background(), repo, Adaptation{Dir: adaptationDir}, facts, "bob", logFile)
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

func TestReadyActorIsReadOnceAcrossClearanceAndReconcile(t *testing.T) {
	drafts := false
	repo := RepoConfig{Triggers: []TriggerRule{{Run: "finish", On: []string{"label-added:Ready"}, Actors: []string{"bob"}, Drafts: &drafts}}}
	facts := Facts{Owner: "pump19", Repo: "subject", PR: "42", Labels: []string{LabelReady}}
	dir := t.TempDir()
	countFile := filepath.Join(dir, "label-actor.count")
	writeScript(t, filepath.Join(dir, "label-actor"), "#!/usr/bin/env sh\nprintf x >>'"+countFile+"'\nprintf 'bob\\n'\n")
	writeScript(t, filepath.Join(dir, "remove-label"), "#!/usr/bin/env sh\nexit 0\n")
	adaptation := Adaptation{Dir: dir}
	logFile, err := os.Create(filepath.Join(t.TempDir(), "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	readyActor, err := resolveReadyActor(context.Background(), repo, adaptation, facts)
	if err != nil {
		t.Fatal(err)
	}
	facts, err = clearUnauthorisedReady(context.Background(), repo, adaptation, facts, readyActor, logFile)
	if err != nil {
		t.Fatal(err)
	}
	reconcileDecision(repo, facts, nil, readyActor)
	count, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(count); got != 1 {
		t.Fatalf("label actor reads = %d, want 1", got)
	}
}

func TestReconcileReviewActorGuardIsReceiverPathOnly(t *testing.T) {
	drafts := false
	repo := RepoConfig{Triggers: []TriggerRule{{Run: "review", Authors: []string{"*"}, Actors: []string{"bob"}, Drafts: &drafts}}}
	facts := Facts{Author: "contributor"}
	decision, ok := reconcileDecision(repo, facts, nil, "")
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
	cfg.Runs.MaxConcurrent = 2
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
	if _, err := reapLabelLessClaims(context.Background(), cfg, RepoConfig{}, Adaptation{}, facts, nil, logFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("label-less claim still present: %v", err)
	}
	matches, err := filepath.Glob(runDir + ".retry-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one liveness-spaced retry attempt, got %v", matches)
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

func TestRunWrapTreatsBodyStartFailureAsRetryable(t *testing.T) {
	root := t.TempDir()
	adaptationDir := filepath.Join(root, "adaptation")
	if err := os.Mkdir(adaptationDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptationDir, "prepare-workspace"), "#!/usr/bin/env sh\nexit 0\n")
	serviceConfig := strings.Replace(validServiceConfig, "/tmp/adapt", adaptationDir, 1)
	credentialFile := filepath.Join(root, "token")
	if err := os.WriteFile(credentialFile, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serviceConfig = strings.Replace(serviceConfig, "/tmp/token", credentialFile, 1)
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(serviceConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(root, "runs", "local--pump19--subject", "pr42", "abcdef123456-review")
	workspace := filepath.Join(root, "workspace")
	t.Setenv("PUMP19_RUN_DIR", runDir)
	t.Setenv("PUMP19_WORKSPACE", workspace)
	t.Setenv("PUMP19_RUN_KIND", "review")
	t.Setenv("PUMP19_FORGE", "local")
	t.Setenv("PUMP19_OWNER", "pump19")
	t.Setenv("PUMP19_REPO_NAME", "subject")
	t.Setenv("PUMP19_PR", "42")
	t.Setenv("PUMP19_HEAD_SHA", "abcdef1234567890")
	t.Setenv("PUMP19_RUN_BODY", filepath.Join(root, "missing-run-body"))

	runErr := RunWrapCommand(context.Background(), []string{"--config", root})
	if runErr == nil {
		t.Fatal("expected missing run body to fail before starting")
	}
	values, err := readMetaFile(filepath.Join(runDir, "retry.env"))
	if err != nil {
		t.Fatalf("body start failure %v did not record retry evidence: %v", runErr, err)
	}
	if got := values["PUMP19_FAILURE_PHASE"]; got != "run-body-start" {
		t.Fatalf("failure phase = %q, want run-body-start", got)
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
