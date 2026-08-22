package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReapRemovesWorktreeBuildDirsAndKeepsEvidence(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	worktrees := workspace + ".ensemble-workflows-worktrees"
	mustMkdirAll(t, workspace)
	mustMkdirAll(t, filepath.Join(worktrees, "leg-one", "target", "debug", "deps"))
	mustWriteFile(t, filepath.Join(worktrees, "leg-one", "target", "debug", "deps", "libcrate.rlib"))
	mustWriteFile(t, filepath.Join(worktrees, "leg-one", "notes.md"))
	mustMkdirAll(t, filepath.Join(worktrees, "leg-two", "node_modules", "pkg"))
	mustWriteFile(t, filepath.Join(worktrees, "leg-two", "node_modules", "pkg", "index.js"))
	mustWriteFile(t, filepath.Join(worktrees, "leg-two", "finding.json"))

	runReapWorktreeBuildDirs(t, workspace)

	for _, gone := range []string{
		filepath.Join(worktrees, "leg-one", "target"),
		filepath.Join(worktrees, "leg-two", "node_modules"),
	} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("build directory %s survived the reap (stat err = %v)", gone, err)
		}
	}
	for _, kept := range []string{
		filepath.Join(worktrees, "leg-one", "notes.md"),
		filepath.Join(worktrees, "leg-two", "finding.json"),
	} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("worktree evidence %s did not survive the reap: %v", kept, err)
		}
	}
}

func TestReapResolvesTheWorkspaceRootFromASubdirectory(t *testing.T) {
	repository, _ := createGitRepository(t, "code.txt", "reviewed code\n")
	worktrees := repository + ".ensemble-workflows-worktrees"
	mustMkdirAll(t, filepath.Join(worktrees, "leg", "target"))
	mustWriteFile(t, filepath.Join(worktrees, "leg", "target", "artefact"))
	nested := filepath.Join(repository, "crates", "app")
	mustMkdirAll(t, nested)

	runReapWorktreeBuildDirs(t, nested)

	if _, err := os.Stat(filepath.Join(worktrees, "leg", "target")); !os.IsNotExist(err) {
		t.Fatalf("build directory survived a reap addressed via a workspace subdirectory (stat err = %v)", err)
	}
}

func TestReapToleratesAWorkspaceWithoutWorktrees(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	mustMkdirAll(t, workspace)

	runReapWorktreeBuildDirs(t, workspace)
}

func TestReapRejectsAMissingWorkspace(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "run-body", "reap-worktree-build-dirs")
	output, err := exec.Command(script, filepath.Join(t.TempDir(), "absent")).CombinedOutput()
	if err == nil {
		t.Fatalf("reap of a missing workspace succeeded:\n%s", output)
	}
}

func runReapWorktreeBuildDirs(t *testing.T, workspace string) {
	t.Helper()
	script := filepath.Join("..", "..", "scripts", "run-body", "reap-worktree-build-dirs")
	output, err := exec.Command(script, workspace).CombinedOutput()
	if err != nil {
		t.Fatalf("reap-worktree-build-dirs failed: %v\n%s", err, output)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
