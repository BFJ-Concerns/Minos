package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The installer's preflight checks the box's tool versions against the
// declared set before anything lands: the `?.` jq defect shipped green
// because the gate ran jq 1.8.2 while the box ran 1.7. A drifted version
// now stops the install loudly, naming each drifted tool with both
// versions, with nothing installed past the stop. The declared set is
// data the installer reads, so the check generalises past any one tool.

// stubTool shadows a real tool on PATH with a script that reports the
// given version line, so the preflight is exercised against controlled
// versions rather than whatever the test machine carries.
func stubTool(t *testing.T, binDir, name, versionLine string) {
	t.Helper()
	script := "#!/bin/sh\nprintf '%s\\n' \"" + versionLine + "\"\n"
	if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runInstallerWithPath(t *testing.T, sourceRoot, destination, binDir string) (string, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join(sourceRoot, "scripts", "install-review-runtime"), destination)
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestInstallReviewRuntimePreflightStopsOnVersionMismatch(t *testing.T) {
	sourceRoot := installerSourceRoot(t)
	if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", "review.js"), []byte("export const meta = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	declareExpectedTools(t, sourceRoot,
		"jq\tjq --version\tjq-1.8.2",
		"vanished\tno-such-tool-minos-preflight --version\t1.0.0",
	)
	binDir := t.TempDir()
	stubTool(t, binDir, "jq", "jq-1.7")

	destination := filepath.Join(t.TempDir(), "opt", "minos")
	output, err := runInstallerWithPath(t, sourceRoot, destination, binDir)
	if err == nil {
		t.Fatalf("installer succeeded against a drifted tool set\n%s", output)
	}
	// The report names every drifted tool with both versions, not just the
	// first one met.
	for _, fragment := range []string{"jq-1.8.2", "jq-1.7", "vanished", "not runnable"} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("preflight report missing %q:\n%s", fragment, output)
		}
	}
	// Nothing may land past a mismatch — the runtime install and workflow
	// swap both sit behind the preflight.
	for _, name := range []string{"runtime", "workflows"} {
		if _, err := os.Stat(filepath.Join(destination, name)); !os.IsNotExist(err) {
			t.Fatalf("%s installed despite a preflight mismatch: %v", name, err)
		}
	}
}

func TestInstallReviewRuntimePreflightReportsParityAndAcceptedDivergence(t *testing.T) {
	sourceRoot := installerSourceRoot(t)
	if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", "review.js"), []byte("export const meta = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	declareExpectedTools(t, sourceRoot,
		"jq\tjq --version\tjq-1.8.2",
		"git\tgit --version\tany",
	)
	binDir := t.TempDir()
	stubTool(t, binDir, "jq", "jq-1.8.2")
	stubTool(t, binDir, "git", "git version 9.9.9")

	destination := filepath.Join(t.TempDir(), "opt", "minos")
	output, err := runInstallerWithPath(t, sourceRoot, destination, binDir)
	if err != nil {
		t.Fatalf("install review runtime: %v\n%s", err, output)
	}
	if !strings.Contains(output, "jq: parity (jq-1.8.2)") {
		t.Fatalf("preflight did not report parity:\n%s", output)
	}
	// An accepted divergence is reported with the observed version rather
	// than silently skipped, so the deploy log still says what the box ran.
	if !strings.Contains(output, "git: accepted divergence (git version 9.9.9)") {
		t.Fatalf("preflight did not report the accepted divergence:\n%s", output)
	}
	assertRegularFile(t, filepath.Join(destination, "workflows", "review.js"))
}

func TestInstallReviewRuntimePreflightRequiresTheDeclaredToolSet(t *testing.T) {
	sourceRoot := installerSourceRoot(t)
	if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", "review.js"), []byte("export const meta = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(sourceRoot, "scripts", "expected-tool-versions")); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "opt", "minos")
	cmd := exec.Command(filepath.Join(sourceRoot, "scripts", "install-review-runtime"), destination)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("installer succeeded without a declared tool set\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(destination, "runtime")); !os.IsNotExist(err) {
		t.Fatalf("runtime installed despite the missing declared set: %v", err)
	}
}
