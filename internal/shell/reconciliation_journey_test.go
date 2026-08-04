package shell

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	AutoMergeTree *string  `json:"autoMergeTree"`
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
	if state.Outcome != "conflict" || state.Merge != nil || len(state.Conflicts) != 1 ||
		state.Conflicts[0] != "shared.txt" || state.PreimageDir == nil || state.AutoMergeTree == nil {
		t.Fatalf("conflict state = %+v", state)
	}
	runGit(t, workspace, "cat-file", "-e", *state.AutoMergeTree+"^{tree}")
	if _, err := os.Stat(filepath.Join(gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-dir"), "MERGE_HEAD")); err != nil {
		t.Fatalf("MERGE_HEAD missing: %v", err)
	}
	assertContainsFile(t, filepath.Join(*state.PreimageDir, "shared.txt"), "<<<<<<<")

	complete := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "complete-reconciliation"), workspace)
	complete.Env = append(os.Environ(), "MINOS_RUN_DIR="+runDir, "MINOS_BASE_REF=main")
	completeOutput, err := complete.CombinedOutput()
	if err == nil || !strings.Contains(string(completeOutput), "unmerged paths remain") {
		t.Fatalf("unmerged completion was not refused: %v\n%s", err, completeOutput)
	}

	if err := os.WriteFile(filepath.Join(workspace, "shared.txt"), []byte("feature and target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "shared.txt")
	staged := strings.Fields(gitOutput(t, workspace, "diff", "--cached", "--name-only"))
	if !slices.Equal(staged, []string{"shared.txt", "sibling.txt"}) {
		t.Fatalf("staged merge paths = %v, want resolved conflict plus clean target path", staged)
	}
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
	setupResultPath := filepath.Join(runDir, "setup-result.json")
	setupRetryResultPath := filepath.Join(runDir, "setup-retry-result.json")
	if err := os.WriteFile(setupResultPath, []byte("stale setup result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(setupRetryResultPath, []byte("accepted retry result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	complete = exec.Command(filepath.Join("..", "..", "scripts", "run-body", "complete-reconciliation"), workspace)
	complete.Env = append(os.Environ(), "MINOS_RUN_DIR="+runDir, "MINOS_BASE_REF=main")
	if completeOutput, err = complete.CombinedOutput(); err != nil {
		t.Fatalf("complete-reconciliation: %v\n%s", err, completeOutput)
	}
	accepted := readReconciliationState(t, statePath)
	if accepted.Outcome != "reconciled" || accepted.Merge == nil {
		t.Fatalf("accepted state = %+v", accepted)
	}
	assertContainsFile(t, setupResultPath, "accepted retry result")
	if _, err := os.Stat(setupRetryResultPath); !os.IsNotExist(err) {
		t.Fatalf("setup retry result survived completed reconciliation: %v", err)
	}
	if got := gitOutput(t, workspace, "show", "HEAD:sibling.txt"); got != "clean target content" {
		t.Fatalf("completed reconciliation dropped clean target content: %q", got)
	}

	buildAndTest := exec.Command(
		"sh", "-c",
		`test "$(cat shared.txt)" = "feature and target" &&
		 test "$(cat sibling.txt)" = "clean target content" &&
		 test "$(git rev-parse HEAD^1)" = "$EXPECTED_HEAD" &&
		 test "$(git rev-parse HEAD^2)" = "$EXPECTED_TARGET"`,
	)
	buildAndTest.Dir = workspace
	buildAndTest.Env = append(os.Environ(), "EXPECTED_HEAD="+head, "EXPECTED_TARGET="+target)
	if stageOutput, err := buildAndTest.CombinedOutput(); err != nil {
		t.Fatalf("merged-tree build/test consumer failed: %v\n%s", err, stageOutput)
	}
	remoteRefsBefore := gitOutput(t, repository, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	forbiddenPush := exec.Command("git", "-C", workspace, "push", "origin", "HEAD:refs/heads/forbidden-reconciliation")
	forbiddenOutput, err := forbiddenPush.CombinedOutput()
	if err == nil || !strings.Contains(string(forbiddenOutput), "pre-push-guard: refusing to push") {
		t.Fatalf("reconciled workspace push did not fire the guard: %v\n%s", err, forbiddenOutput)
	}
	if remoteRefsAfter := gitOutput(t, repository, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); remoteRefsAfter != remoteRefsBefore {
		t.Fatalf("remote refs changed after reconciled workspace refusal\nbefore:\n%s\nafter:\n%s", remoteRefsBefore, remoteRefsAfter)
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
	if got := gitOutput(t, workspace, "show", "HEAD:sibling.txt"); got != "clean target content" {
		t.Fatalf("rerere reconciliation dropped clean target content: %q", got)
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

func TestIntegrateWavePublishesOnlyTheHeadLineageAndReconcilesTheReadingTree(t *testing.T) {
	repository, head, target := createDivergedSetupRepository(t, false)
	server := newSetupForge(t, head, repository, "")
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	runSetupWorkspaceForTarget(t, server.URL, runDir, workspace, head, target)
	before := readReconciliationState(t, filepath.Join(runDir, "reconciliation.json"))

	publication := *before.Publication
	agentWorktree := filepath.Join(t.TempDir(), "agent")
	runGit(t, publication, "worktree", "add", "--detach", agentWorktree, "HEAD")
	if err := os.WriteFile(filepath.Join(agentWorktree, "repair.txt"), []byte("repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, agentWorktree, "add", "repair.txt")
	runGit(t, agentWorktree, "-c", "user.name=Minos", "-c", "user.email=minos@example.invalid", "commit", "-m", "fix: repair")
	repair := gitOutput(t, agentWorktree, "rev-parse", "HEAD")
	commits := writeJSONFixture(t, []string{repair})

	remoteRefsBefore := gitOutput(t, repository, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	unsanctioned := exec.Command("git", "-C", agentWorktree, "push", "origin", "HEAD:refs/heads/feature")
	unsanctionedOutput, err := unsanctioned.CombinedOutput()
	if err == nil || !strings.Contains(string(unsanctionedOutput), "worker fixes must be routed through workflows/integrate-wave") {
		t.Fatalf("unsanctioned author-branch push result = %v\n%s", err, unsanctionedOutput)
	}
	if remoteRefsAfter := gitOutput(t, repository, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); remoteRefsAfter != remoteRefsBefore {
		t.Fatalf("remote refs changed after author-branch refusal\nbefore:\n%s\nafter:\n%s", remoteRefsBefore, remoteRefsAfter)
	}

	runGit(t, agentWorktree, "push", "origin", "HEAD:refs/heads/worker-evidence")
	if got := gitOutput(t, repository, "rev-parse", "refs/heads/worker-evidence"); got != repair {
		t.Fatalf("unprotected remote ref = %q, want %q", got, repair)
	}

	setupScript, err := filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", "setup-workspace"))
	if err != nil {
		t.Fatal(err)
	}
	integrate := exec.Command(filepath.Join("..", "..", "workflows", "integrate-wave"), workspace, commits)
	integrate.Env = append(os.Environ(),
		"MINOS_RUN_DIR="+runDir,
		"MINOS_SETUP_WORKSPACE="+setupScript,
		"MINOS_BASE_REF=main",
		"MINOS_HEAD_BRANCH=feature",
		"MINOS_GIT_AUTHOR_NAME=Minos",
		"MINOS_GIT_AUTHOR_EMAIL=minos@example.invalid",
	)
	output, err := integrate.CombinedOutput()
	if err != nil || !strings.Contains(string(output), `"outcome":"reconciled"`) {
		t.Fatalf("integrate-wave: %v\n%s", err, output)
	}

	publishedHead := gitOutput(t, repository, "rev-parse", "refs/heads/feature")
	if err := exec.Command("git", "-C", repository, "merge-base", "--is-ancestor", *before.Merge, publishedHead).Run(); err == nil {
		t.Fatal("published fix lineage contains the local reconciliation merge")
	}
	if got := gitOutput(t, repository, "show", "refs/heads/feature:repair.txt"); got != "repair" {
		t.Fatalf("published repair = %q", got)
	}
	after := readReconciliationState(t, filepath.Join(runDir, "reconciliation.json"))
	if after.Merge == nil || *after.Merge == *before.Merge || after.BaseHead != publishedHead {
		t.Fatalf("round reconciliation state = %+v; previous merge = %s", after, *before.Merge)
	}
	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != *after.Merge {
		t.Fatalf("reading tree head = %q, want %q", got, *after.Merge)
	}
	parents := strings.Fields(gitOutput(t, workspace, "show", "-s", "--format=%P", "HEAD"))
	if len(parents) != 2 || parents[0] != publishedHead || parents[1] != target {
		t.Fatalf("round reconciliation parents = %v, want [%s %s]", parents, publishedHead, target)
	}
}

func TestIntegrateWaveReportsANewRoundConflictAfterPublishingTheFix(t *testing.T) {
	repository, head, target := createDivergedSetupRepository(t, false)
	server := newSetupForge(t, head, repository, "")
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	runSetupWorkspaceForTarget(t, server.URL, runDir, workspace, head, target)
	state := readReconciliationState(t, filepath.Join(runDir, "reconciliation.json"))

	agentWorktree := filepath.Join(t.TempDir(), "agent")
	runGit(t, *state.Publication, "worktree", "add", "--detach", agentWorktree, "HEAD")
	if err := os.WriteFile(filepath.Join(agentWorktree, "target.txt"), []byte("fix-side addition\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, agentWorktree, "add", "target.txt")
	runGit(t, agentWorktree, "-c", "user.name=Minos", "-c", "user.email=minos@example.invalid", "commit", "-m", "fix: collide with target")
	repair := gitOutput(t, agentWorktree, "rev-parse", "HEAD")
	commits := writeJSONFixture(t, []string{repair})
	setupScript, err := filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", "setup-workspace"))
	if err != nil {
		t.Fatal(err)
	}
	integrate := exec.Command(filepath.Join("..", "..", "workflows", "integrate-wave"), workspace, commits)
	integrate.Env = append(os.Environ(),
		"MINOS_RUN_DIR="+runDir,
		"MINOS_SETUP_WORKSPACE="+setupScript,
		"MINOS_BASE_REF=main",
		"MINOS_HEAD_BRANCH=feature",
		"MINOS_GIT_AUTHOR_NAME=Minos",
		"MINOS_GIT_AUTHOR_EMAIL=minos@example.invalid",
	)
	output, err := integrate.CombinedOutput()
	if err == nil || !strings.Contains(string(output), `"outcome":"reconcile-conflict"`) ||
		!strings.Contains(string(output), `"target.txt"`) {
		t.Fatalf("round conflict result = %v\n%s", err, output)
	}
	if got := gitOutput(t, repository, "show", "refs/heads/feature:target.txt"); got != "fix-side addition" {
		t.Fatalf("fix was not published before round conflict: %q", got)
	}
	after := readReconciliationState(t, filepath.Join(runDir, "reconciliation.json"))
	if after.Outcome != "conflict" || len(after.Conflicts) != 1 || after.Conflicts[0] != "target.txt" {
		t.Fatalf("round conflict state = %+v", after)
	}
}

func TestSyncTargetUsesThePublicationTreeAndRefusesAReconciledCheckout(t *testing.T) {
	repository, head, target := createDivergedSetupRepository(t, false)
	server := newSetupForge(t, head, repository, "")
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	runSetupWorkspaceForTarget(t, server.URL, runDir, workspace, head, target)
	statePath := filepath.Join(runDir, "reconciliation.json")
	state := readReconciliationState(t, statePath)

	if err := os.WriteFile(filepath.Join(repository, "later-target.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "later-target.txt")
	runGit(t, repository, "commit", "-m", "move target")
	currentTarget := gitOutput(t, repository, "rev-parse", "main")

	sync := exec.Command(
		filepath.Join("..", "..", "scripts", "run-body", "sync-target"),
		workspace, "feature", "main", head, currentTarget, "merge",
	)
	sync.Env = append(os.Environ(), "MINOS_RUN_DIR="+runDir)
	output, err := sync.CombinedOutput()
	if err != nil || !strings.Contains(string(output), `"outcome":"synced"`) {
		t.Fatalf("redirected sync-target: %v\n%s", err, output)
	}
	published := gitOutput(t, repository, "rev-parse", "feature")
	if err := exec.Command("git", "-C", repository, "merge-base", "--is-ancestor", *state.Merge, published).Run(); err == nil {
		t.Fatal("finishing sync published the setup reconciliation merge")
	}
	if err := exec.Command("git", "-C", repository, "merge-base", "--is-ancestor", currentTarget, published).Run(); err != nil {
		t.Fatal("finishing sync did not publish the current target")
	}

	stateData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(stateData, &raw); err != nil {
		t.Fatal(err)
	}
	raw["publication"] = nil
	stateData, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, stateData, 0o600); err != nil {
		t.Fatal(err)
	}
	refusal := exec.Command(
		filepath.Join("..", "..", "scripts", "run-body", "sync-target"),
		workspace, "feature", "main", published, currentTarget, "merge",
	)
	refusal.Env = append(os.Environ(), "MINOS_RUN_DIR="+runDir)
	refusalOutput, err := refusal.CombinedOutput()
	if err == nil || !strings.Contains(string(refusalOutput), "this checkout carries local reconciliation commit") {
		t.Fatalf("reconciled-checkout refusal = %v\n%s", err, refusalOutput)
	}
}

func TestSyncTargetReusesAnAcceptedSetupResolutionWithoutPublishingSetupMerge(t *testing.T) {
	repository, head, target := createDivergedSetupRepository(t, true)
	server := newSetupForge(t, head, repository, "")
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	runSetupWorkspaceForTarget(t, server.URL, runDir, workspace, head, target)
	statePath := filepath.Join(runDir, "reconciliation.json")

	if err := os.WriteFile(filepath.Join(workspace, "shared.txt"), []byte("feature and target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "shared.txt")
	complete := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "complete-reconciliation"), workspace)
	complete.Env = append(os.Environ(), "MINOS_RUN_DIR="+runDir, "MINOS_BASE_REF=main")
	if output, err := complete.CombinedOutput(); err != nil {
		t.Fatalf("complete setup reconciliation: %v\n%s", err, output)
	}
	state := readReconciliationState(t, statePath)

	first := exec.Command(
		filepath.Join("..", "..", "scripts", "run-body", "sync-target"),
		workspace, "feature", "main", head, target, "merge",
	)
	first.Env = append(os.Environ(), "MINOS_RUN_DIR="+runDir)
	firstOutput, err := first.CombinedOutput()
	if err == nil || !strings.Contains(string(firstOutput), `"outcome":"conflict"`) {
		t.Fatalf("first finishing sync = %v\n%s", err, firstOutput)
	}
	publication := *state.Publication
	if got := gitOutput(t, publication, "show", ":shared.txt"); got != "feature and target" {
		t.Fatalf("rerere staged resolution = %q", got)
	}
	runGit(t, publication, "commit", "-m", "Merge target branch")

	second := exec.Command(
		filepath.Join("..", "..", "scripts", "run-body", "sync-target"),
		workspace, "feature", "main", head, target, "merge",
	)
	second.Env = append(os.Environ(), "MINOS_RUN_DIR="+runDir)
	secondOutput, err := second.CombinedOutput()
	if err != nil || !strings.Contains(string(secondOutput), `"outcome":"synced"`) {
		t.Fatalf("resumed finishing sync = %v\n%s", err, secondOutput)
	}
	published := gitOutput(t, repository, "rev-parse", "feature")
	if got := gitOutput(t, repository, "show", "feature:shared.txt"); got != "feature and target" {
		t.Fatalf("published rerere resolution = %q", got)
	}
	if got := gitOutput(t, repository, "show", "feature:sibling.txt"); got != "clean target content" {
		t.Fatalf("finishing sync dropped clean target content: %q", got)
	}
	if err := exec.Command("git", "-C", repository, "merge-base", "--is-ancestor", *state.Merge, published).Run(); err == nil {
		t.Fatal("finishing sync published the setup reconciliation merge")
	}
}

func TestCompleteReconciliationAllowsOnlyRecordedConflictResolutions(t *testing.T) {
	t.Run("ordinary conflict with a clean target path completes", func(t *testing.T) {
		runDir, workspace := createConflictCompletionEstate(t)
		resolveConflict(t, workspace)
		output, err := runCompleteReconciliation(workspace, runDir)
		if err != nil {
			t.Fatalf("ordinary completion: %v\n%s", err, output)
		}
		if got := gitOutput(t, workspace, "show", "HEAD:sibling.txt"); got != "clean target content" {
			t.Fatalf("ordinary completion dropped clean target content: %q", got)
		}
	})

	t.Run("an unrelated staged path is refused and shown", func(t *testing.T) {
		runDir, workspace := createConflictCompletionEstate(t)
		resolveConflict(t, workspace)
		if err := os.WriteFile(filepath.Join(workspace, "unrelated.txt"), []byte("invented\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, workspace, "add", "unrelated.txt")
		output, err := runCompleteReconciliation(workspace, runDir)
		if err == nil || !strings.Contains(output, "staged paths outside recorded conflicts differ from Git's auto-merge result") {
			t.Fatalf("unrelated staged path was not refused: %v\n%s", err, output)
		}
		assertUnexpectedResolutionShown(t, runDir, workspace, "unrelated.txt", "invented")
	})

	t.Run("a rewritten clean auto-merge is refused and shown", func(t *testing.T) {
		runDir, workspace := createConflictCompletionEstate(t)
		resolveConflict(t, workspace)
		if err := os.WriteFile(filepath.Join(workspace, "sibling.txt"), []byte("agent rewrite\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, workspace, "add", "sibling.txt")
		output, err := runCompleteReconciliation(workspace, runDir)
		if err == nil || !strings.Contains(output, "staged paths outside recorded conflicts differ from Git's auto-merge result") {
			t.Fatalf("rewritten clean auto-merge was not refused: %v\n%s", err, output)
		}
		assertUnexpectedResolutionShown(t, runDir, workspace, "sibling.txt", "agent rewrite")
	})

	t.Run("an unresolved conflict stays refused", func(t *testing.T) {
		runDir, workspace := createConflictCompletionEstate(t)
		output, err := runCompleteReconciliation(workspace, runDir)
		if err == nil || !strings.Contains(output, "unmerged paths remain") {
			t.Fatalf("unresolved conflict was not refused: %v\n%s", err, output)
		}
	})

	t.Run("an unstaged post-resolution edit stays refused", func(t *testing.T) {
		runDir, workspace := createConflictCompletionEstate(t)
		resolveConflict(t, workspace)
		if err := os.WriteFile(filepath.Join(workspace, "shared.txt"), []byte("unstaged rewrite\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		output, err := runCompleteReconciliation(workspace, runDir)
		if err == nil || !strings.Contains(output, "unstaged tracked changes remain") {
			t.Fatalf("unstaged rewrite was not refused: %v\n%s", err, output)
		}
	})
}

func createConflictCompletionEstate(t *testing.T) (string, string) {
	t.Helper()
	repository, head, target := createDivergedSetupRepository(t, true)
	server := newSetupForge(t, head, repository, "")
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	runSetupWorkspaceForTarget(t, server.URL, runDir, workspace, head, target)
	return runDir, workspace
}

func resolveConflict(t *testing.T, workspace string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workspace, "shared.txt"), []byte("feature and target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "shared.txt")
}

func runCompleteReconciliation(workspace, runDir string) (string, error) {
	command := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "complete-reconciliation"), workspace)
	command.Env = append(os.Environ(), "MINOS_RUN_DIR="+runDir, "MINOS_BASE_REF=main")
	output, err := command.CombinedOutput()
	return string(output), err
}

func assertUnexpectedResolutionShown(t *testing.T, runDir, workspace, path, content string) {
	t.Helper()
	show := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "show-resolutions"))
	show.Env = append(os.Environ(), "MINOS_RUN_DIR="+runDir, "MINOS_WORKSPACE="+workspace)
	output, err := show.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "=== staged outside recorded conflicts") ||
		!strings.Contains(string(output), path) || !strings.Contains(string(output), content) {
		t.Fatalf("unexpected staged change was not shown: %v\n%s", err, output)
	}
}

func runSetupWorkspaceForTarget(t *testing.T, apiBase, runDir, workspace, head, target string) {
	t.Helper()
	cmd := setupWorkspaceCommandForTarget(
		t, apiBase, runDir, workspace, filepath.Join(runDir, "orientation.json"), head, target,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup-workspace: %v\n%s", err, output)
	}
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
	if conflict {
		if err := os.WriteFile(filepath.Join(repository, "sibling.txt"), []byte("clean target content\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repository, "add", ".")
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
