package shell

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// publish-on-exit is the atomic publication seam for lead-facing workflow
// result files: the result path holds either a whole, successfully produced
// result or nothing. Shell redirection creates the result empty at launch,
// so a supervisor reading it mid-run — or after a kill — sees a plausible
// empty verdict; the wrapper makes absence mean "no verdict yet".

func runPublishOnExit(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "publish-on-exit"), args...)
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}

func TestResultPublicationAppearsWholeAndOnlyAfterSuccess(t *testing.T) {
	dir := t.TempDir()
	result := filepath.Join(dir, "review-result.json")
	seen := filepath.Join(dir, "result-state-during-command")

	// The command records whether the result path already exists while it
	// is still running and producing output.
	output, err := runPublishOnExit(t, result, "sh", "-c",
		`if [ -e "$0" ]; then echo present >"$1"; else echo absent >"$1"; fi; printf '{"status":"complete"}\n'`,
		result, seen)
	if err != nil {
		t.Fatalf("publish-on-exit: %v\n%s", err, output)
	}
	during, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(during)); got != "absent" {
		t.Fatalf("result state during command = %q, want absent", got)
	}
	published, err := os.ReadFile(result)
	if err != nil {
		t.Fatalf("result missing after successful exit: %v", err)
	}
	if got := strings.TrimSpace(string(published)); got != `{"status":"complete"}` {
		t.Fatalf("published result = %q", got)
	}
	if _, err := os.Stat(result + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("partial file left beside a published result: %v", err)
	}
}

func TestResultPublicationWithholdsTheResultOnFailure(t *testing.T) {
	dir := t.TempDir()
	result := filepath.Join(dir, "review-result.json")

	output, err := runPublishOnExit(t, result, "sh", "-c",
		`printf '{"status":'; exit 7`)
	var exitError *exec.ExitError
	if err == nil {
		t.Fatalf("wrapper exited 0 for a failing command\n%s", output)
	} else if ok := errors.As(err, &exitError); !ok || exitError.ExitCode() != 7 {
		t.Fatalf("wrapper exit = %v, want the command's status 7\n%s", err, output)
	}
	if _, err := os.Stat(result); !os.IsNotExist(err) {
		t.Fatalf("failed command published a result file: %v", err)
	}
	partial, err := os.ReadFile(result + ".partial")
	if err != nil {
		t.Fatalf("partial output not retained for diagnosis: %v", err)
	}
	if got := string(partial); got != `{"status":` {
		t.Fatalf("retained partial = %q", got)
	}
}

func TestResultPublicationWithholdsTheResultWhenTheCommandIsKilled(t *testing.T) {
	dir := t.TempDir()
	result := filepath.Join(dir, "review-result.json")

	output, err := runPublishOnExit(t, result, "sh", "-c",
		`printf '{"stat'; kill -KILL $$`)
	if err == nil {
		t.Fatalf("wrapper exited 0 for a signal-killed command\n%s", output)
	}
	if _, err := os.Stat(result); !os.IsNotExist(err) {
		t.Fatalf("signal-killed command published a result file: %v", err)
	}
}

func TestResultPublicationLeavesNoResultWhenKilledMidCommand(t *testing.T) {
	dir := t.TempDir()
	result := filepath.Join(dir, "review-result.json")

	// The orphaned child closes its inherited pipe before lingering, so the
	// test observes the wrapper's death rather than waiting out the child.
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "publish-on-exit"),
		result, "sh", "-c", `printf '{"stat'; kill -KILL $PPID; exec >/dev/null 2>&1; sleep 10`)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("wrapper survived being killed\n%s", output)
	}
	if _, err := os.Stat(result); !os.IsNotExist(err) {
		t.Fatalf("killed wrapper left an observable result file: %v", err)
	}
}

func TestResultPublicationClearsStaleResultAndPartialBeforeRunning(t *testing.T) {
	dir := t.TempDir()
	result := filepath.Join(dir, "review-result.json")
	if err := os.WriteFile(result, []byte(`{"stale":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result+".partial", []byte(`{"sta`), 0o644); err != nil {
		t.Fatal(err)
	}

	// A stale result from an earlier invocation must be gone before the new
	// command starts, or a supervisor would read old news as this run's
	// verdict; a failing command must not resurrect it, and the stale
	// partial must not pollute the new command's collected output.
	output, err := runPublishOnExit(t, result, "sh", "-c", "exit 1")
	var exitError *exec.ExitError
	if err == nil || !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
		t.Fatalf("wrapper exit = %v, want 1\n%s", err, output)
	}
	if _, err := os.Stat(result); !os.IsNotExist(err) {
		t.Fatalf("stale result survived a failing re-run: %v", err)
	}
	partial, err := os.ReadFile(result + ".partial")
	if err != nil {
		t.Fatalf("partial file missing after the failing re-run: %v", err)
	}
	if got := string(partial); got != "" {
		t.Fatalf("stale partial content survived into the new run: %q", got)
	}
}

func TestResultPublicationRefusesMissingArguments(t *testing.T) {
	output, err := runPublishOnExit(t, "/tmp/result-only")
	var exitError *exec.ExitError
	if err == nil || !errors.As(err, &exitError) || exitError.ExitCode() != 2 {
		t.Fatalf("usage exit = %v, want 2\n%s", err, output)
	}
	if !strings.Contains(string(output), "usage: publish-on-exit") {
		t.Fatalf("usage output = %s", output)
	}
}
