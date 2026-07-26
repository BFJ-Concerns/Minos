package shell

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type reconciliationState struct {
	Outcome       string   `json:"outcome"`
	Fork          bool     `json:"fork"`
	Target        string   `json:"target"`
	BaseHead      string   `json:"baseHead"`
	Merge         *string  `json:"merge"`
	Conflicts     []string `json:"conflicts"`
	PreimageDir   *string  `json:"preimageDir"`
	Publication   *string  `json:"publication"`
	TargetBranch  string   `json:"targetBranch"`
	TargetRepoURL string   `json:"targetRepositoryUrl"`
}

func TestSetupWorkspaceCreatesSeparatedReconciledAndPublicationTrees(t *testing.T) {
	repository, head, target := createDivergedSetupRepository(t, false)
	server := newSetupForge(t, head, repository, "")
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	orientation := filepath.Join(runDir, "orientation.json")

	cmd := setupWorkspaceCommandForTarget(t, server.URL, runDir, workspace, orientation, head, target)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup-workspace failed: %v\n%s", err, output)
	}

	state := readReconciliationState(t, filepath.Join(runDir, "reconciliation.json"))
	if state.Outcome != "reconciled" || state.Merge == nil || state.Publication == nil || state.Fork {
		t.Fatalf("reconciliation state = %+v", state)
	}
	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != *state.Merge {
		t.Fatalf("reading head = %q, want reconciliation %q", got, *state.Merge)
	}
	parents := strings.Fields(gitOutput(t, workspace, "show", "-s", "--format=%P", *state.Merge))
	if len(parents) != 2 || parents[0] != head || parents[1] != target {
		t.Fatalf("reconciliation parents = %v, want [%s %s]", parents, head, target)
	}
	if got := gitOutput(t, *state.Publication, "rev-parse", "HEAD"); got != head {
		t.Fatalf("publication head = %q, want %q", got, head)
	}
	if got := gitOutput(t, *state.Publication, "symbolic-ref", "--short", "HEAD"); got != "feature" {
		t.Fatalf("publication branch = %q, want feature", got)
	}
	if got := gitOutput(t, workspace, "config", "--get", "rerere.enabled"); got != "true" {
		t.Fatalf("rerere.enabled = %q", got)
	}
	if got := gitOutput(t, workspace, "config", "--get", "rerere.autoupdate"); got != "true" {
		t.Fatalf("rerere.autoupdate = %q", got)
	}
	if got := gitOutput(t, workspace, "config", "--get", "merge.conflictstyle"); got != "zdiff3" {
		t.Fatalf("merge.conflictstyle = %q", got)
	}
	commonDir := gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
	assertContainsFile(t, filepath.Join(commonDir, "minos-unpushable"), *state.Merge)
	if info, err := os.Stat(filepath.Join(commonDir, "hooks", "pre-push")); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("pre-push hook is not installed executable: %v", err)
	}
	if got := gitOutput(t, workspace, "rev-parse", "refs/minos/reconciled"); got != *state.Merge {
		t.Fatalf("refs/minos/reconciled = %q, want %q", got, *state.Merge)
	}
}

func TestSetupWorkspaceLeavesConflictForCheckedCompletionAndRerereReuse(t *testing.T) {
	repository, head, target := createDivergedSetupRepository(t, true)
	server := newSetupForge(t, head, repository, "")
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	orientation := filepath.Join(runDir, "orientation.json")
	statePath := filepath.Join(runDir, "reconciliation.json")

	cmd := setupWorkspaceCommandForTarget(t, server.URL, runDir, workspace, orientation, head, target)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("conflicted setup should continue: %v\n%s", err, output)
	}
	state := readReconciliationState(t, statePath)
	if state.Outcome != "conflict" || state.Merge != nil || len(state.Conflicts) != 1 || state.Conflicts[0] != "shared.txt" || state.PreimageDir == nil {
		t.Fatalf("conflict state = %+v", state)
	}
	if _, err := os.Stat(filepath.Join(gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-dir"), "MERGE_HEAD")); err != nil {
		t.Fatalf("MERGE_HEAD missing: %v", err)
	}
	assertContainsFile(t, filepath.Join(*state.PreimageDir, "shared.txt"), "<<<<<<<")

	if err := os.WriteFile(filepath.Join(workspace, "shared.txt"), []byte("feature and target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "shared.txt")
	show := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "show-resolutions"))
	show.Env = append(os.Environ(), "MINOS_RUN_DIR="+runDir, "MINOS_WORKSPACE="+workspace)
	showOutput, err := show.CombinedOutput()
	if err != nil || !strings.Contains(string(showOutput), "=== shared.txt") ||
		!strings.Contains(string(showOutput), "feature and target") ||
		!strings.Contains(string(showOutput), "<<<<<<<") {
		t.Fatalf("show-resolutions = %v\n%s", err, showOutput)
	}

	reopen := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "reopen-conflict"), workspace, "shared.txt")
	if reopenOutput, err := reopen.CombinedOutput(); err != nil {
		t.Fatalf("reopen-conflict: %v\n%s", err, reopenOutput)
	}
	assertContainsFile(t, filepath.Join(workspace, "shared.txt"), "<<<<<<<")
	if err := os.WriteFile(filepath.Join(workspace, "shared.txt"), []byte("feature and target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "shared.txt")
	complete := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "complete-reconciliation"), workspace)
	complete.Env = append(os.Environ(), "MINOS_RUN_DIR="+runDir, "MINOS_BASE_REF=main")
	if completeOutput, err := complete.CombinedOutput(); err != nil {
		t.Fatalf("complete-reconciliation: %v\n%s", err, completeOutput)
	}
	accepted := readReconciliationState(t, statePath)
	if accepted.Outcome != "reconciled" || accepted.Merge == nil {
		t.Fatalf("accepted state = %+v", accepted)
	}

	publication := *accepted.Publication
	if err := os.WriteFile(filepath.Join(publication, "round.txt"), []byte("round\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, publication, "add", "round.txt")
	runGit(t, publication, "commit", "-m", "fix: next round")
	nextHead := gitOutput(t, publication, "rev-parse", "HEAD")
	reconcile := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "reconcile-target"), workspace, nextHead)
	reconcile.Env = append(os.Environ(), "MINOS_RUN_DIR="+runDir, "MINOS_BASE_REF=main")
	reconcileOutput, err := reconcile.CombinedOutput()
	if err != nil || !strings.Contains(string(reconcileOutput), `"outcome":"reconciled"`) {
		t.Fatalf("rerere reconciliation = %v\n%s", err, reconcileOutput)
	}
	replayed := readReconciliationState(t, statePath)
	if replayed.Merge == nil || *replayed.Merge == *accepted.Merge {
		t.Fatalf("rerere did not create a fresh reconciliation merge: before=%v after=%v", accepted.Merge, replayed.Merge)
	}
	if got := gitOutput(t, workspace, "show", "HEAD:shared.txt"); got != "feature and target" {
		t.Fatalf("rerere result = %q", got)
	}
}

