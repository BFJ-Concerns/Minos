package shell

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// flag-on-exit is the completion signal the lead's background watcher arms
// on: the flag file appears, complete, only after the wrapped command has
// exited. Watching the command's own result file is unreliable — shell
// redirection creates it empty at launch — so the wrapper is the one place
// creation-after-exit is guaranteed.

func runFlagOnExit(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "flag-on-exit"), args...)
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}

func TestCompletionFlagAppearsOnlyAfterTheCommandExits(t *testing.T) {
	dir := t.TempDir()
	flag := filepath.Join(dir, "review.done")
	seen := filepath.Join(dir, "flag-state-during-command")

	// The wrapped command records whether the flag already exists while it
	// is still running; the wrapper must not have created it yet.
	output, err := runFlagOnExit(t, flag, "sh", "-c",
		`if [ -e "$0" ]; then echo present >"$1"; else echo absent >"$1"; fi`, flag, seen)
	if err != nil {
		t.Fatalf("flag-on-exit: %v\n%s", err, output)
	}
	during, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(during)); got != "absent" {
		t.Fatalf("flag state during command = %q, want absent", got)
	}
	status, err := os.ReadFile(flag)
	if err != nil {
		t.Fatalf("flag missing after exit: %v", err)
	}
	if got := strings.TrimSpace(string(status)); got != "0" {
		t.Fatalf("flag content = %q, want recorded exit status 0", got)
	}
	if _, err := os.Stat(flag + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left beside the flag: %v", err)
	}
}

func TestCompletionFlagRecordsFailureAndPreservesExitStatus(t *testing.T) {
	dir := t.TempDir()
	flag := filepath.Join(dir, "review.done")

	output, err := runFlagOnExit(t, flag, "sh", "-c", "exit 7")
	var exitError *exec.ExitError
	if err == nil {
		t.Fatalf("wrapper exited 0 for a failing command\n%s", output)
	} else if ok := errors.As(err, &exitError); !ok || exitError.ExitCode() != 7 {
		t.Fatalf("wrapper exit = %v, want the command's status 7\n%s", err, output)
	}
	status, err := os.ReadFile(flag)
	if err != nil {
		t.Fatalf("flag missing after failed command: %v", err)
	}
	if got := strings.TrimSpace(string(status)); got != "7" {
		t.Fatalf("flag content = %q, want recorded exit status 7", got)
	}
}

func TestCompletionFlagClearsAStaleFlagBeforeRunning(t *testing.T) {
	dir := t.TempDir()
	flag := filepath.Join(dir, "review.done")
	if err := os.WriteFile(flag, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The command observes the pre-launch state: a stale flag from an
	// earlier invocation must be gone before the new command starts, or a
	// watcher armed on existence would fire on old news.
	seen := filepath.Join(dir, "flag-state-during-command")
	output, err := runFlagOnExit(t, flag, "sh", "-c",
		`if [ -e "$0" ]; then echo present >"$1"; else echo absent >"$1"; fi`, flag, seen)
	if err != nil {
		t.Fatalf("flag-on-exit: %v\n%s", err, output)
	}
	during, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(during)); got != "absent" {
		t.Fatalf("stale flag survived into the new command: state = %q", got)
	}
}

func TestCompletionFlagRefusesMissingArguments(t *testing.T) {
	output, err := runFlagOnExit(t, "/tmp/flag-only")
	var exitError *exec.ExitError
	if err == nil || !errors.As(err, &exitError) || exitError.ExitCode() != 2 {
		t.Fatalf("usage exit = %v, want 2\n%s", err, output)
	}
	if !strings.Contains(string(output), "usage: flag-on-exit") {
		t.Fatalf("usage output = %s", output)
	}
}
