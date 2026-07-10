package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewRunBodyPostsAndUpdatesFindingThroughRunWrap(t *testing.T) {
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
	resolved := filepath.Join(root, "resolved.json")
	if err := os.WriteFile(pins, []byte(pinsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	resolvedJSON := `[{"id":"spec-claude","resolved_model":"claude-opus-pinned"},{"id":"verify-codex","resolved_model":"model-unknown"},{"id":"bar-codex","resolved_model":"model-unknown"}]`
	if err := os.WriteFile(resolved, []byte(resolvedJSON), 0o644); err != nil {
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
	service := "[listener]\nbind = \":0\"\n\n[forges.local]\nadaptation = \"" + adaptationDir + "\"\napi-base = \"http://forge.invalid\"\nwebhook-secret-file = \"" + webhookSecret + "\"\ncredential-file = \"" + token + "\"\n\n[runs]\ndir = \"" + filepath.Join(root, "runs") + "\"\n\n[sweep]\nliveness-threshold = \"1h\"\n"
	if err := os.WriteFile(filepath.Join(configRoot, "service.toml"), []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReviewAdaptationFixture(t, adaptationDir, stateDir)

	finding1 := filepath.Join(root, "finding-1.json")
	if err := os.WriteFile(finding1, []byte(`{"path":"file.txt","line":2,"priority":"P1","body":"First finding"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	setReviewRunEnv(t, root, configRoot, installRoot, "aaaaaaaaaaaaaaaa", finding1, resolved)
	if err := RunWrapCommand(t.Context(), []string{"--config", configRoot}); err != nil {
		t.Fatal(err)
	}
	firstRun := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", "aaaaaaaaaaaaaaaa", RunReview)
	assertContainsFile(t, filepath.Join(firstRun, "review.md"), "verdict=standing-findings")
	assertContainsFile(t, filepath.Join(firstRun, "new-comments.json"), "finding=F-7KQ3")
	assertContainsFile(t, filepath.Join(firstRun, "governing", "AGENTS.md"), "base guidance")
	assertContainsFile(t, filepath.Join(stateDir, "status.args"), "pump19/review\nsuccess")
	assertReviewDispatchRecord(t, filepath.Join(stateDir, "dispatch.tsv"), installRoot)

	finding2 := filepath.Join(root, "finding-2.json")
	if err := os.WriteFile(finding2, []byte(`{"finding":"F-7KQ3","path":"file.txt","line":2,"priority":"P1","body":"Still present"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	setReviewRunEnv(t, root, configRoot, installRoot, "bbbbbbbbbbbbbbbb", finding2, resolved)
	if err := RunWrapCommand(t.Context(), []string{"--config", configRoot}); err != nil {
		t.Fatal(err)
	}
	secondRun := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", "bbbbbbbbbbbbbbbb", RunReview)
	assertContainsFile(t, filepath.Join(stateDir, "updated-body.md"), "Still present")
	assertContainsFile(t, filepath.Join(stateDir, "updated-body.md"), "finding=F-7KQ3")
	assertFileText(t, filepath.Join(secondRun, "new-comments.json"), "[]\n")

	setReviewRunEnv(t, root, configRoot, installRoot, "cccccccccccccccc", "", resolved)
	t.Setenv("PUMP19_STANDIN_VERDICT", "converged")
	if err := RunWrapCommand(t.Context(), []string{"--config", configRoot}); err != nil {
		t.Fatal(err)
	}
	cleanRun := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", "cccccccccccccccc", RunReview)
	assertContainsFile(t, filepath.Join(cleanRun, "review.md"), "coverage=full")
	assertContainsFile(t, filepath.Join(cleanRun, "review.md"), "verdict=converged")
	assertContainsFile(t, filepath.Join(stateDir, "labels-added"), "Converged")

	setReviewRunEnv(t, root, configRoot, installRoot, "dddddddddddddddd", "", resolved)
	t.Setenv("PUMP19_STANDIN_VERDICT", "partial-coverage")
	if err := RunWrapCommand(t.Context(), []string{"--config", configRoot}); err != nil {
		t.Fatal(err)
	}
	partialRun := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", "dddddddddddddddd", RunReview)
	assertContainsFile(t, filepath.Join(partialRun, "review.md"), "coverage=partial")
	assertContainsFile(t, filepath.Join(partialRun, "review.md"), "verdict=partial-coverage")
	assertContainsFile(t, filepath.Join(stateDir, "labels-added"), "Partial Coverage")

	// The head may move after the run claims Reviewing but before it posts. Hold
	// the deterministic engine at that exact seam, advance the forge fixture,
	// and prove the real mission path yields without a mutation.
	postedBeforeStale, err := os.ReadFile(filepath.Join(stateDir, "posted-review.md"))
	if err != nil {
		t.Fatal(err)
	}
	staleReady := filepath.Join(root, "stale-ready")
	staleContinue := filepath.Join(root, "stale-continue")
	setReviewRunEnv(t, root, configRoot, installRoot, "ffffffffffffffff", "", resolved)
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
	mismatch := filepath.Join(root, "resolved-mismatch.json")
	mismatchJSON := resolvedJSON
	if err := os.WriteFile(mismatch, []byte(mismatchJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	setReviewRunEnv(t, root, configRoot, installRoot, "eeeeeeeeeeeeeeee", "", mismatch)
	t.Setenv("PUMP19_STANDIN_VERDICT", "converged")
	t.Setenv("PUMP19_STANDIN_MISMATCH_AFTER_CLAIM", "floating-alias-surprise")
	t.Setenv("PUMP19_UNIT", "")
	if err := RunWrapCommand(t.Context(), []string{"--config", configRoot}); err == nil {
		t.Fatal("model mismatch unexpectedly completed")
	}
	assertContainsFile(t, filepath.Join(stateDir, "status.args"), "pump19/review\nerror")
	assertContainsFile(t, filepath.Join(stateDir, "labels"), "Reviewing")
	postedAfterMismatch, err := os.ReadFile(filepath.Join(stateDir, "posted-review.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(postedAfterMismatch) != string(postedBeforeMismatch) {
		t.Fatal("model mismatch posted a new review")
	}

	// A hard model-mismatch abort cannot run the child's EXIT trap. Age its real
	// run log and prove the reconciliation sweep reaps the claim and removes the
	// orphaned Reviewing label while respecting the wrapper's terminal status.
	mismatchRun := RunDir(filepath.Join(root, "runs"), "local", "pump19", "subject", "42", "eeeeeeeeeeeeeeee", RunReview)
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
	if err := sweepPR(t.Context(), cfg, RepoConfig{}, Adaptation{Dir: adaptationDir}, facts, logFile); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mismatchRun); !os.IsNotExist(err) {
		t.Fatalf("sweep did not reap the mismatched run claim: %v", err)
	}
	assertContainsFile(t, filepath.Join(stateDir, "labels-removed"), "Reviewing")
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

func setReviewRunEnv(t *testing.T, root, configRoot, installRoot, head, finding, resolved string) {
	t.Helper()
	skill, err := filepath.Abs(filepath.Join("..", "..", "skills", "foundry", "agent-review", "SKILL.md"))
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
		"PUMP19_CONFIG":                  configRoot,
		"PUMP19_UNIT":                    "pump19-test.service",
		"PUMP19_STANDIN_VERDICT":         "standing-findings",
		"PUMP19_STANDIN_FINDING_FILE":    finding,
		"PUMP19_STANDIN_NEW_HANDLE":      "F-7KQ3",
		"PUMP19_STANDIN_RESOLVED_MODELS": resolved,
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
	wantSkill, err := filepath.Abs(filepath.Join("..", "..", "skills", "foundry", "agent-review", "SKILL.md"))
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
head=$(cat '`+filepath.Join(stateDir, "head")+`')
labels=$(paste -sd, '`+filepath.Join(stateDir, "labels")+`')
printf 'OCCASION=reconcile\nOWNER=%s\nREPO=%s\nPR=%s\nHEAD_SHA=%s\nBASE_REF=main\nLABELS=%s\n' "$1" "$2" "$3" "$head" "$labels"
`)
	writeScript(t, filepath.Join(adaptationDir, "add-label"), `#!/usr/bin/env sh
set -eu
printf '%s\n' "$@" >>'`+filepath.Join(stateDir, "labels-added")+`'
if ! grep -Fxq "$4" '`+filepath.Join(stateDir, "labels")+`'; then printf '%s\n' "$4" >>'`+filepath.Join(stateDir, "labels")+`'; fi
`)
	writeScript(t, filepath.Join(adaptationDir, "remove-label"), `#!/usr/bin/env sh
set -eu
printf '%s\n' "$@" >>'`+filepath.Join(stateDir, "labels-removed")+`'
tmp='`+filepath.Join(stateDir, "labels.tmp")+`'
grep -Fxv "$4" '`+filepath.Join(stateDir, "labels")+`' >"$tmp" || true
mv "$tmp" '`+filepath.Join(stateDir, "labels")+`'
`)
	writeScript(t, filepath.Join(adaptationDir, "set-status"), `#!/usr/bin/env sh
set -eu
printf '%s\n' "$@" >'`+filepath.Join(stateDir, "status.args")+`'
jq -nc --arg context "$4" --arg state "$5" '[{id:1,context:$context,state:$state,creator:"pump19"}]' >'`+filepath.Join(stateDir, "statuses.json")+`'
`)
	comments := filepath.Join(stateDir, "comments.json")
	if err := os.WriteFile(comments, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptationDir, "list-review-comments"), "#!/usr/bin/env sh\ncat '"+comments+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "post-review"), "#!/usr/bin/env sh\nset -eu\ncp \"$6\" '"+filepath.Join(stateDir, "posted-review.md")+"'\ncp \"$7\" '"+filepath.Join(stateDir, "posted-comments.json")+"'\nif [ \"$(jq 'length' \"$7\")\" -gt 0 ]; then jq '[.[0] + {id:91}]' \"$7\" >'"+comments+"'; fi\nprintf '{}\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "update-comment"), "#!/usr/bin/env sh\ncp \"$4\" '"+filepath.Join(stateDir, "updated-body.md")+"'\nprintf '{}\\n'\n")
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
