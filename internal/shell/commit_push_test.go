package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommitPushLandsAttributedFastForward exercises the real commit-push
// adaptation against a local bare repository — the git plumbing the fix run's
// landing rests on. It proves the fix commit is attributed to the service
// identity with the model trailer, that the branch fast-forwards rather than
// being rewritten, and that an empty tree is refused rather than committed. The
// credential path is unused for a file:// remote, so the leak guard is checked
// but not stressed here; the live push is a go-live proof.
func TestCommitPushLandsAttributedFastForward(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "pump19", "subject.git")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Seed", "GIT_AUTHOR_EMAIL=seed@x.invalid",
			"GIT_COMMITTER_NAME=Seed", "GIT_COMMITTER_EMAIL=seed@x.invalid",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(root, "init", "--bare", "-b", "main", remote)

	// Seed the remote with a head branch to fast-forward.
	seed := filepath.Join(root, "seed")
	git(root, "clone", remote, seed)
	if err := os.WriteFile(filepath.Join(seed, "file.txt"), []byte("head\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(seed, "add", ".")
	git(seed, "commit", "-m", "base")
	git(seed, "push", "origin", "main")
	git(seed, "push", "origin", "main:feature")
	branchTip := git(seed, "rev-parse", "HEAD")

	// Prepare the workspace as the run would see it: a detached checkout of the
	// head, no credential in its config.
	workspace := filepath.Join(root, "workspace")
	git(root, "clone", remote, workspace)
	git(workspace, "checkout", "--detach", branchTip)

	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "commit-push"))
	if err != nil {
		t.Fatal(err)
	}
	message := filepath.Join(root, "message.txt")
	if err := os.WriteFile(message, []byte("Answer the finding.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommitPush := func() (string, error) {
		cmd := exec.Command(script, "pump19", "subject", "7", "feature",
			"Pump-19", "pump19@bfj.invalid", "test-model", message)
		cmd.Env = append(os.Environ(),
			"PUMP19_API_BASE=file://"+root,
			"PUMP19_FORGE_TOKEN=secrettoken",
			"PUMP19_WORKSPACE="+workspace,
		)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// Empty tree: refused with a non-zero exit, never an empty commit. The
	// mission reads exit 3 as the fruitless outcome.
	out, err := runCommitPush()
	if err == nil {
		t.Fatalf("commit-push committed an empty tree:\n%s", out)
	}
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
		t.Fatalf("empty-tree commit-push did not exit 3: %v\n%s", err, out)
	}

	// A real change lands.
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("head\nfix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = runCommitPush()
	if err != nil {
		t.Fatalf("commit-push failed: %v\n%s", err, out)
	}

	// The branch advanced by exactly one commit whose parent is the old tip: a
	// fast-forward, not a rewrite.
	newTip := git(remote, "rev-parse", "feature")
	if newTip == branchTip {
		t.Fatal("feature branch did not advance")
	}
	if parent := git(remote, "rev-parse", "feature^"); parent != branchTip {
		t.Fatalf("fix commit was not a fast-forward: parent %s, old tip %s", parent, branchTip)
	}
	author := git(remote, "log", "-1", "--format=%an <%ae>", "feature")
	if author != "Pump-19 <pump19@bfj.invalid>" {
		t.Fatalf("fix commit misattributed: %q", author)
	}
	body := git(remote, "log", "-1", "--format=%B", "feature")
	if !strings.Contains(body, "Pump-19-Model: test-model") {
		t.Fatalf("fix commit missing model trailer:\n%s", body)
	}

	// The credential never reaches the workspace git config.
	config, _ := os.ReadFile(filepath.Join(workspace, ".git", "config"))
	if strings.Contains(string(config), "secrettoken") {
		t.Fatal("credential leaked into workspace .git/config")
	}
}
