package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractGoverningReadsBriefsAndGuidanceFromBaseRef(t *testing.T) {
	workspace := t.TempDir()
	runDir := t.TempDir()
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(runDir, "governing"), 0o755)
		_ = filepath.Walk(filepath.Join(runDir, "governing"), func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(path, 0o755)
			}
			return nil
		})
	})
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("checkout", "-q", "-b", "main")
	writeRepoFile(t, workspace, "AGENTS.md", "base guidance\n")
	writeRepoFile(t, workspace, "nested/CLAUDE.md", "nested base guidance\n")
	writeRepoFile(t, workspace, ".review/correctness.md", "base brief\n")
	git("add", ".")
	git("commit", "-qm", "base")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	writeRepoFile(t, workspace, "AGENTS.md", "head injection\n")
	writeRepoFile(t, workspace, ".review/new-on-head.md", "must not govern\n")
	git("add", ".")
	git("commit", "-qm", "head")

	script := filepath.Join("..", "..", "scripts", "review", "extract-governing")
	cmd := exec.Command(script)
	cmd.Env = append(os.Environ(),
		"MINOS_WORKSPACE="+workspace,
		"MINOS_RUN_DIR="+runDir,
		"MINOS_BASE_REF=main",
		"MINOS_BRIEFS=.review",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("extract-governing failed: %v\n%s", err, out)
	}
	assertFileText(t, filepath.Join(runDir, "governing", "AGENTS.md"), "base guidance\n")
	assertFileText(t, filepath.Join(runDir, "governing", "nested", "CLAUDE.md"), "nested base guidance\n")
	assertFileText(t, filepath.Join(runDir, "governing", ".review", "correctness.md"), "base brief\n")
	if _, err := os.Stat(filepath.Join(runDir, "governing", ".review", "new-on-head.md")); !os.IsNotExist(err) {
		t.Fatalf("head-only brief entered governing tree: %v", err)
	}
	info, err := os.Stat(filepath.Join(runDir, "governing", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("governing file is writable: %o", info.Mode().Perm())
	}
}

func TestExtractGoverningRejectsParentBriefPath(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "review", "extract-governing")
	cmd := exec.Command(script)
	cmd.Env = append(os.Environ(), "MINOS_WORKSPACE=x", "MINOS_RUN_DIR=x", "MINOS_BASE_REF=main", "MINOS_BRIEFS=../escape")
	if err := cmd.Run(); err == nil {
		t.Fatal("parent brief path was accepted")
	}
}

func TestReviewEngineStandInPropagatesGoverningExtractionFailure(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	reviewScripts := filepath.Join(root, "review-scripts")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(reviewScripts, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(reviewScripts, "extract-governing"), "#!/usr/bin/env sh\nexit 17\n")
	minos := filepath.Join(root, "minos-fixture")
	writeScript(t, minos, `#!/usr/bin/env sh
case "$1:$4" in
  run-guard:begin) printf 'claimed\n' ;;
  run-guard:release) ;;
  *) printf '%s\n' "$*" >"$MINOS_UNEXPECTED_CALL"; exit 99 ;;
esac
`)
	standin := filepath.Join("..", "..", "scripts", "e2e", "review-engine-standin")
	cmd := exec.Command(standin)
	cmd.Stdin = strings.NewReader("mission")
	cmd.Env = append(os.Environ(),
		"MINOS_BIN="+minos,
		"MINOS_REVIEW_SCRIPTS="+reviewScripts,
		"MINOS_RUN_DIR="+runDir,
		"MINOS_CONFIG="+root,
		"MINOS_PINS="+filepath.Join(root, "pins.toml"),
		"MINOS_STANDIN_RESOLVED_MODELS="+filepath.Join(root, "workers.json"),
		"MINOS_UNEXPECTED_CALL="+filepath.Join(root, "unexpected-call"),
	)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("stand-in ignored governing extraction failure:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "unexpected-call")); !os.IsNotExist(err) {
		t.Fatalf("stand-in continued after governing extraction failure: %v", err)
	}
}

func writeRepoFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFileText(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, strings.TrimSpace(string(data)), strings.TrimSpace(want))
	}
}
