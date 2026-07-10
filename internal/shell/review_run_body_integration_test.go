package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewRunBodyPublishesOutcomesAndAutoMergeJourney(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(path, 0o755)
			}
			return nil
		})
	})
	configRoot := filepath.Join(root, "config")
	adaptationDir := filepath.Join(root, "adaptation")
	stateDir := filepath.Join(root, "state")
	installRoot := filepath.Join(root, "install")
	for _, dir := range []string{configRoot, adaptationDir, stateDir, filepath.Join(configRoot, "repos"), filepath.Join(root, "runs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	binary := filepath.Join(root, "pump19")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/pump19")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pump19 fixture: %v\n%s", err, out)
	}
	installReviewRunBodyFixture(t, installRoot)
	reviewScripts, err := filepath.Abs(filepath.Join("..", "..", "scripts", "review"))
	if err != nil {
		t.Fatal(err)
	}
	standin, err := filepath.Abs(filepath.Join("..", "..", "scripts", "e2e", "review-engine-standin"))
	if err != nil {
		t.Fatal(err)
	}

	pins := filepath.Join(root, "pins.toml")
	if err := os.WriteFile(pins, []byte(pinsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	runBodyEnv := "PUMP19_ENGINE_LAUNCH_LEAD='" + standin + "'\n" +
		"PUMP19_ENSEMBLE_LAUNCH='/bin/false'\n" +
		"PUMP19_PINS='" + pins + "'\n" +
		"PUMP19_REVIEW_SCRIPTS='" + reviewScripts + "'\n" +
		"PUMP19_BIN='" + binary + "'\n"
	if err := os.WriteFile(filepath.Join(configRoot, "run-body.env"), []byte(runBodyEnv), 0o644); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(root, "token")
	webhookSecret := filepath.Join(root, "webhook-secret")
	if err := os.WriteFile(token, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(webhookSecret, []byte("test-webhook-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := "[listener]\nbind = \":0\"\n\n[forges.local]\nadaptation = \"" + adaptationDir + "\"\napi-base = \"http://forge.invalid\"\nwebhook-secret-file = \"" + webhookSecret + "\"\ncredential-file = \"" + token + "\"\n\n[runs]\ndir = \"" + filepath.Join(root, "runs") + "\"\nmax-concurrent = 2\n\n[sweep]\nliveness-threshold = \"1h\"\n"
	if err := os.WriteFile(filepath.Join(configRoot, "service.toml"), []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReviewAdaptationFixture(t, adaptationDir, stateDir)

	finding1 := filepath.Join(root, "finding-1.json")
	if err := os.WriteFile(finding1, []byte(`{"path":"file.txt","line":2,"priority":"P1","body":"First finding"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	setReviewRunEnv(t, root, configRoot, installRoot, "aaaaaaaaaaaaaaaa", finding1)
	if err := RunWrapCommand(t.Context(), []string{"--config", configRoot}); err != nil {
		t.Fatal(err)
	}
	firstRun := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", "aaaaaaaaaaaaaaaa", RunReview)
	assertContainsFile(t, filepath.Join(firstRun, "review.md"), "verdict=standing-findings")
	assertContainsFile(t, filepath.Join(firstRun, "new-comments.json"), "finding=F-7KQ3")
	assertContainsFile(t, filepath.Join(firstRun, "governing", "AGENTS.md"), "base guidance")
	assertContainsFile(t, filepath.Join(firstRun, "provenance.json"), `"role": "lead"`)
	if data, err := os.ReadFile(filepath.Join(firstRun, "provenance.json")); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(data), "spec-claude") || strings.Contains(string(data), "verify-codex") {
		t.Fatalf("worker provenance unexpectedly remained a review gate:\n%s", data)
	}
	assertContainsFile(t, filepath.Join(stateDir, "status.args"), "pump19/review\nsuccess")
	assertReviewDispatchRecord(t, filepath.Join(stateDir, "dispatch.tsv"), installRoot)

	finding2 := filepath.Join(root, "finding-2.json")
	if err := os.WriteFile(finding2, []byte(`{"finding":"F-7KQ3","path":"file.txt","line":2,"priority":"P1","body":"Still present"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	setReviewRunEnv(t, root, configRoot, installRoot, "bbbbbbbbbbbbbbbb", finding2)
	if err := RunWrapCommand(t.Context(), []string{"--config", configRoot}); err != nil {
		t.Fatal(err)
	}
	secondRun := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", "bbbbbbbbbbbbbbbb", RunReview)
	assertContainsFile(t, filepath.Join(stateDir, "updated-body.md"), "Still present")
	assertContainsFile(t, filepath.Join(stateDir, "updated-body.md"), "finding=F-7KQ3")
	assertFileText(t, filepath.Join(secondRun, "new-comments.json"), "[]\n")

	setReviewRunEnv(t, root, configRoot, installRoot, "cccccccccccccccc", "")
	t.Setenv("PUMP19_STANDIN_VERDICT", "converged")
	t.Setenv("PUMP19_AUTO_MERGE", "true")
	afterReady := filepath.Join(root, "after-ready")
	continueAfterReady := filepath.Join(root, "continue-after-ready")
	t.Setenv("PUMP19_STANDIN_AFTER_READY_READY", afterReady)
	t.Setenv("PUMP19_STANDIN_AFTER_READY_CONTINUE", continueAfterReady)
	cleanResult := make(chan error, 1)
	go func() {
		cleanResult <- RunWrapCommand(t.Context(), []string{"--config", configRoot})
	}()
	if !waitForReviewFixturePath(afterReady, 5*time.Second) {
		_ = os.WriteFile(continueAfterReady, nil, 0o644)
		t.Fatal("stand-in did not reach the post-Ready, pre-release seam")
	}
	cleanFacts := Facts{
		Forge:   "local",
		Owner:   "pump19",
		Repo:    "subject",
		PR:      "42",
		HeadSHA: "cccccccccccccccc",
		Labels:  readFixtureLabels(t, filepath.Join(stateDir, "labels")),
	}
	if !cleanFacts.HasLabel(LabelReviewing) || !cleanFacts.HasLabel(LabelReady) {
		t.Fatalf("Ready should overlap Reviewing before review release: %v", cleanFacts.Labels)
	}
	drafts := false
	autoMergeRepo := RepoConfig{Triggers: []TriggerRule{{Run: "finish", On: []string{"label-added:Ready"}, Actors: []string{"pump19"}, Drafts: &drafts}}}
	adaptation := Adaptation{Dir: adaptationDir, Credential: "test-token"}
	readyActor, err := resolveReadyActor(t.Context(), autoMergeRepo, adaptation, cleanFacts)
	if err != nil {
		t.Fatal(err)
	}
	guardLog, err := os.Create(filepath.Join(root, "ready-guard.log"))
	if err != nil {
		t.Fatal(err)
	}
	guardedFacts, err := clearUnauthorisedReady(t.Context(), autoMergeRepo, adaptation, cleanFacts, readyActor, guardLog)
	if closeErr := guardLog.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if !guardedFacts.HasLabel(LabelReady) {
		t.Fatal("the service bot's Ready application failed the sweep actor guard")
	}
	statuses, err := adaptation.GetStatuses(t.Context(), "pump19", "subject", cleanFacts.HeadSHA)
	if err != nil {
		t.Fatal(err)
	}
	if decision, ok := reconcileDecision(autoMergeRepo, guardedFacts, statuses, readyActor); !ok || decision != RunFinish {
		t.Fatalf("Ready implication during Reviewing overlap = %s ok=%v, want finish", decision, ok)
	}
	if err := beginRun(t.Context(), adaptation, cleanFacts, RunFinish); err != nil {
		t.Fatal(err)
	}
	assertContainsFile(t, filepath.Join(stateDir, "labels"), LabelFinishing)
	if err := adaptation.RemoveLabel(t.Context(), "pump19", "subject", "42", LabelFinishing); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(continueAfterReady, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-cleanResult; err != nil {
		t.Fatal(err)
	}
	cleanRun := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", "cccccccccccccccc", RunReview)
	assertContainsFile(t, filepath.Join(cleanRun, "review.md"), "coverage=full")
	assertContainsFile(t, filepath.Join(cleanRun, "review.md"), "verdict=converged")
	assertContainsFile(t, filepath.Join(stateDir, "labels-added"), "Converged")
	assertContainsFile(t, filepath.Join(stateDir, "labels-added"), "Ready")
	assertReviewReadyOrder(t, filepath.Join(stateDir, "operations"), "cccccccccccccccc")
	t.Setenv("PUMP19_STANDIN_AFTER_READY_READY", "")
	t.Setenv("PUMP19_STANDIN_AFTER_READY_CONTINUE", "")

	negativeCases := []struct {
		name             string
		head             string
		verdict          string
		autoMerge        string
		labels           []string
		flakyAfterStatus bool
	}{
		{name: "standing-findings", head: "dddddddddddddddd", verdict: "standing-findings", autoMerge: "true"},
		{name: "partial-coverage", head: "1111111111111111", verdict: "partial-coverage", autoMerge: "true"},
		{name: "auto-merge-disabled", head: "2222222222222222", verdict: "converged", autoMerge: "false"},
		{name: "flaky-tests", head: "3333333333333333", verdict: "converged", autoMerge: "true", labels: []string{LabelFlakyTests, LabelConverged}},
		{name: "flaky-arrives-on-post-status-refresh", head: "5555555555555555", verdict: "converged", autoMerge: "true", flakyAfterStatus: true},
	}
	for _, tc := range negativeCases {
		t.Run(tc.name, func(t *testing.T) {
			readyBefore := fixtureLineCount(t, filepath.Join(stateDir, "labels-added"), LabelReady)
			convergedBefore := fixtureLineCount(t, filepath.Join(stateDir, "labels-added"), LabelConverged)
			setReviewRunEnv(t, root, configRoot, installRoot, tc.head, "")
			t.Setenv("PUMP19_STANDIN_VERDICT", tc.verdict)
			t.Setenv("PUMP19_AUTO_MERGE", tc.autoMerge)
			if tc.flakyAfterStatus {
				t.Setenv("PUMP19_FIXTURE_FLAKY_AFTER_STATUS", "1")
			}
			writeFixtureLabels(t, filepath.Join(stateDir, "labels"), tc.labels)
			if err := RunWrapCommand(t.Context(), []string{"--config", configRoot}); err != nil {
				t.Fatal(err)
			}
			if readyAfter := fixtureLineCount(t, filepath.Join(stateDir, "labels-added"), LabelReady); readyAfter != readyBefore {
				t.Fatalf("Ready applications = %d, want unchanged %d", readyAfter, readyBefore)
			}
			if tc.flakyAfterStatus {
				assertContainsFile(t, filepath.Join(stateDir, "labels"), "Flaky Tests")
			}
			if tc.name == "flaky-tests" {
				run := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", tc.head, RunReview)
				assertContainsFile(t, filepath.Join(run, "review.md"), "bar=passed coverage=full")
				assertContainsFile(t, filepath.Join(run, "review.md"), "verdict=paused-flaky")
				assertContainsFile(t, filepath.Join(run, "review.md"), "no approval was given")
				assertContainsFile(t, filepath.Join(stateDir, "status.args"), "pump19/review\nsuccess")
				reviews, err := os.ReadFile(filepath.Join(stateDir, "reviews.json"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(reviews), `"state": "COMMENT"`) || !strings.Contains(string(reviews), tc.head) {
					t.Fatalf("paused review was not recorded as COMMENT for %s:\n%s", tc.head, reviews)
				}
				if fixtureLineCount(t, filepath.Join(stateDir, "labels-added"), LabelConverged) != convergedBefore {
					t.Fatal("paused review added a Converged outcome label")
				}
				for _, label := range []string{LabelConverged, LabelStandingFindings, LabelPartialCoverage, LabelReady} {
					if containsFixtureLabel(readFixtureLabels(t, filepath.Join(stateDir, "labels")), label) {
						t.Fatalf("paused review retained outcome/control label %q", label)
					}
				}
			}
		})
	}
	partialRun := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", "1111111111111111", RunReview)
	assertContainsFile(t, filepath.Join(partialRun, "review.md"), "coverage=partial")
	assertContainsFile(t, filepath.Join(partialRun, "review.md"), "verdict=partial-coverage")
	assertContainsFile(t, filepath.Join(stateDir, "labels-added"), "Partial Coverage")

	// Ready is the sole mutation after the terminal review status. If it fails,
	// the wrapper preserves that status and records the command failure in the
	// run log; with no Ready, reconciliation has no finish implication to fire.
	applyFailureHead := "4444444444444444"
	setReviewRunEnv(t, root, configRoot, installRoot, applyFailureHead, "")
	t.Setenv("PUMP19_STANDIN_VERDICT", "converged")
	t.Setenv("PUMP19_AUTO_MERGE", "true")
	t.Setenv("PUMP19_FIXTURE_FAIL_READY_APPLY", "1")
	if err := RunWrapCommand(t.Context(), []string{"--config", configRoot}); err == nil {
		t.Fatal("failed Ready apply unexpectedly completed")
	}
	applyFailureRun := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", applyFailureHead, RunReview)
	assertContainsFile(t, filepath.Join(stateDir, "status.args"), "pump19/review\nsuccess")
	assertContainsFile(t, filepath.Join(applyFailureRun, "run.log"), "fixture Ready apply failed")
	assertContainsFile(t, filepath.Join(applyFailureRun, "run.log"), "pump19 run-wrap error")
	failedApplyLabels := readFixtureLabels(t, filepath.Join(stateDir, "labels"))
	if containsFixtureLabel(failedApplyLabels, LabelReady) || containsFixtureLabel(failedApplyLabels, LabelReviewing) {
		t.Fatalf("failed Ready apply left active control labels: %v", failedApplyLabels)
	}
	failedApplyFacts := Facts{Forge: "local", Owner: "pump19", Repo: "subject", PR: "42", HeadSHA: applyFailureHead, Labels: failedApplyLabels}
	failedApplyStatuses, err := adaptation.GetStatuses(t.Context(), "pump19", "subject", applyFailureHead)
	if err != nil {
		t.Fatal(err)
	}
	if decision, ok := reconcileDecision(autoMergeRepo, failedApplyFacts, failedApplyStatuses, ""); ok {
		t.Fatalf("failed Ready apply unexpectedly re-fired %s", decision)
	}
	t.Setenv("PUMP19_FIXTURE_FAIL_READY_APPLY", "")

	// The head may move after the run claims Reviewing but before it posts. Hold
	// the deterministic engine at that exact seam, advance the forge fixture,
	// and prove the real mission path yields without a mutation.
	postedBeforeStale, err := os.ReadFile(filepath.Join(stateDir, "posted-review.md"))
	if err != nil {
		t.Fatal(err)
	}
	staleReady := filepath.Join(root, "stale-ready")
	staleContinue := filepath.Join(root, "stale-continue")
	setReviewRunEnv(t, root, configRoot, installRoot, "ffffffffffffffff", "")
	t.Setenv("PUMP19_STANDIN_BEFORE_POST_READY", staleReady)
	t.Setenv("PUMP19_STANDIN_BEFORE_POST_CONTINUE", staleContinue)
	staleResult := make(chan error, 1)
	go func() {
		staleResult <- RunWrapCommand(t.Context(), []string{"--config", configRoot})
	}()
	if !waitForReviewFixturePath(staleReady, 5*time.Second) {
		_ = os.WriteFile(staleContinue, nil, 0o644)
		t.Fatal("stand-in did not reach the before-post stale-yield seam")
	}
	if err := os.WriteFile(filepath.Join(stateDir, "head"), []byte("newer-head-sha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleContinue, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-staleResult; err != nil {
		t.Fatalf("stale run should yield cleanly: %v", err)
	}
	postedAfterStale, err := os.ReadFile(filepath.Join(stateDir, "posted-review.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(postedAfterStale) != string(postedBeforeStale) {
		t.Fatal("stale run posted a review after the head moved")
	}
	t.Setenv("PUMP19_STANDIN_BEFORE_POST_READY", "")
	t.Setenv("PUMP19_STANDIN_BEFORE_POST_CONTINUE", "")

	postedBeforeMismatch, err := os.ReadFile(filepath.Join(stateDir, "posted-review.md"))
	if err != nil {
		t.Fatal(err)
	}
	setReviewRunEnv(t, root, configRoot, installRoot, "eeeeeeeeeeeeeeee", "")
	t.Setenv("PUMP19_STANDIN_VERDICT", "converged")
	t.Setenv("PUMP19_AUTO_MERGE", "true")
	t.Setenv("PUMP19_STANDIN_MISMATCH_AFTER_CLAIM", "floating-alias-surprise")
	t.Setenv("PUMP19_UNIT", "")
	if err := RunWrapCommand(t.Context(), []string{"--config", configRoot}); err == nil {
		t.Fatal("model mismatch unexpectedly completed")
	}
	mismatchRun := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", "eeeeeeeeeeeeeeee", RunReview)
	if _, err := os.Stat(filepath.Join(mismatchRun, "retry.env")); err != nil {
		t.Fatalf("mid-body mismatch after claim-only writes was not retryable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mismatchRun, terminalMarkerFile)); !os.IsNotExist(err) {
		t.Fatalf("mid-body mismatch latched on claim-only writes: %v", err)
	}
	if status, _ := os.ReadFile(filepath.Join(stateDir, "status.args")); strings.Contains(string(status), "pump19/review\nerror") {
		t.Fatalf("review mismatch wrote PR error status:\n%s", status)
	}
	assertContainsFile(t, filepath.Join(stateDir, "labels"), "Reviewing")
	postedAfterMismatch, err := os.ReadFile(filepath.Join(stateDir, "posted-review.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(postedAfterMismatch) != string(postedBeforeMismatch) {
		t.Fatal("model mismatch posted a new review")
	}
	if containsFixtureLabel(readFixtureLabels(t, filepath.Join(stateDir, "labels")), LabelReady) {
		t.Fatal("errored review applied Ready")
	}

	// A hard model-mismatch abort cannot run the child's EXIT trap. Age its real
	// run log and prove the reconciliation sweep reaps the claim and removes the
	// orphaned Reviewing label while preserving the attempt for paced retry.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(mismatchRun, "run.log"), old, old); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(root, "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	facts := Facts{Forge: "local", Owner: "pump19", Repo: "subject", PR: "42", HeadSHA: "eeeeeeeeeeeeeeee", Labels: []string{LabelReviewing}}
	cfg, err := LoadServiceConfig(configRoot)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Sweep.LivenessThreshold.Duration = time.Second
	if err := sweepPR(t.Context(), cfg, RepoConfig{}, Adaptation{Dir: adaptationDir, Credential: "test-token"}, facts, logFile); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mismatchRun); !os.IsNotExist(err) {
		t.Fatalf("sweep did not reap the mismatched run claim: %v", err)
	}
	if _, err := os.Stat(mismatchRun + ".retry-1"); err != nil {
		t.Fatalf("sweep did not preserve the first retry attempt: %v", err)
	}
	assertContainsFile(t, filepath.Join(stateDir, "labels-removed"), "Reviewing")

	// Complete the deterministic cross-run journey: the converged review above
	// supplied the service marker and Ready; finish claims and merges even while
	// the review run's in-flight label is present.
	finishStandin, err := filepath.Abs(filepath.Join("..", "..", "scripts", "e2e", "finish-engine-standin"))
	if err != nil {
		t.Fatal(err)
	}
	finishRunBodyEnv := "PUMP19_ENGINE_LAUNCH_LEAD='" + finishStandin + "'\n" +
		"PUMP19_ENSEMBLE_LAUNCH='/bin/false'\n" +
		"PUMP19_PINS='" + pins + "'\n" +
		"PUMP19_REVIEW_SCRIPTS='" + reviewScripts + "'\n" +
		"PUMP19_BIN='" + binary + "'\n"
	if err := os.WriteFile(filepath.Join(configRoot, "run-body.env"), []byte(finishRunBodyEnv), 0o644); err != nil {
		t.Fatal(err)
	}
	copyRepoFile(t, filepath.Join("..", "..", "missions", "finish.md"), filepath.Join(installRoot, "missions", "finish.md"), 0o644)
	setReadyJourneyFinishEnv(t, root, configRoot, installRoot, "cccccccccccccccc")
	writeFixtureLabels(t, filepath.Join(stateDir, "labels"), []string{LabelReviewing, LabelReady, LabelConverged})
	if err := RunWrapCommand(t.Context(), []string{"--config", configRoot}); err != nil {
		t.Fatal(err)
	}
	finishRun := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", "cccccccccccccccc", RunFinish)
	assertContainsFile(t, filepath.Join(finishRun, "finish-summary.md"), "outcome=merged")
	assertContainsFile(t, filepath.Join(stateDir, "merge.args"), "merge")
	assertFileText(t, filepath.Join(stateDir, "assignees"), "Minos\n")
	if assignments := fixtureLineCount(t, filepath.Join(stateDir, "operations"), "assign-if-missing:Minos"); assignments != 1 {
		t.Fatalf("Minos assignment writes = %d, want 1", assignments)
	}
	assertContainsFile(t, filepath.Join(stateDir, "operations"), "add-reaction:eyes")
	assertContainsFile(t, filepath.Join(stateDir, "operations"), "remove-reaction:eyes")
}

func waitForReviewFixturePath(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func readFixtureLabels(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func writeFixtureLabels(t *testing.T, path string, labels []string) {
	t.Helper()
	text := ""
	if len(labels) > 0 {
		text = strings.Join(labels, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func containsFixtureLabel(labels []string, want string) bool {
	for _, label := range labels {
		if label == want {
			return true
		}
	}
	return false
}

func fixtureLineCount(t *testing.T, path, want string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == want {
			count++
		}
	}
	return count
}

func assertReviewReadyOrder(t *testing.T, path, head string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	post, status, ready := -1, -1, -1
	for i, line := range lines {
		switch line {
		case "post-review:" + head:
			post = i
		case "set-status:" + head + ":pump19/review:success":
			status = i
		case "add-label:" + head + ":Ready":
			ready = i
		}
	}
	if post < 0 || status <= post || ready <= status {
		t.Fatalf("review publication order for %s = post:%d status:%d Ready:%d\n%s", head, post, status, ready, data)
	}
	freshFacts := 0
	for _, line := range lines[status+1 : ready] {
		if line == "get-pr-facts:"+head {
			freshFacts++
		}
	}
	// The first read is the explicit post-status flaky-label refresh. The
	// second is run-guard current immediately before the Ready mutation. Merely
	// finding either read would let the currency guard mask a missing refresh.
	if freshFacts != 2 {
		t.Fatalf("PR-facts reads between terminal status and Ready for %s = %d, want 2\n%s", head, freshFacts, data)
	}
}

func setReadyJourneyFinishEnv(t *testing.T, root, configRoot, installRoot, head string) {
	t.Helper()
	stateDir := filepath.Join(root, "state")
	if err := os.WriteFile(filepath.Join(stateDir, "head"), []byte(head+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDir := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", head, RunFinish)
	values := map[string]string{
		"PUMP19_RUN_DIR":          runDir,
		"PUMP19_RUN_KIND":         "finish",
		"PUMP19_OCCASION":         "label-added:Ready",
		"PUMP19_FORGE":            "local",
		"PUMP19_REPO":             "pump19/subject",
		"PUMP19_OWNER":            "pump19",
		"PUMP19_REPO_NAME":        "subject",
		"PUMP19_PR":               "42",
		"PUMP19_HEAD_SHA":         head,
		"PUMP19_BASE_REF":         "main",
		"PUMP19_WORKSPACE":        filepath.Join(root, "finish-workspace-"+head),
		"PUMP19_DIFF":             filepath.Join(runDir, "diff.patch"),
		"PUMP19_ADAPTATION":       filepath.Join(root, "adaptation"),
		"PUMP19_RUN_BODY":         filepath.Join(installRoot, "run-body", "run-body"),
		"PUMP19_CONFIG":           configRoot,
		"PUMP19_UNIT":             "pump19-finish-test.service",
		"PUMP19_BUILD_CMD":        "true",
		"PUMP19_TEST_CMD":         "true",
		"PUMP19_AUTO_MERGE":       "true",
		"PUMP19_FIX_AUTHOR_NAME":  "Pump-19",
		"PUMP19_FIX_AUTHOR_EMAIL": "pump19@example.invalid",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	for _, key := range []string{"PUMP19_STANDIN_VERDICT", "PUMP19_STANDIN_MISMATCH_AFTER_CLAIM", "PUMP19_FIXTURE_FAIL_READY_APPLY", "PUMP19_FIXTURE_FLAKY_AFTER_STATUS", "PUMP19_STANDIN_AFTER_READY_READY", "PUMP19_STANDIN_AFTER_READY_CONTINUE"} {
		t.Setenv(key, "")
	}
}

func setReviewRunEnv(t *testing.T, root, configRoot, installRoot, head, finding string) {
	t.Helper()
	skill, err := filepath.Abs(filepath.Join("..", "..", "skills", "foundry", "review-panel", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	if err := os.WriteFile(filepath.Join(stateDir, "head"), []byte(head+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"labels", "statuses.json"} {
		contents := []byte(nil)
		if name == "statuses.json" {
			contents = []byte("[]\n")
		}
		if err := os.WriteFile(filepath.Join(stateDir, name), contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runDir := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", head, RunReview)
	values := map[string]string{
		"PUMP19_RUN_DIR":                 runDir,
		"PUMP19_RUN_KIND":                "review",
		"PUMP19_OCCASION":                "pr-opened",
		"PUMP19_FORGE":                   "local",
		"PUMP19_REPO":                    "pump19/subject",
		"PUMP19_OWNER":                   "pump19",
		"PUMP19_REPO_NAME":               "subject",
		"PUMP19_PR":                      "42",
		"PUMP19_HEAD_SHA":                head,
		"PUMP19_BASE_REF":                "main",
		"PUMP19_WORKSPACE":               filepath.Join(root, "workspace-"+head),
		"PUMP19_DIFF":                    filepath.Join(runDir, "diff.patch"),
		"PUMP19_ADAPTATION":              filepath.Join(root, "adaptation"),
		"PUMP19_SKILL":                   skill,
		"PUMP19_RUN_BODY":                filepath.Join(installRoot, "run-body", "run-body"),
		"PUMP19_BRIEFS":                  ".review",
		"PUMP19_AUTO_MERGE":              "false",
		"PUMP19_CONFIG":                  configRoot,
		"PUMP19_UNIT":                    "pump19-test.service",
		"PUMP19_STANDIN_VERDICT":         "standing-findings",
		"PUMP19_STANDIN_FINDING_FILE":    finding,
		"PUMP19_STANDIN_NEW_HANDLE":      "F-7KQ3",
		"PUMP19_STANDIN_DISPATCH_RECORD": filepath.Join(stateDir, "dispatch.tsv"),
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func assertReviewDispatchRecord(t *testing.T, path, installRoot string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Split(strings.TrimSpace(string(data)), "\n")[0]
	fields := strings.Split(line, "\t")
	if len(fields) != 18 {
		t.Fatalf("dispatch record has %d fields, want 18: %q", len(fields), line)
	}
	wantSkill, err := filepath.Abs(filepath.Join("..", "..", "skills", "foundry", "review-panel", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if fields[0] != "review" || fields[3] != wantSkill || fields[4] != filepath.Join(installRoot, "run-body", "run-body") {
		t.Fatalf("E1 dispatch identity = kind %q skill %q body %q", fields[0], fields[3], fields[4])
	}
	for index, field := range fields {
		if field == "" && index != 17 {
			t.Fatalf("dispatch field %d is empty: %q", index+1, line)
		}
	}
}

func installReviewRunBodyFixture(t *testing.T, installRoot string) {
	t.Helper()
	for _, dir := range []string{"run-body", "missions"} {
		if err := os.MkdirAll(filepath.Join(installRoot, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyFile := func(source, target string, mode os.FileMode) {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	copyFile(filepath.Join("..", "..", "scripts", "run-body", "run-body"), filepath.Join(installRoot, "run-body", "run-body"), 0o755)
	copyFile(filepath.Join("..", "..", "missions", "review.md"), filepath.Join(installRoot, "missions", "review.md"), 0o644)
}

func writeReviewAdaptationFixture(t *testing.T, adaptationDir, stateDir string) {
	t.Helper()
	operations := filepath.Join(stateDir, "operations")
	writeScript(t, filepath.Join(adaptationDir, "prepare-workspace"), `#!/usr/bin/env sh
set -eu
rm -rf "$PUMP19_WORKSPACE"
mkdir -p "$PUMP19_WORKSPACE/.review"
git -C "$PUMP19_WORKSPACE" init -q
git -C "$PUMP19_WORKSPACE" config user.name Test
git -C "$PUMP19_WORKSPACE" config user.email test@example.invalid
printf 'base guidance\n' >"$PUMP19_WORKSPACE/AGENTS.md"
printf 'base brief\n' >"$PUMP19_WORKSPACE/.review/correctness.md"
printf 'base\n' >"$PUMP19_WORKSPACE/file.txt"
git -C "$PUMP19_WORKSPACE" add .
git -C "$PUMP19_WORKSPACE" commit -qm base
git -C "$PUMP19_WORKSPACE" update-ref refs/remotes/origin/main HEAD
printf 'changed\n' >>"$PUMP19_WORKSPACE/file.txt"
git -C "$PUMP19_WORKSPACE" add file.txt
git -C "$PUMP19_WORKSPACE" commit -qm head
git -C "$PUMP19_WORKSPACE" diff origin/main...HEAD >"$PUMP19_DIFF"
`)
	writeScript(t, filepath.Join(adaptationDir, "get-statuses"), "#!/usr/bin/env sh\ncat '"+filepath.Join(stateDir, "statuses.json")+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "get-pr-facts"), `#!/usr/bin/env sh
printf 'get-pr-facts:%s\n' "$PUMP19_HEAD_SHA" >>'`+operations+`'
if [ "${PUMP19_FIXTURE_FLAKY_AFTER_STATUS:-}" = 1 ] &&
   grep -Fxq "set-status:$PUMP19_HEAD_SHA:pump19/review:success" '`+operations+`' &&
   ! grep -Fxq "$PUMP19_HEAD_SHA" '`+filepath.Join(stateDir, "flaky-label-added-after-status")+`' 2>/dev/null; then
  # Add the label on the first facts read after success. This exercises the mission's
  # explicit refresh rather than merely starting the run with a flaky label.
  printf 'Flaky Tests\n' >>'`+filepath.Join(stateDir, "labels")+`'
  printf '%s\n' "$PUMP19_HEAD_SHA" >>'`+filepath.Join(stateDir, "flaky-label-added-after-status")+`'
fi
head=$(cat '`+filepath.Join(stateDir, "head")+`')
labels=$(paste -sd, '`+filepath.Join(stateDir, "labels")+`')
printf 'OCCASION=reconcile\nOWNER=%s\nREPO=%s\nPR=%s\nHEAD_SHA=%s\nBASE_REF=main\nLABELS=%s\n' "$1" "$2" "$3" "$head" "$labels"
printf 'HEAD_BRANCH=main\nHEAD_REPO=pump19/subject\nBASE_REPO=pump19/subject\nMERGEABLE=true\n'
`)
	writeScript(t, filepath.Join(adaptationDir, "add-label"), `#!/usr/bin/env sh
set -eu
printf '%s\n' "$@" >>'`+filepath.Join(stateDir, "labels-added")+`'
printf 'add-label:%s:%s\n' "$PUMP19_HEAD_SHA" "$4" >>'`+operations+`'
if [ "$4" = Ready ] && [ "${PUMP19_FIXTURE_FAIL_READY_APPLY:-}" = 1 ]; then
  echo 'fixture Ready apply failed' >&2
  exit 88
fi
if ! grep -Fxq "$4" '`+filepath.Join(stateDir, "labels")+`'; then printf '%s\n' "$4" >>'`+filepath.Join(stateDir, "labels")+`'; fi
`)
	writeScript(t, filepath.Join(adaptationDir, "remove-label"), `#!/usr/bin/env sh
set -eu
printf '%s\n' "$@" >>'`+filepath.Join(stateDir, "labels-removed")+`'
tmp='`+filepath.Join(stateDir, "labels.tmp")+`'
grep -Fxv "$4" '`+filepath.Join(stateDir, "labels")+`' >"$tmp" || true
mv "$tmp" '`+filepath.Join(stateDir, "labels")+`'
`)
	writeScript(t, filepath.Join(adaptationDir, "add-reaction"), `#!/usr/bin/env sh
set -eu
[ "$PUMP19_FORGE_TOKEN" = test-token ]
printf 'add-reaction:%s\n' "$4" >>'`+operations+`'
`)
	writeScript(t, filepath.Join(adaptationDir, "remove-reaction"), `#!/usr/bin/env sh
set -eu
[ "$PUMP19_FORGE_TOKEN" = test-token ]
printf 'remove-reaction:%s\n' "$4" >>'`+operations+`'
`)
	writeScript(t, filepath.Join(adaptationDir, "assign-if-missing"), `#!/usr/bin/env sh
set -eu
[ "$PUMP19_FORGE_TOKEN" = test-token ]
assignees='`+filepath.Join(stateDir, "assignees")+`'
if [ -f "$assignees" ] && grep -Fxiq "$4" "$assignees"; then exit 0; fi
printf '%s\n' "$4" >>"$assignees"
printf 'assign-if-missing:%s\n' "$4" >>'`+operations+`'
`)
	writeScript(t, filepath.Join(adaptationDir, "set-status"), `#!/usr/bin/env sh
set -eu
printf '%s\n' "$@" >'`+filepath.Join(stateDir, "status.args")+`'
printf 'set-status:%s:%s:%s\n' "$PUMP19_HEAD_SHA" "$4" "$5" >>'`+operations+`'
jq -nc --arg context "$4" --arg state "$5" '[{id:1,context:$context,state:$state,creator:"pump19"}]' >'`+filepath.Join(stateDir, "statuses.json")+`'
`)
	comments := filepath.Join(stateDir, "comments.json")
	reviews := filepath.Join(stateDir, "reviews.json")
	if err := os.WriteFile(comments, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reviews, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptationDir, "list-review-comments"), "#!/usr/bin/env sh\ncat '"+comments+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "post-review"), "#!/usr/bin/env sh\nset -eu\nprintf 'post-review:%s\\n' \"$PUMP19_HEAD_SHA\" >>'"+operations+"'\ncp \"$6\" '"+filepath.Join(stateDir, "posted-review.md")+"'\ncp \"$7\" '"+filepath.Join(stateDir, "posted-comments.json")+"'\njq --arg head \"$4\" --arg state \"$5\" --rawfile body \"$6\" '. + [{state:$state,commit_id:$head,body:$body}]' '"+reviews+"' >'"+reviews+".tmp'\nmv '"+reviews+".tmp' '"+reviews+"'\nif [ \"$(jq 'length' \"$7\")\" -gt 0 ]; then jq '[.[0] + {id:91}]' \"$7\" >'"+comments+"'; fi\nprintf '{}\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "update-comment"), "#!/usr/bin/env sh\ncp \"$4\" '"+filepath.Join(stateDir, "updated-body.md")+"'\nprintf '{}\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "list-reviews"), "#!/usr/bin/env sh\ncat '"+reviews+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "label-actor"), "#!/usr/bin/env sh\nprintf 'pump19\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "merge"), "#!/usr/bin/env sh\nprintf 'merge:%s\\n' \"$PUMP19_HEAD_SHA\" >>'"+operations+"'\nprintf '%s\\n' \"$@\" >'"+filepath.Join(stateDir, "merge.args")+"'\nprintf '{\"merged\":true}\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "post-comment"), "#!/usr/bin/env sh\ncat \"$4\" >>'"+filepath.Join(stateDir, "comments")+"'\nprintf '{}\\n'\n")
}

func assertContainsFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s does not contain %q:\n%s", path, want, data)
	}
}
