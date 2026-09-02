package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// dispatch-stage is the one launcher for a lifecycle stage: it owns the
// stage's file naming, the wrapper nesting, the Ensemble record and status
// directories, and the diagnostics log, so the lead never composes them.
// These tests drive the real script with a stand-in adjudication wrapper and
// read the run directory the way the lead and the status projection do.

type dispatchStageFixture struct {
	runDir   string
	tools    string
	wrapper  string
	observed string
}

// newDispatchStageFixture copies the run-body wrappers beside dispatch-stage
// (the script locates its siblings through MINOS_SETUP_WORKSPACE) and installs
// a stand-in adjudication wrapper that records its arguments and environment,
// prints the body it is told to, and exits as instructed.
func newDispatchStageFixture(t *testing.T, wrapperBody string) dispatchStageFixture {
	t.Helper()
	root := t.TempDir()
	tools := filepath.Join(root, "run-body")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dispatch-stage", "time-on-exit", "flag-on-exit", "publish-on-exit"} {
		content, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-body", name))
		if err != nil {
			t.Fatal(err)
		}
		writeScript(t, filepath.Join(tools, name), string(content))
	}
	workflows := filepath.Join(root, "workflows")
	if err := os.MkdirAll(workflows, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"review.js", "review-briefs.js"} {
		if err := os.WriteFile(filepath.Join(workflows, name), []byte("return {};\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	observed := filepath.Join(root, "observed")
	wrapper := filepath.Join(workflows, "adjudicated-review")
	writeScript(t, wrapper, "#!/usr/bin/env sh\n"+
		"printf 'args=%s\\nrecord=%s\\nstatus=%s\\n' \"$*\" \"$ENSEMBLE_RUN_RECORD_DIR\" \"$ENSEMBLE_STATUS_DIR\" >\"$OBSERVED\"\n"+
		"echo 'wrapper diagnostics' >&2\n"+
		wrapperBody)
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "review-args.json"), []byte(`{"target":"t","head":"h"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dispatchStageFixture{runDir: runDir, tools: tools, wrapper: wrapper, observed: observed}
}

func (fixture dispatchStageFixture) run(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join(fixture.tools, "dispatch-stage"), args...)
	cmd.Env = append(os.Environ(),
		"MINOS_RUN_DIR="+fixture.runDir,
		"MINOS_REVIEW_WORKFLOW="+fixture.wrapper,
		"MINOS_SETUP_WORKSPACE="+filepath.Join(fixture.tools, "setup-workspace"),
		"OBSERVED="+fixture.observed,
	)
	return cmd.CombinedOutput()
}

func TestDispatchStageLaunchesTheWrappedWorkflowUnderTheLifecycleConvention(t *testing.T) {
	fixture := newDispatchStageFixture(t, "printf '{\"status\":\"complete\"}\\n'\n")

	output, err := fixture.run(t, "review", "review.js")
	if err != nil {
		t.Fatalf("dispatch-stage: %v\n%s", err, output)
	}
	if len(output) != 0 {
		t.Fatalf("dispatch-stage wrote to the lead's terminal: %q", output)
	}
	assertContainsFile(t, filepath.Join(fixture.runDir, "review-result.json"), `{"status":"complete"}`)
	assertContainsFile(t, filepath.Join(fixture.runDir, "review-result.done"), "0")
	assertContainsFile(t, filepath.Join(fixture.runDir, "review.log"), "wrapper diagnostics")
	assertContainsFile(t, filepath.Join(fixture.runDir, "timings.ndjson"), `"name":"review"`)
	assertContainsFile(t, fixture.observed, "args="+filepath.Join(fixture.tools, "..", "workflows", "review.js")+" --json-args @"+filepath.Join(fixture.runDir, "review-args.json"))
	assertContainsFile(t, fixture.observed, "record="+filepath.Join(fixture.runDir, "ensemble-records", "review"))
	assertContainsFile(t, fixture.observed, "status="+fixture.runDir)
}

func TestDispatchStageAnnouncesARepeatAttemptFromTheBaseInput(t *testing.T) {
	fixture := newDispatchStageFixture(t, "printf '{\"status\":\"complete\"}\\n'\n")

	if output, err := fixture.run(t, "review@2", "review.js"); err != nil {
		t.Fatalf("dispatch-stage: %v\n%s", err, output)
	}
	assertContainsFile(t, filepath.Join(fixture.runDir, "review@2-args.json"), `"target":"t"`)
	assertContainsFile(t, filepath.Join(fixture.runDir, "review@2-result.json"), `{"status":"complete"}`)
	assertContainsFile(t, filepath.Join(fixture.runDir, "timings.ndjson"), `"name":"review@2"`)
	assertContainsFile(t, fixture.observed, "record="+filepath.Join(fixture.runDir, "ensemble-records", "review@2"))
	if _, err := os.Stat(filepath.Join(fixture.runDir, "review-result.json")); !os.IsNotExist(err) {
		t.Fatalf("a repeat attempt wrote the first attempt's result: %v", err)
	}
}

func TestDispatchStageLeavesNoResultAndAFailureFlagWhenTheWorkflowFails(t *testing.T) {
	fixture := newDispatchStageFixture(t, "printf '{\"partial\":'\nexit 3\n")

	output, err := fixture.run(t, "review", "review.js")
	if err == nil {
		t.Fatalf("dispatch-stage reported success for a failing workflow\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDir, "review-result.json")); !os.IsNotExist(err) {
		t.Fatalf("result file exists after a failed workflow: %v", err)
	}
	assertContainsFile(t, filepath.Join(fixture.runDir, "review-result.json.partial"), `{"partial":`)
	assertContainsFile(t, filepath.Join(fixture.runDir, "review-result.done"), "3")
	assertContainsFile(t, filepath.Join(fixture.runDir, "timings.ndjson"), `"exit_status":3`)
}

func TestDispatchStageRefusesImprovisedNamesAndMissingInput(t *testing.T) {
	fixture := newDispatchStageFixture(t, "printf '{}\\n'\n")

	for _, name := range []string{"Review", "review@1", "review@01", "review-", "review@2@3", "review_2"} {
		output, err := fixture.run(t, name, "review.js")
		if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 2 {
			t.Fatalf("name %q: exit = %v, want usage exit 2\n%s", name, err, output)
		}
	}
	output, err := fixture.run(t, "review-brief", "review-briefs.js")
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 || !strings.Contains(string(output), "stage input") {
		t.Fatalf("missing input: exit = %v, want 1 naming the stage input\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDir, "review-brief-result.done")); !os.IsNotExist(err) {
		t.Fatalf("a refused launch left a completion flag: %v", err)
	}
}

func TestDispatchStageAwaitReturnsOnceTheCompletionFlagAppears(t *testing.T) {
	fixture := newDispatchStageFixture(t, "printf '{}\\n'\n")
	flag := filepath.Join(fixture.runDir, "review-result.done")

	done := make(chan error, 1)
	go func() {
		_, err := fixture.run(t, "await", "review")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("await returned before the flag existed: %v", err)
	case <-time.After(1500 * time.Millisecond):
	}
	if err := os.WriteFile(flag, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("await: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("await did not return after the flag appeared")
	}
}
