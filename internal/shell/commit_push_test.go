package shell

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// commitPushRepo is a disposable bare remote plus a prepared workspace checkout,
// the shape the real fix run lands into.
type commitPushRepo struct {
	root      string
	remote    string
	workspace string
	branchTip string
}

func setupCommitPushRepo(t *testing.T) commitPushRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "minos", "subject.git")
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
	workspace := filepath.Join(root, "workspace")
	git(root, "clone", remote, workspace)
	git(workspace, "checkout", "--detach", branchTip)
	return commitPushRepo{root: root, remote: remote, workspace: workspace, branchTip: branchTip}
}

func (r commitPushRepo) run(t *testing.T, token string, extraEnv ...string) (string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "commit-push"))
	if err != nil {
		t.Fatal(err)
	}
	message := filepath.Join(r.root, "message.txt")
	if err := os.WriteFile(message, []byte("Answer the finding.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, "minos", "subject", "7", "feature",
		"Minos", "minos@bfj.invalid", message)
	cmd.Env = append(os.Environ(),
		"MINOS_API_BASE=file://"+r.root,
		"MINOS_FORGE_TOKEN="+token,
		"MINOS_WORKSPACE="+r.workspace,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	// Capture stdout alone — the outcome token — the way guarded-commit-push does
	// through command substitution. The git diagnostics on stderr are noise for
	// the token, folded into the error only when the run fails.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil {
		err = fmt.Errorf("%w: %s", err, stderr.String())
	}
	return stdout.String(), err
}

func gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestCommitPushLandsAttributedFastForward proves a fix commit is attributed to
// the service identity with the model trailer, fast-forwards rather than
// rewriting, and refuses an empty tree.
func TestCommitPushLandsAttributedFastForward(t *testing.T) {
	repo := setupCommitPushRepo(t)

	// Empty tree: the fruitless outcome, never an empty commit.
	out, err := repo.run(t, "secrettoken")
	if err != nil {
		t.Fatalf("empty-tree commit-push failed: %v\n%s", err, out)
	}
	if strings.Fields(out)[0] != "fruitless" {
		t.Fatalf("empty tree did not report fruitless:\n%s", out)
	}

	if err := os.WriteFile(filepath.Join(repo.workspace, "file.txt"), []byte("head\nfix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = repo.run(t, "secrettoken")
	if err != nil {
		t.Fatalf("commit-push failed: %v\n%s", err, out)
	}
	if strings.Fields(out)[0] != "landed" {
		t.Fatalf("landed fix did not report landed:\n%s", out)
	}

	newTip := gitAt(t, repo.remote, "rev-parse", "feature")
	if newTip == repo.branchTip {
		t.Fatal("feature branch did not advance")
	}
	if parent := gitAt(t, repo.remote, "rev-parse", "feature^"); parent != repo.branchTip {
		t.Fatalf("fix commit was not a fast-forward: parent %s, old tip %s", parent, repo.branchTip)
	}
	if author := gitAt(t, repo.remote, "log", "-1", "--format=%an <%ae>", "feature"); author != "Minos <minos@bfj.invalid>" {
		t.Fatalf("fix commit misattributed: %q", author)
	}
	if body := gitAt(t, repo.remote, "log", "-1", "--format=%B", "feature"); strings.Contains(body, "Minos-Model:") {
		t.Fatalf("fix commit exposed deployment model provenance:\n%s", body)
	}
}

// TestCommitPushKeepsCredentialFromWorkspaceHooks is the F1 regression. It
// installs repository-controlled hooks that try to exfiltrate the credential at
// both commit and push, then proves the real secret is unreachable — the
// credentialled git never runs with workspace hooks or config in scope.
func TestCommitPushKeepsCredentialFromWorkspaceHooks(t *testing.T) {
	repo := setupCommitPushRepo(t)
	secret := "s3cr3t-verifier-token"
	exfil := filepath.Join(repo.root, "exfil.txt")

	// Hostile hooks: pre-commit and pre-push both dump the environment and probe
	// GIT_ASKPASS for the password. Wired in via the workspace's own local config.
	hooks := filepath.Join(repo.root, "hostile-hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\n" +
		"{\n" +
		"  echo \"=== $(basename \"$0\") ===\"\n" +
		"  env\n" +
		"  if [ -n \"$GIT_ASKPASS\" ] && [ -x \"$GIT_ASKPASS\" ]; then\n" +
		"    echo \"askpass=[$(\"$GIT_ASKPASS\" 'Password: ' 2>/dev/null)]\"\n" +
		"  fi\n" +
		"} >> '" + exfil + "' 2>&1\n" +
		"exit 0\n"
	for _, name := range []string{"pre-commit", "pre-push"} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte(hook), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitAt(t, repo.workspace, "config", "core.hooksPath", hooks)

	if err := os.WriteFile(filepath.Join(repo.workspace, "file.txt"), []byte("head\nfix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := repo.run(t, secret)
	if err != nil {
		t.Fatalf("commit-push failed under hostile hooks: %v\n%s", err, out)
	}

	// The secret must appear nowhere a workspace-controlled surface could reach.
	if data, readErr := os.ReadFile(exfil); readErr == nil && strings.Contains(string(data), secret) {
		t.Fatalf("credential recovered by a workspace hook:\n%s", data)
	}
	if config, _ := os.ReadFile(filepath.Join(repo.workspace, ".git", "config")); strings.Contains(string(config), secret) {
		t.Fatal("credential leaked into workspace .git/config")
	}
	if strings.Contains(out, secret) {
		t.Fatal("credential leaked to commit-push output")
	}
	// The fix still landed despite the hostile hooks.
	if gitAt(t, repo.remote, "rev-parse", "feature") == repo.branchTip {
		t.Fatal("fix did not land under hostile hooks")
	}
}

// TestCommitPushClassifiesRemoteRejection is the F3 regression: a protected head
// whose push the remote refuses reports the unwritable outcome, distinct from an
// infrastructure failure (which would exit non-zero). The outcome rides stdout
// at exit 0 because guarded-commit-push consumes it through command substitution;
// a non-zero command substitution cannot carry that typed outcome.
func TestCommitPushClassifiesRemoteRejection(t *testing.T) {
	repo := setupCommitPushRepo(t)
	// A pre-receive hook that declines every push stands in for a protected
	// same-repository branch.
	preReceive := filepath.Join(repo.remote, "hooks", "pre-receive")
	if err := os.MkdirAll(filepath.Dir(preReceive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preReceive, []byte("#!/bin/sh\necho 'protected branch: declined' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.workspace, "file.txt"), []byte("head\nfix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := repo.run(t, "secrettoken")
	if err != nil {
		t.Fatalf("rejected push should be a handled outcome, not an error: %v\n%s", err, out)
	}
	if strings.Fields(out)[0] != "unwritable" {
		t.Fatalf("rejected push did not report unwritable:\n%s", out)
	}
	if gitAt(t, repo.remote, "rev-parse", "feature") != repo.branchTip {
		t.Fatal("rejected push moved the remote branch")
	}
}
