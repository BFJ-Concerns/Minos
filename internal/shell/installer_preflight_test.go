package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The installer checks the declared tool set before installing. A missing
// required tool, or no engine CLI at all, stops it with every gap reported
// and the previous installation preserved, as does a Node the review runtime
// refuses to start on; a version outside the recommended range warns without
// preventing installation.

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

func preflightSourceRoot(t *testing.T, declarations ...string) string {
	t.Helper()
	sourceRoot := installerSourceRoot(t)
	if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", "review.js"), []byte("export const meta = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	declareExpectedTools(t, sourceRoot, declarations...)
	return sourceRoot
}

func assertNothingInstalled(t *testing.T, destination string) {
	t.Helper()
	// The runtime install and workflow swap both sit behind the preflight.
	for _, name := range []string{"runtime", "workflows"} {
		if _, err := os.Stat(filepath.Join(destination, name)); !os.IsNotExist(err) {
			t.Fatalf("%s installed despite a failed preflight: %v", name, err)
		}
	}
}

func TestInstallReviewRuntimePreflightStopsOnMissingRequiredTools(t *testing.T) {
	sourceRoot := preflightSourceRoot(t,
		"vanished\tno-such-tool-minos-preflight --version\t>=1.0.0\trequired",
		"absent\tno-such-other-tool-minos-preflight --version\tany\trequired",
	)
	destination := filepath.Join(t.TempDir(), "opt", "minos")
	output, err := runInstallerWithPath(t, sourceRoot, destination, t.TempDir())
	if err == nil {
		t.Fatalf("installer succeeded without its required tools\n%s", output)
	}
	// The report names every missing tool, not just the first one met.
	for _, fragment := range []string{"vanished: missing", "absent: missing", "2 blocking problem(s)"} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("preflight report missing %q:\n%s", fragment, output)
		}
	}
	assertNothingInstalled(t, destination)
}

func TestInstallReviewRuntimePreflightWarnsBelowRangeAndInstalls(t *testing.T) {
	sourceRoot := preflightSourceRoot(t,
		"jq\tjq --version\t>=1.8.2\trequired",
		"node\tnode --version\t>=24.14.0\trequired",
		"git\tgit --version\tany\trequired",
	)
	binDir := t.TempDir()
	stubTool(t, binDir, "jq", "jq-1.7")
	stubTool(t, binDir, "node", "v24.14.0")
	stubTool(t, binDir, "git", "git version 9.9.9")

	destination := filepath.Join(t.TempDir(), "opt", "minos")
	output, err := runInstallerWithPath(t, sourceRoot, destination, binDir)
	if err != nil {
		t.Fatalf("installer stopped on a version drift: %v\n%s", err, output)
	}
	// The deploy log names older versions while accepting the range boundary.
	for _, fragment := range []string{
		"jq: jq-1.7 — warning: recommended >=1.8.2",
		"node: v24.14.0 (meets >=24.14.0)",
		"git: git version 9.9.9",
	} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("preflight report missing %q:\n%s", fragment, output)
		}
	}
	assertRegularFile(t, filepath.Join(destination, "workflows", "review.js"))
}

func TestInstallReviewRuntimePreflightAcceptsASingleEngine(t *testing.T) {
	sourceRoot := preflightSourceRoot(t,
		"claude\tclaude --version\t>=2.1.300\tengine",
		"codex\tno-such-codex-minos-preflight --version\t>=0.162.1\tengine",
	)
	binDir := t.TempDir()
	stubTool(t, binDir, "claude", "2.1.300 (Claude Code)")

	destination := filepath.Join(t.TempDir(), "opt", "minos")
	output, err := runInstallerWithPath(t, sourceRoot, destination, binDir)
	if err != nil {
		t.Fatalf("installer stopped with one engine present: %v\n%s", err, output)
	}
	if !strings.Contains(output, "codex: not installed") {
		t.Fatalf("preflight did not report the absent engine:\n%s", output)
	}
	assertRegularFile(t, filepath.Join(destination, "workflows", "review.js"))
}

func TestInstallReviewRuntimePreflightStopsWithNoEngine(t *testing.T) {
	sourceRoot := preflightSourceRoot(t,
		"claude\tno-such-claude-minos-preflight --version\t>=2.1.300\tengine",
		"codex\tno-such-codex-minos-preflight --version\t>=0.162.1\tengine",
	)
	destination := filepath.Join(t.TempDir(), "opt", "minos")
	output, err := runInstallerWithPath(t, sourceRoot, destination, t.TempDir())
	if err == nil {
		t.Fatalf("installer succeeded with no engine CLI\n%s", output)
	}
	if !strings.Contains(output, "install at least one of: claude, codex") {
		t.Fatalf("preflight did not name the engines to install:\n%s", output)
	}
	assertNothingInstalled(t, destination)
}

