package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFinishRunBodyGatesTheMergeThroughRunWrap drives the finish kind end to end
// through the real wrapper with a deterministic stand-in. It proves firing is
// permissive but merging is gated: a passing gate merges and consumes the Ready
// label, and each gate failure records a refused outcome as machine state
// rather than a silent stop. The merge's real quality is exercised only against
// a live forge at activation.
func TestFinishRunBodyGatesTheMergeThroughRunWrap(t *testing.T) {
	env := setupRunBodyHarness(t, "finish", "finish-engine-standin", "finish.md")

	// Merged: build and test pass, the forge reports mergeable, the head is
	// current. The merge runs and consumes the finish label.
	env.setRunEnv(t, "aaaaaaaaaaaaaaaa", nil)
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	merged := RunDir(env.runsDir, "local", "pump19", "subject", "7", "aaaaaaaaaaaaaaaa", RunFinish)
	assertContainsFile(t, filepath.Join(merged, "finish-summary.md"), "outcome=merged")
	assertContainsFile(t, filepath.Join(env.stateDir, "merge.args"), "merge")
	assertContainsFile(t, filepath.Join(env.stateDir, "labels-removed"), "Ready")
	assertContainsFile(t, filepath.Join(env.stateDir, "status.args"), "pump19/finish\nsuccess")

	// Refused, not mergeable: the forge reports the branch not mergeable. The
	// merge is never attempted, the finish label stays sticky, and the refusal
	// is recorded — never a silent exit.
	_ = os.Remove(filepath.Join(env.stateDir, "merge.args"))
	_ = os.Remove(filepath.Join(env.stateDir, "labels-removed"))
	env.setRunEnv(t, "bbbbbbbbbbbbbbbb", map[string]string{"PUMP19_FIXTURE_MERGEABLE": "false"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	notMergeable := RunDir(env.runsDir, "local", "pump19", "subject", "7", "bbbbbbbbbbbbbbbb", RunFinish)
	assertContainsFile(t, filepath.Join(notMergeable, "finish-summary.md"), "outcome=refused")
	assertContainsFile(t, filepath.Join(notMergeable, "finish-summary.md"), "reason=not-mergeable")
	if _, err := os.Stat(filepath.Join(env.stateDir, "merge.args")); !os.IsNotExist(err) {
		t.Fatal("a non-mergeable branch was merged")
	}
	// The run releases its own Finishing label, but must leave Ready sticky.
	if removed, _ := os.ReadFile(filepath.Join(env.stateDir, "labels-removed")); strings.Contains(string(removed), "Ready") {
		t.Fatal("a refused finish consumed the Ready label")
	}
	assertContainsFile(t, filepath.Join(env.stateDir, "status.args"), "pump19/finish\nfailure")

	// Refused, build failed: the workspace build gate stops the run before any
	// merge consideration.
	_ = os.Remove(filepath.Join(env.stateDir, "merge.args"))
	env.setRunEnv(t, "cccccccccccccccc", map[string]string{"PUMP19_BUILD_CMD": "false"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	buildFailed := RunDir(env.runsDir, "local", "pump19", "subject", "7", "cccccccccccccccc", RunFinish)
	assertContainsFile(t, filepath.Join(buildFailed, "finish-summary.md"), "reason=build-failed")
	if _, err := os.Stat(filepath.Join(env.stateDir, "merge.args")); !os.IsNotExist(err) {
		t.Fatal("a failing build reached the merge")
	}
	assertContainsFile(t, filepath.Join(env.stateDir, "status.args"), "pump19/finish\nfailure")
}
