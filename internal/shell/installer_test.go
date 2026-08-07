package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// install-review-runtime must leave no orphan on the deployment target: a
// workflow deleted from the source tree is gone from the destination after
// the next install. Twice a torn-out workflow survived a deploy and needed
// a manual sweep of /opt/minos/workflows — the installer only ever added.

func installerSourceRoot(t *testing.T) string {
	t.Helper()
	sourceRoot := t.TempDir()
	for _, directory := range []string{"scripts", "runtime", "workflows"} {
		if err := os.MkdirAll(filepath.Join(sourceRoot, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyFixtureFile(t, filepath.Join("..", "..", "scripts", "install-review-runtime"), filepath.Join(sourceRoot, "scripts", "install-review-runtime"), 0o755)
	for _, name := range []string{"ensemble.mjs", "ensemble.mjs.sha256", "ensemble.source-version"} {
		copyFixtureFile(t, filepath.Join("..", "..", "runtime", name), filepath.Join(sourceRoot, "runtime", name), 0o644)
	}
	return sourceRoot
}

func runInstaller(t *testing.T, sourceRoot, destination string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(sourceRoot, "scripts", "install-review-runtime"), destination)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install review runtime: %v\n%s", err, output)
	}
}

func TestInstallReviewRuntimeMirrorsSourceDeletions(t *testing.T) {
	sourceRoot := installerSourceRoot(t)
	survivor := filepath.Join(sourceRoot, "workflows", "review.js")
	condemned := filepath.Join(sourceRoot, "workflows", "torn-out.mjs")
	nested := filepath.Join(sourceRoot, "workflows", "review-briefs", "retired-brief.md")
	if err := os.WriteFile(survivor, []byte("export const meta = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(condemned, []byte("export const meta = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("# Retired\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "opt", "minos")
	runInstaller(t, sourceRoot, destination)
	assertRegularFile(t, filepath.Join(destination, "workflows", "torn-out.mjs"))
	assertRegularFile(t, filepath.Join(destination, "workflows", "review-briefs", "retired-brief.md"))

	// Tear both out of the source tree; the next install must sweep the
	// deployed copies, the nested one's now-empty directory included.
	if err := os.Remove(condemned); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(nested); err != nil {
		t.Fatal(err)
	}
	runInstaller(t, sourceRoot, destination)

	assertRegularFile(t, filepath.Join(destination, "workflows", "review.js"))
	if _, err := os.Stat(filepath.Join(destination, "workflows", "torn-out.mjs")); !os.IsNotExist(err) {
		t.Fatalf("deleted workflow survived the re-install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "workflows", "review-briefs")); !os.IsNotExist(err) {
		t.Fatalf("emptied workflow directory survived the re-install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "workflows.next")); !os.IsNotExist(err) {
		t.Fatalf("staging directory left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "workflows.next.listing")); !os.IsNotExist(err) {
		t.Fatalf("source listing left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "workflows.previous")); !os.IsNotExist(err) {
		t.Fatalf("replaced tree left behind: %v", err)
	}
}

func TestInstallReviewRuntimeStopsWhenTheSourceTreeIsUnreadable(t *testing.T) {
	sourceRoot := installerSourceRoot(t)
	if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", "review.js"), []byte("export const meta = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "opt", "minos")
	runInstaller(t, sourceRoot, destination)
	assertRegularFile(t, filepath.Join(destination, "workflows", "review.js"))

	// A source tree the installer cannot traverse must stop the install
	// with the previous deployment intact — a masked find failure would
	// swap an incomplete staging tree over the good one.
	if err := os.Rename(filepath.Join(sourceRoot, "workflows"), filepath.Join(sourceRoot, "workflows.hidden")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(sourceRoot, "scripts", "install-review-runtime"), destination)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("installer succeeded against a missing source tree\n%s", output)
	}
	assertRegularFile(t, filepath.Join(destination, "workflows", "review.js"))
}

func TestInstallReviewRuntimeSweepsStaleStagingBeforeInstalling(t *testing.T) {
	sourceRoot := installerSourceRoot(t)
	if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", "review.js"), []byte("export const meta = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A stale staging tree from an interrupted earlier install must not
	// leak its contents into the swapped-in result.
	destination := filepath.Join(t.TempDir(), "opt", "minos")
	staleStaging := filepath.Join(destination, "workflows.next")
	if err := os.MkdirAll(staleStaging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleStaging, "stale.mjs"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runInstaller(t, sourceRoot, destination)

	assertRegularFile(t, filepath.Join(destination, "workflows", "review.js"))
	if _, err := os.Stat(filepath.Join(destination, "workflows", "stale.mjs")); !os.IsNotExist(err) {
		t.Fatalf("stale staged file leaked into the installed tree: %v", err)
	}
}
