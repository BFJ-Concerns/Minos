package shell

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSetupReconcilesTargetLocallyWithoutPushingMergeAncestry(t *testing.T) {
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
	server := newReconciliationSetupForge(t, head, target, remote)
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	orientation := filepath.Join(runDir, "orientation.json")
	runSetupReconciliationWorkspace(t, server.URL, runDir, workspace, orientation, head, target)

	if _, err := os.Stat(filepath.Join(remote, "push-count")); !os.IsNotExist(err) {
		t.Fatalf("setup contacted the remote push boundary: %v", err)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != head {
		t.Fatalf("remote feature = %q, want original pull-request head %q", got, head)
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

func newReconciliationSetupForge(t *testing.T, head, target, repository string) *httptest.Server {
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
				`{"head":{"sha":%q,"ref":"feature","repo":{"clone_url":%q,"full_name":"owner/repository"}},"base":{"sha":%q,"ref":"main","repo":{"clone_url":%q,"full_name":"owner/repository"}}}`,
				head,
				repository,
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
