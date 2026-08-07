package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

	push = exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/feature")
	output, err = push.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "controlled branch updates") {
		t.Fatalf("absent permit result = %v\n%s", err, output)
	}

	outsideReconcilePermit := filepath.Join(t.TempDir(), "minos-reconcile-target-push.test")
	if err := os.WriteFile(outsideReconcilePermit, []byte("refs/heads/feature "+repair+" "+base+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	push = exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/feature")
	push.Env = append(os.Environ(), "MINOS_RECONCILE_TARGET_PUSH_PERMIT="+outsideReconcilePermit)
	output, err = push.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "reconcile-target push permit is outside the shared Git directory") {
		t.Fatalf("outside reconcile permit result = %v\n%s", err, output)
	}

	reconcileSymlink := filepath.Join(commonDir, "minos-reconcile-target-push.link")
	if err := os.Symlink(outsideReconcilePermit, reconcileSymlink); err != nil {
		t.Fatal(err)
	}
	push = exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/feature")
	push.Env = append(os.Environ(), "MINOS_RECONCILE_TARGET_PUSH_PERMIT="+reconcileSymlink)
	output, err = push.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "reconcile-target push permit is outside the shared Git directory") {
		t.Fatalf("symlink reconcile permit result = %v\n%s", err, output)
	}

	reconcilePermit := filepath.Join(commonDir, "minos-reconcile-target-push.test")
	if err := os.WriteFile(reconcilePermit, []byte("refs/heads/feature "+repair+" "+base+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	push = exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/feature")
	push.Env = append(os.Environ(),
		"MINOS_RECONCILE_TARGET_PUSH_PERMIT="+reconcilePermit,
		"MINOS_INTEGRATE_WAVE_PUSH_PERMIT="+outsideReconcilePermit,
	)
	output, err = push.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "multiple controlled-push permits") {
		t.Fatalf("multiple permit result = %v\n%s", err, output)
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
