package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrePushGuardRefusesReconciliationAncestryAndAllowsPublicationLineage(t *testing.T) {
	remote, workspace := createBareFixtureRemote(t, "shared.txt", "base\n")
	runGit(t, workspace, "config", "user.name", "Minos")
	runGit(t, workspace, "config", "user.email", "minos@example.invalid")
	base := gitOutput(t, workspace, "rev-parse", "HEAD")

	runGit(t, workspace, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "feature.txt")
	runGit(t, workspace, "commit", "-m", "feature")
	head := gitOutput(t, workspace, "rev-parse", "HEAD")
	runGit(t, workspace, "push", "origin", "feature")

	runGit(t, workspace, "checkout", "-B", "target", base)
	if err := os.WriteFile(filepath.Join(workspace, "target.txt"), []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "target.txt")
	runGit(t, workspace, "commit", "-m", "target")
	target := gitOutput(t, workspace, "rev-parse", "HEAD")

	runGit(t, workspace, "checkout", "--detach", head)
	runGit(t, workspace, "merge", "--no-ff", "--no-edit", target)
	reconciliation := gitOutput(t, workspace, "rev-parse", "HEAD")

	commonDir := gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err := os.WriteFile(filepath.Join(commonDir, "minos-unpushable"), []byte(reconciliation+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	guardSource := filepath.Join("..", "..", "scripts", "run-body", "pre-push-guard")
	guard, err := os.ReadFile(guardSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "hooks", "pre-push"), guard, 0o755); err != nil {
		t.Fatal(err)
	}

	remoteRefsBefore := gitOutput(t, remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	push := exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/reconciled")
	output, err := push.CombinedOutput()
	if err == nil {
		t.Fatalf("push containing reconciliation commit unexpectedly succeeded:\n%s", output)
	}
	if !strings.Contains(string(output), "pre-push-guard: refusing to push") ||
		!strings.Contains(string(output), reconciliation) {
		t.Fatalf("push failed without the guard-specific refusal:\n%s", output)
	}
	if remoteRefsAfter := gitOutput(t, remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); remoteRefsAfter != remoteRefsBefore {
		t.Fatalf("remote refs changed after guard refusal\nbefore:\n%s\nafter:\n%s", remoteRefsBefore, remoteRefsAfter)
	}

	runGit(t, workspace, "checkout", "--detach", head)
	runGit(t, workspace, "push", "origin", "HEAD:refs/heads/published")
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/published"); got != head {
		t.Fatalf("allowed publication head = %q, want %q", got, head)
	}
	if err := exec.Command("git", "--git-dir", remote, "merge-base", "--is-ancestor", reconciliation, "refs/heads/published").Run(); err == nil {
		t.Fatal("allowed publication branch unexpectedly contains the reconciliation commit")
	}
}

func TestPrePushGuardCoversLinkedWorktreesAndFailsClosedOnInvalidRecords(t *testing.T) {
	_, workspace := createBareFixtureRemote(t, "base.txt", "base\n")
	runGit(t, workspace, "config", "user.name", "Minos")
	runGit(t, workspace, "config", "user.email", "minos@example.invalid")
	reconciliation := gitOutput(t, workspace, "rev-parse", "HEAD")
	commonDir := gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
	guard, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-body", "pre-push-guard"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "hooks", "pre-push"), guard, 0o755); err != nil {
		t.Fatal(err)
	}

	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, workspace, "worktree", "add", "--detach", linked, reconciliation)
	if err := os.WriteFile(filepath.Join(commonDir, "minos-unpushable"), []byte(reconciliation+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("git", "-C", linked, "push", "origin", "HEAD:refs/heads/linked").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "pre-push-guard: refusing to push") {
		t.Fatalf("linked-worktree guard result = %v\n%s", err, output)
	}

	if err := os.WriteFile(filepath.Join(commonDir, "minos-unpushable"), []byte("not-a-commit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err = exec.Command("git", "-C", linked, "push", "origin", "HEAD:refs/heads/invalid-record").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "reconciliation record contains an invalid commit") {
		t.Fatalf("invalid record was not fail-closed: %v\n%s", err, output)
	}
}

func TestPrePushGuardAllowsPushesBeforeAnyReconciliationIsRecorded(t *testing.T) {
	remote, workspace := createBareFixtureRemote(t, "base.txt", "base\n")
	commonDir := gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
	guard, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-body", "pre-push-guard"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "hooks", "pre-push"), guard, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit(t, workspace, "push", "origin", "HEAD:refs/heads/no-record")
	if err := os.WriteFile(filepath.Join(commonDir, "minos-unpushable"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "push", "origin", "HEAD:refs/heads/empty-record")
	head := gitOutput(t, workspace, "rev-parse", "HEAD")
	for _, branch := range []string{"no-record", "empty-record"} {
		if got := gitOutput(t, remote, "rev-parse", "refs/heads/"+branch); got != head {
			t.Fatalf("%s = %q, want %q", branch, got, head)
		}
	}
}
