package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	t.Setenv("PUMP19_STANDIN_LEAD_MODEL", "floating-alias-surprise")
	if err := RunWrapCommand(t.Context(), []string{"--config", configRoot}); err == nil {
		t.Fatal("model mismatch unexpectedly completed")
	}
	assertContainsFile(t, filepath.Join(stateDir, "status.args"), "pump19/review\nerror")
	postedAfterMismatch, err := os.ReadFile(filepath.Join(stateDir, "posted-review.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(postedAfterMismatch) != string(postedBeforeMismatch) {
		t.Fatal("model mismatch posted a new review")
	}
}

func setReviewRunEnv(t *testing.T, root, configRoot, installRoot, head, finding, resolved string) {
	t.Helper()
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
		"PUMP19_SKILL":                   filepath.Join(root, "skill", "SKILL.md"),
		"PUMP19_RUN_BODY":                filepath.Join(installRoot, "run-body", "run-body"),
		"PUMP19_BRIEFS":                  ".review",
		"PUMP19_CONFIG":                  configRoot,
		"PUMP19_UNIT":                    "pump19-test.service",
		"PUMP19_STANDIN_VERDICT":         "standing-findings",
		"PUMP19_STANDIN_FINDING_FILE":    finding,
		"PUMP19_STANDIN_NEW_HANDLE":      "F-7KQ3",
		"PUMP19_STANDIN_RESOLVED_MODELS": resolved,
	}
	for key, value := range values {
		t.Setenv(key, value)
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
	writeScript(t, filepath.Join(adaptationDir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "get-pr-facts"), `#!/usr/bin/env sh
printf 'OCCASION=reconcile\nOWNER=%s\nREPO=%s\nPR=%s\nHEAD_SHA=%s\nBASE_REF=main\nLABELS=\n' "$1" "$2" "$3" "$PUMP19_HEAD_SHA"
`)
	writeScript(t, filepath.Join(adaptationDir, "add-label"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >>'"+filepath.Join(stateDir, "labels-added")+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "remove-label"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >>'"+filepath.Join(stateDir, "labels-removed")+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "set-status"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >'"+filepath.Join(stateDir, "status.args")+"'\n")
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
