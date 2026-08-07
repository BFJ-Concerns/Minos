package shell

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupReconcilesTargetAndPushesMergeToMainlineBranch(t *testing.T) {
	remote, seed := createBareFixtureRemote(t, "AGENTS.md", "SETUP_RECONCILIATION_SENTINEL\n")

	runGit(t, seed, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(seed, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "feature.txt")
	runGit(t, seed, "commit", "-m", "feature")
	runGit(t, seed, "push", "origin", "feature")
	head := gitOutput(t, remote, "rev-parse", "refs/heads/feature")

	runGit(t, seed, "checkout", "main")
	if err := os.WriteFile(filepath.Join(seed, "target.txt"), []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "target.txt")
	runGit(t, seed, "commit", "-m", "target")
	runGit(t, seed, "push", "origin", "main")
	target := gitOutput(t, remote, "rev-parse", "refs/heads/main")

	installPushCounter(t, remote)
	server := newReconciliationSetupForge(t, head, target, remote, "feature", "owner/repository")
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	orientation := filepath.Join(runDir, "orientation.json")
	runSetupReconciliationWorkspace(t, server.URL, runDir, workspace, orientation, head, target)

	if _, err := os.Stat(filepath.Join(remote, "push-count")); err != nil {
		t.Fatalf("setup did not contact the remote push boundary: %v", err)
	}
	published := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
	if published == head {
		t.Fatalf("remote feature remained at original pull-request head %q", head)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/main"); got != target {
		t.Fatalf("remote main = %q, want original target %q", got, target)
	}
	if err := exec.Command("git", "-C", workspace, "merge-base", "--is-ancestor", target, "HEAD").Run(); err != nil {
		t.Fatalf("local workspace does not contain target %s", target)
	}
	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got == head {
		t.Fatalf("local workspace remained at the unreconciled pull-request head %s", head)
	}
	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != published {
		t.Fatalf("workspace head = %q, want published merge %q", got, published)
	}
}

func TestSetupKeepsForkAndAGitReconciliationLocalOnly(t *testing.T) {
	for _, tc := range []struct {
		name, headRef, headRepository string
	}{
		{name: "fork", headRef: "feature", headRepository: "contributor/repository"},
		{name: "AGit", headRef: "refs/pull/17/head", headRepository: "owner/repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote, seed := createBareFixtureRemote(t, "AGENTS.md", "guidance\n")
			runGit(t, seed, "checkout", "-b", "feature")
			if err := os.WriteFile(filepath.Join(seed, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, seed, "add", "feature.txt")
			runGit(t, seed, "commit", "-m", "feature")
			runGit(t, seed, "push", "origin", "feature")
			head := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
			runGit(t, seed, "checkout", "main")
			if err := os.WriteFile(filepath.Join(seed, "target.txt"), []byte("target\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, seed, "add", "target.txt")
			runGit(t, seed, "commit", "-m", "target")
			runGit(t, seed, "push", "origin", "main")
			target := gitOutput(t, remote, "rev-parse", "refs/heads/main")

			server := newReconciliationSetupForge(t, head, target, remote, tc.headRef, tc.headRepository)
			runDir := t.TempDir()
			workspace := filepath.Join(runDir, "workspace")
			runSetupReconciliationWorkspace(t, server.URL, runDir, workspace, filepath.Join(runDir, "orientation.json"), head, target)
			if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != head {
				t.Fatalf("remote feature = %q, want untouched head %q", got, head)
			}
			push := exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/feature")
			if output, err := push.CombinedOutput(); err == nil || !strings.Contains(string(output), "controlled branch updates") {
				t.Fatalf("downstream push result = %v\n%s", err, output)
			}
		})
	}
}

func runSetupReconciliationWorkspace(t *testing.T, apiBase, runDir, workspace, orientation, head, target string) {
	t.Helper()
	credential := filepath.Join(runDir, "forge.token")
	if err := os.WriteFile(credential, []byte("forge-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("..", "..", "scripts", "run-body", "setup-workspace")
	cmd := exec.Command(script)
	cmd.Env = append(os.Environ(),
		"MINOS_API_BASE="+apiBase,
		"MINOS_CREDENTIAL_FILE="+credential,
		"MINOS_OWNER=owner",
		"MINOS_REPO_NAME=repository",
		"MINOS_PR=17",
		"MINOS_HEAD_SHA="+head,
		"MINOS_TARGET_SHA="+target,
		"MINOS_BASE_REF=main",
		"MINOS_HEAD_BRANCH=feature",
		"MINOS_RUN_DIR="+runDir,
		"MINOS_WORKSPACE="+workspace,
		"MINOS_ORIENTATION="+orientation,
		"MINOS_GIT_AUTHOR_NAME=Minos",
		"MINOS_GIT_AUTHOR_EMAIL=minos@example.invalid",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup-workspace failed: %v\n%s", err, output)
	}
}

func newReconciliationSetupForge(t *testing.T, head, target, repository, headRef, headRepository string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token forge-token" {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/repos/owner/repository/pulls/17":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(
				w,
				`{"head":{"sha":%q,"ref":%q,"repo":{"clone_url":%q,"full_name":%q}},"base":{"sha":%q,"ref":"main","repo":{"clone_url":%q,"full_name":"owner/repository"}}}`,
				head,
				headRef,
				repository,
				headRepository,
				target,
				repository,
			)
		case "/api/v1/repos/owner/repository-Annexe":
			http.Error(w, "not found", http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
