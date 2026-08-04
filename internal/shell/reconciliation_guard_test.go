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
	if err := os.WriteFile(filepath.Join(commonDir, "minos-protected-ref"), []byte("refs/heads/reconciled\n"), 0o600); err != nil {
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

func TestPrePushGuardScopesAndConsumesControlledPushPermits(t *testing.T) {
	remote, workspace := createBareFixtureRemote(t, "base.txt", "base\n")
	runGit(t, workspace, "config", "user.name", "Minos")
	runGit(t, workspace, "config", "user.email", "minos@example.invalid")
	base := gitOutput(t, workspace, "rev-parse", "HEAD")
	runGit(t, workspace, "push", "origin", "HEAD:refs/heads/feature")
	runGit(t, workspace, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(workspace, "repair.txt"), []byte("repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "repair.txt")
	runGit(t, workspace, "commit", "-m", "fix: repair")
	repair := gitOutput(t, workspace, "rev-parse", "HEAD")

	commonDir := gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
	guard, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-body", "pre-push-guard"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "hooks", "pre-push"), guard, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "minos-protected-ref"), []byte("refs/heads/feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	outsidePermit := filepath.Join(t.TempDir(), "permit")
	if err := os.WriteFile(outsidePermit, []byte("refs/heads/feature "+repair+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	traversalRoot := filepath.Join(commonDir, "minos-integrate-wave-push.dir")
	if err := os.Mkdir(traversalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	traversalSuffix, err := filepath.Rel(traversalRoot, outsidePermit)
	if err != nil {
		t.Fatal(err)
	}
	traversalPermit := traversalRoot + string(os.PathSeparator) + traversalSuffix
	push := exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/feature")
	push.Env = append(os.Environ(), "MINOS_INTEGRATE_WAVE_PUSH_PERMIT="+traversalPermit)
	output, err := push.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "integrate-wave push permit is outside the shared Git directory") {
		t.Fatalf("traversal permit result = %v\n%s", err, output)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != base {
		t.Fatalf("remote feature = %q after traversal permit, want %q", got, base)
	}

	symlinkPermit := filepath.Join(commonDir, "minos-integrate-wave-push.link")
	if err := os.Symlink(outsidePermit, symlinkPermit); err != nil {
		t.Fatal(err)
	}
	push = exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/feature")
	push.Env = append(os.Environ(), "MINOS_INTEGRATE_WAVE_PUSH_PERMIT="+symlinkPermit)
	output, err = push.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "integrate-wave push permit is outside the shared Git directory") {
		t.Fatalf("symlink permit result = %v\n%s", err, output)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != base {
		t.Fatalf("remote feature = %q after symlink permit, want %q", got, base)
	}

	outsideSyncPermit := filepath.Join(t.TempDir(), "permit")
	if err := os.WriteFile(outsideSyncPermit, []byte("refs/heads/feature "+repair+" "+base+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	syncTraversalRoot := filepath.Join(commonDir, "minos-sync-target-push.dir")
	if err := os.Mkdir(syncTraversalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	syncTraversalSuffix, err := filepath.Rel(syncTraversalRoot, outsideSyncPermit)
	if err != nil {
		t.Fatal(err)
	}
	syncTraversalPermit := syncTraversalRoot + string(os.PathSeparator) + syncTraversalSuffix
	push = exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/feature")
	push.Env = append(os.Environ(), "MINOS_SYNC_TARGET_PUSH_PERMIT="+syncTraversalPermit)
	output, err = push.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "sync-target push permit is outside the shared Git directory") {
		t.Fatalf("sync-target traversal permit result = %v\n%s", err, output)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != base {
		t.Fatalf("remote feature = %q after sync-target traversal permit, want %q", got, base)
	}

	integratePermit := filepath.Join(commonDir, "minos-integrate-wave-push.test")
	if err := os.WriteFile(integratePermit, []byte("refs/heads/feature "+base+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	push = exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/feature")
	push.Env = append(os.Environ(), "MINOS_INTEGRATE_WAVE_PUSH_PERMIT="+integratePermit)
	output, err = push.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "permit authorises a different ref update") {
		t.Fatalf("commit-mismatched permit result = %v\n%s", err, output)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != base {
		t.Fatalf("remote feature = %q after mismatched permit, want %q", got, base)
	}

	if err := os.WriteFile(integratePermit, []byte("refs/heads/feature "+repair+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reconciliation := gitOutput(t, workspace, "commit-tree", base+"^{tree}", "-p", base, "-m", "local reconciliation")
	runGit(t, workspace, "update-ref", "refs/heads/reconciliation", reconciliation)
	if err := os.WriteFile(filepath.Join(commonDir, "minos-unpushable"), []byte(reconciliation+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	push = exec.Command(
		"git", "-C", workspace, "push", "origin",
		"HEAD:refs/heads/feature",
		"refs/heads/reconciliation:refs/heads/rejected",
	)
	push.Env = append(os.Environ(), "MINOS_INTEGRATE_WAVE_PUSH_PERMIT="+integratePermit)
	output, err = push.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "contains local reconciliation commit") {
		t.Fatalf("multi-ref refusal result = %v\n%s", err, output)
	}
	if _, err := os.Stat(integratePermit); err != nil {
		t.Fatalf("multi-ref refusal consumed the controlled-push permit: %v", err)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != base {
		t.Fatalf("remote feature = %q after multi-ref refusal, want %q", got, base)
	}
	if err := exec.Command("git", "--git-dir", remote, "rev-parse", "--verify", "refs/heads/rejected").Run(); err == nil {
		t.Fatal("multi-ref refusal still created the rejected remote ref")
	}

	push = exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/feature")
	push.Env = append(os.Environ(), "MINOS_INTEGRATE_WAVE_PUSH_PERMIT="+integratePermit)
	if output, err := push.CombinedOutput(); err != nil {
		t.Fatalf("exact integrate-wave permit: %v\n%s", err, output)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != repair {
		t.Fatalf("remote feature = %q after exact permit, want %q", got, repair)
	}

	runGit(t, remote, "update-ref", "refs/heads/feature", base)
	push = exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/feature")
	push.Env = append(os.Environ(), "MINOS_INTEGRATE_WAVE_PUSH_PERMIT="+integratePermit)
	output, err = push.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "permit is absent or already used") {
		t.Fatalf("reused permit result = %v\n%s", err, output)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != base {
		t.Fatalf("remote feature = %q after reused permit, want %q", got, base)
	}

	syncPermit := filepath.Join(commonDir, "minos-sync-target-push.test")
	if err := os.WriteFile(syncPermit, []byte("refs/heads/feature "+repair+" 0000000000000000000000000000000000000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	push = exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/feature")
	push.Env = append(os.Environ(), "MINOS_SYNC_TARGET_PUSH_PERMIT="+syncPermit)
	output, err = push.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "permit authorises a different ref update") {
		t.Fatalf("lease-mismatched permit result = %v\n%s", err, output)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != base {
		t.Fatalf("remote feature = %q after lease-mismatched permit, want %q", got, base)
	}
}
