package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrePushGuardRefusesProtectedPushWithValidRecordAndCleanEnvironment(t *testing.T) {
	fixture := newPrePushGuardFixture(t)

	output, err := fixture.push("HEAD:refs/heads/main")
	if err == nil {
		t.Fatalf("protected push with a valid record and clean environment succeeded:\n%s", output)
	}
	assertPrePushGuardOutput(t, output, "refusing to update refs/heads/main")
	fixture.assertRemoteRef(t, "refs/heads/main", fixture.initialHead)
}

func TestPrePushGuardRefusesProtectedPushWithPermitShapedEnvironment(t *testing.T) {
	fixture := newPrePushGuardFixture(t)
	permit := fixture.permitForProtectedPush(t, "minos-integrate-wave-push.test")

	output, err := fixture.push("HEAD:refs/heads/main", "MINOS_INTEGRATE_WAVE_PUSH_PERMIT="+permit)
	if err == nil {
		t.Fatalf("protected push with permit-shaped environment succeeded:\n%s", output)
	}
	assertPrePushGuardOutput(t, output, "refusing to update refs/heads/main")
	fixture.assertRemoteRef(t, "refs/heads/main", fixture.initialHead)
}

func TestPrePushGuardFailsClosedForUnusableProtectedRefRecord(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *prePushGuardFixture)
		want    string
	}{
		{
			name: "malformed",
			prepare: func(t *testing.T, fixture *prePushGuardFixture) {
				fixture.writeProtectedRef(t, "not-a-ref\n", 0o600)
			},
			want: "protected-ref record is invalid",
		},
		{
			name: "empty",
			prepare: func(t *testing.T, fixture *prePushGuardFixture) {
				fixture.writeProtectedRef(t, "", 0o600)
			},
			want: "protected-ref record is invalid: <empty>",
		},
		{
			name: "unreadable",
			prepare: func(t *testing.T, fixture *prePushGuardFixture) {
				// A directory at the record path is unreadable as a file for
				// every uid; chmod 0o000 would not deter root (CI containers).
				if err := os.Remove(fixture.protectedRef); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(fixture.protectedRef, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "protected-ref record is present but unreadable",
		},
		{
			name: "missing",
			prepare: func(t *testing.T, fixture *prePushGuardFixture) {
				if err := os.Remove(fixture.protectedRef); err != nil {
					t.Fatal(err)
				}
			},
			want: "protected-ref record is missing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrePushGuardFixture(t)
			test.prepare(t, fixture)

			output, err := fixture.push("HEAD:refs/heads/main")
			if err == nil {
				t.Fatalf("protected push with %s record succeeded:\n%s", test.name, output)
			}
			assertPrePushGuardOutput(t, output, test.want)
			fixture.assertRemoteRef(t, "refs/heads/main", fixture.initialHead)
		})
	}
}

func TestPrePushGuardAllowsUnprotectedPushButNeverAProtectedPush(t *testing.T) {
	fixture := newPrePushGuardFixture(t)

	output, err := fixture.push("HEAD:refs/heads/review-notes")
	if err != nil {
		t.Fatalf("unprotected push failed: %v\n%s", err, output)
	}
	fixture.assertRemoteRef(t, "refs/heads/review-notes", fixture.localHead(t))

	permit := fixture.permitForProtectedPush(t, "minos-integrate-wave-push.test")
	output, err = fixture.push("HEAD:refs/heads/main", "MINOS_INTEGRATE_WAVE_PUSH_PERMIT="+permit)
	if err == nil {
		t.Fatalf("protected push after an unprotected push succeeded:\n%s", output)
	}
	assertPrePushGuardOutput(t, output, "refusing to update refs/heads/main")
	fixture.assertRemoteRef(t, "refs/heads/main", fixture.initialHead)
}

type prePushGuardFixture struct {
	remote       string
	workspace    string
	protectedRef string
	commonDir    string
	initialHead  string
}

func newPrePushGuardFixture(t *testing.T) *prePushGuardFixture {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	workspace := filepath.Join(root, "workspace")
	runGit(t, root, "init", "--bare", remote)
	runGit(t, root, "init", "-b", "main", workspace)
	runGit(t, workspace, "config", "user.name", "Minos")
	runGit(t, workspace, "config", "user.email", "minos@example.invalid")
	if err := os.WriteFile(filepath.Join(workspace, "review.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "review.txt")
	runGit(t, workspace, "commit", "-m", "test: seed protected branch")
	runGit(t, workspace, "remote", "add", "origin", remote)
	runGit(t, workspace, "push", "-u", "origin", "main")
	initialHead := gitOutput(t, workspace, "rev-parse", "HEAD")

	commonDir := gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
	guard := filepath.Join("..", "..", "scripts", "run-body", "pre-push-guard")
	contents, err := os.ReadFile(guard)
	if err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(commonDir, "hooks", "pre-push")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, contents, 0o755); err != nil {
		t.Fatal(err)
	}

	fixture := &prePushGuardFixture{
		remote:       remote,
		workspace:    workspace,
		protectedRef: filepath.Join(commonDir, "minos-protected-ref"),
		commonDir:    commonDir,
		initialHead:  initialHead,
	}
	fixture.writeProtectedRef(t, "refs/heads/main\n", 0o600)
	if err := os.WriteFile(filepath.Join(workspace, "review.txt"), []byte("reviewed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "review.txt")
	runGit(t, workspace, "commit", "-m", "test: advance review head")
	return fixture
}

func (fixture *prePushGuardFixture) writeProtectedRef(t *testing.T, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(fixture.protectedRef, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixture.protectedRef, mode); err != nil {
		t.Fatal(err)
	}
}

func (fixture *prePushGuardFixture) push(refspec string, environment ...string) ([]byte, error) {
	cmd := exec.Command("git", "-C", fixture.workspace, "push", "origin", refspec)
	cmd.Env = append(os.Environ(), environment...)
	return cmd.CombinedOutput()
}

func (fixture *prePushGuardFixture) localHead(t *testing.T) string {
	t.Helper()
	return gitOutput(t, fixture.workspace, "rev-parse", "HEAD")
}

func (fixture *prePushGuardFixture) permitForProtectedPush(t *testing.T, name string) string {
	t.Helper()
	permit := filepath.Join(fixture.commonDir, name)
	update := strings.Join([]string{"refs/heads/main", fixture.localHead(t), fixture.initialHead}, " ")
	if err := os.WriteFile(permit, []byte(update), 0o600); err != nil {
		t.Fatal(err)
	}
	return permit
}

func (fixture *prePushGuardFixture) assertRemoteRef(t *testing.T, ref, want string) {
	t.Helper()
	if got := gitOutput(t, fixture.remote, "rev-parse", ref); got != want {
		t.Fatalf("remote %s = %q, want %q", ref, got, want)
	}
}

func assertPrePushGuardOutput(t *testing.T, output []byte, want string) {
	t.Helper()
	if !strings.Contains(string(output), want) {
		t.Fatalf("push output = %q, want %q", output, want)
	}
}