func TestSetupWorkspaceRefusesMovedTargetAndReconcilesForkWithoutPublication(t *testing.T) {
	t.Run("moved target", func(t *testing.T) {
		repository, head, target := createDivergedSetupRepository(t, false)
		server := newSetupForge(t, head, repository, "")
		runDir := t.TempDir()
		cmd := setupWorkspaceCommandForTarget(
			t, server.URL, runDir, filepath.Join(runDir, "workspace"),
			filepath.Join(runDir, "orientation.json"), head, strings.Repeat("f", len(target)),
		)
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "pull-request target moved") {
			t.Fatalf("moved target result = %v\n%s", err, output)
		}
	})

	t.Run("fork", func(t *testing.T) {
		baseRepository, _, target := createDivergedSetupRepository(t, false)
		forkRepository := filepath.Join(t.TempDir(), "fork")
		runGit(t, t.TempDir(), "clone", baseRepository, forkRepository)
		runGit(t, forkRepository, "config", "user.name", "Fixture")
		runGit(t, forkRepository, "config", "user.email", "fixture@example.invalid")
		runGit(t, forkRepository, "checkout", "-B", "feature", target+"^")
		if err := os.WriteFile(filepath.Join(forkRepository, "fork.txt"), []byte("fork\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(forkRepository, "AGENTS.md"), []byte("# Guidance\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, forkRepository, "add", "fork.txt", "AGENTS.md")
		runGit(t, forkRepository, "commit", "-m", "fork feature")
		head := gitOutput(t, forkRepository, "rev-parse", "HEAD")
		server := newForkSetupForge(t, head, forkRepository, baseRepository)
		runDir := t.TempDir()
		workspace := filepath.Join(runDir, "workspace")
		cmd := setupWorkspaceCommandForTarget(t, server.URL, runDir, workspace, filepath.Join(runDir, "orientation.json"), head, target)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fork setup: %v\n%s", err, output)
		}
		state := readReconciliationState(t, filepath.Join(runDir, "reconciliation.json"))
		if !state.Fork || state.Publication != nil || state.Outcome != "reconciled" {
			t.Fatalf("fork state = %+v", state)
		}
		if got := gitOutput(t, workspace, "rev-parse", "refs/minos/target"); got != target {
			t.Fatalf("fork pinned target = %q, want %q", got, target)
		}
	})
}

func createDivergedSetupRepository(t *testing.T, conflict bool) (string, string, string) {
	t.Helper()
	repository, _ := createGitRepository(t, "shared.txt", "base\n")
	if err := os.WriteFile(filepath.Join(repository, "AGENTS.md"), []byte("# Guidance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "AGENTS.md")
	runGit(t, repository, "commit", "-m", "guidance")
	base := gitOutput(t, repository, "rev-parse", "HEAD")
	runGit(t, repository, "checkout", "-b", "feature", base)
	featureName := "feature.txt"
	if conflict {
		featureName = "shared.txt"
	}
	if err := os.WriteFile(filepath.Join(repository, featureName), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", featureName)
	runGit(t, repository, "commit", "-m", "feature")
	head := gitOutput(t, repository, "rev-parse", "HEAD")
	runGit(t, repository, "checkout", "main")
	targetName := "target.txt"
	if conflict {
		targetName = "shared.txt"
	}
	if err := os.WriteFile(filepath.Join(repository, targetName), []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", targetName)
	runGit(t, repository, "commit", "-m", "target")
	target := gitOutput(t, repository, "rev-parse", "HEAD")
	return repository, head, target
}

func newForkSetupForge(t *testing.T, head, headRepository, baseRepository string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token forge-token" {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/repos/owner/repository/pulls/17":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"head":{"sha":%q,"ref":"feature","repo":{"clone_url":%q,"full_name":"contributor/repository"}},
				"base":{"ref":"main","repo":{"clone_url":%q,"full_name":"owner/repository"}}
			}`, head, headRepository, baseRepository)
		case "/api/v1/repos/owner/repository-Annexe":
			http.Error(w, "not found", http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func readReconciliationState(t *testing.T, path string) reconciliationState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state reconciliationState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}