func TestInstallReviewRuntimePreflightStopsWhenTheRuntimeRefusesNode(t *testing.T) {
	sourceRoot := preflightSourceRoot(t,
		"node\tnode --version\t>=24.14.0\trequired",
		"review-runtime\tnode runtime/ensemble.mjs --version\tany\trequired",
	)
	binDir := t.TempDir()
	// A Node that runs but is older than the review runtime's floor: the
	// runtime refuses to start on it, naming both versions.
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) printf 'v16.20.2\\n' ;;\n  *) printf 'Ensemble requires Node >=24.14.0; detected Node 16.20.2\\n' >&2; exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "node"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "opt", "minos")
	output, err := runInstallerWithPath(t, sourceRoot, destination, binDir)
	if err == nil {
		t.Fatalf("installer succeeded on a Node the runtime refuses\n%s", output)
	}
	for _, fragment := range []string{"review-runtime: missing — Ensemble requires Node >=24.14.0; detected Node 16.20.2", "1 blocking problem(s)"} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("preflight report missing %q:\n%s", fragment, output)
		}
	}
	assertNothingInstalled(t, destination)
}

func TestInstallReviewRuntimePreflightRejectsAnUnknownNeed(t *testing.T) {
	sourceRoot := preflightSourceRoot(t, "jq\tjq --version\tany\toptional")
	destination := filepath.Join(t.TempDir(), "opt", "minos")
	output, err := runInstallerWithPath(t, sourceRoot, destination, t.TempDir())
	if err == nil || !strings.Contains(output, "declares need 'optional'") {
		t.Fatalf("installer accepted an unknown need: %v\n%s", err, output)
	}
	assertNothingInstalled(t, destination)
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

func TestInstallReviewRuntimePreflightAcceptsNewerVersions(t *testing.T) {
	for _, version := range []string{"2.1.300", "2.1.301", "2.2.0", "3.0.0", "10.0.0"} {
		t.Run(version, func(t *testing.T) {
			sourceRoot := preflightSourceRoot(t, "claude\tclaude --version\t>=2.1.300\tengine")
			binDir := t.TempDir()
			stubTool(t, binDir, "claude", version+" (Claude Code)")
			destination := filepath.Join(t.TempDir(), "opt", "minos")
			output, err := runInstallerWithPath(t, sourceRoot, destination, binDir)
			if err != nil || strings.Contains(output, "warning:") || !strings.Contains(output, "meets >=2.1.300") {
				t.Fatalf("installer did not accept newer stable version: %v\n%s", err, output)
			}
			assertRegularFile(t, filepath.Join(destination, "workflows", "review.js"))
		})
	}
}

func TestInstallReviewRuntimePreflightWarnsOnOlderOrUnknownVersions(t *testing.T) {
	for _, version := range []string{"codex-cli 0.162.0", "codex-cli 0.161.1000", "codex-cli 0.162.1-beta.1", "codex-cli unknown"} {
		t.Run(version, func(t *testing.T) {
			sourceRoot := preflightSourceRoot(t, "codex\tcodex --version\t>=0.162.1\tengine")
			binDir := t.TempDir()
			stubTool(t, binDir, "codex", version)
			destination := filepath.Join(t.TempDir(), "opt", "minos")
			output, err := runInstallerWithPath(t, sourceRoot, destination, binDir)
			if err != nil || !strings.Contains(output, "warning: recommended >=0.162.1") {
				t.Fatalf("installer did not warn without blocking: %v\n%s", err, output)
			}
			assertRegularFile(t, filepath.Join(destination, "workflows", "review.js"))
		})
	}
}

func TestInstallReviewRuntimePreflightRejectsInvalidRanges(t *testing.T) {
	for _, versionRange := range []string{"2.1.300", "claude 2.1.300", ">=", ">=2..300", ">=2.1.x", ">=2.1.300.1"} {
		t.Run(versionRange, func(t *testing.T) {
			sourceRoot := preflightSourceRoot(t, "claude\tclaude --version\t"+versionRange+"\tengine")
			destination := filepath.Join(t.TempDir(), "opt", "minos")
			output, err := runInstallerWithPath(t, sourceRoot, destination, t.TempDir())
			if err == nil || !strings.Contains(output, "preflight: claude:") {
				t.Fatalf("installer accepted invalid range: %v\n%s", err, output)
			}
			assertNothingInstalled(t, destination)
		})
	}
}
