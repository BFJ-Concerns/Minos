package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func assertPostedSummaryOmitsModelIdentity(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Model:") || strings.Contains(string(data), "claude-opus") || strings.Contains(string(data), "provenance") {
		t.Fatalf("posted summary exposed model provenance:\n%s", data)
	}
}

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
	merged := RunDir(env.runsDir, "local", "minos", "subject", "7", "aaaaaaaaaaaaaaaa", RunFinish)
	assertContainsFile(t, filepath.Join(merged, "finish-summary.md"), "outcome=merged")
	assertContainsFile(t, filepath.Join(env.stateDir, "merge.args"), "merge")
	assertContainsFile(t, filepath.Join(env.stateDir, "labels-removed"), "Ready")
	assertContainsFile(t, filepath.Join(env.stateDir, "status.args"), "minos/finish\nsuccess")
	assertPostedSummaryOmitsModelIdentity(t, filepath.Join(merged, "finish-summary.md"))
	// Only a completed merge earns a PR comment; prove the channel works here so
	// the refusal subcases' no-comment assertions below are not vacuous.
	assertContainsFile(t, filepath.Join(env.stateDir, "comments"), "outcome=merged")

	// Required CI may carry the build-and-test gate. Unset commands are skipped
	// rather than replaced with a sentinel command that pretends to verify work.
	env.setRunEnv(t, "dddddddddddddddd", map[string]string{"MINOS_BUILD_CMD": "", "MINOS_TEST_CMD": ""})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	ciCarried := RunDir(env.runsDir, "local", "minos", "subject", "7", "dddddddddddddddd", RunFinish)
	assertContainsFile(t, filepath.Join(ciCarried, "finish-summary.md"), "outcome=merged")

	// Refused, not mergeable: the forge reports the branch not mergeable. The
	// merge is never attempted, the finish label stays sticky, and the refusal
	// is recorded — never a silent exit.
	_ = os.Remove(filepath.Join(env.stateDir, "merge.args"))
	_ = os.Remove(filepath.Join(env.stateDir, "labels-removed"))
	env.setRunEnv(t, "bbbbbbbbbbbbbbbb", map[string]string{"MINOS_FIXTURE_MERGEABLE": "false"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	notMergeable := RunDir(env.runsDir, "local", "minos", "subject", "7", "bbbbbbbbbbbbbbbb", RunFinish)
	assertContainsFile(t, filepath.Join(notMergeable, "finish-summary.md"), "outcome=refused")
	assertContainsFile(t, filepath.Join(notMergeable, "finish-summary.md"), "reason=not-mergeable")
	if _, err := os.Stat(filepath.Join(env.stateDir, "merge.args")); !os.IsNotExist(err) {
		t.Fatal("a non-mergeable branch was merged")
	}
	// The run releases its own Finishing label, but must leave Ready sticky.
	if removed, _ := os.ReadFile(filepath.Join(env.stateDir, "labels-removed")); strings.Contains(string(removed), "Ready") {
		t.Fatal("a refused finish consumed the Ready label")
	}
	assertContainsFile(t, filepath.Join(env.stateDir, "status.args"), "minos/finish\nfailure")
	// A refusal is service process: it must not post a PR comment (operator
	// ruling 2026-07-11). The run-dir summary above still carries the account.
	if posted, _ := os.ReadFile(filepath.Join(env.stateDir, "comments")); strings.Contains(string(posted), "outcome=refused") {
		t.Fatalf("a refused finish posted a PR comment:\n%s", posted)
	}

	// Refused, build failed: the workspace build gate stops the run before any
	// merge consideration.
	_ = os.Remove(filepath.Join(env.stateDir, "merge.args"))
	env.setRunEnv(t, "cccccccccccccccc", map[string]string{"MINOS_BUILD_CMD": "false"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	buildFailed := RunDir(env.runsDir, "local", "minos", "subject", "7", "cccccccccccccccc", RunFinish)
	assertContainsFile(t, filepath.Join(buildFailed, "finish-summary.md"), "reason=build-failed")
	if _, err := os.Stat(filepath.Join(env.stateDir, "merge.args")); !os.IsNotExist(err) {
		t.Fatal("a failing build reached the merge")
	}
	assertContainsFile(t, filepath.Join(env.stateDir, "status.args"), "minos/finish\nfailure")

	// A current head which is behind its base is synchronised as an ordinary
	// merge commit and sent back through review. It is not merged into the base
	// during the same finish run.
	_ = os.Remove(filepath.Join(env.stateDir, "merge.args"))
	env.setRunEnv(t, "eeeeeeeeeeeeeeee", map[string]string{"MINOS_FIXTURE_BEHIND_BASE": "1"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	behindBase := RunDir(env.runsDir, "local", "minos", "subject", "7", "eeeeeeeeeeeeeeee", RunFinish)
	assertContainsFile(t, filepath.Join(behindBase, "finish-summary.md"), "reason=review-pending")
	assertContainsFile(t, filepath.Join(env.stateDir, "commit-push.args"), "main")
	if _, err := os.Stat(filepath.Join(env.stateDir, "merge.args")); !os.IsNotExist(err) {
		t.Fatal("a behind-base head reached the forge merge")
	}
	parentData, err := os.ReadFile(filepath.Join(env.stateDir, "sync-parents"))
	if err != nil {
		t.Fatal(err)
	}
	parents := strings.Fields(string(parentData))
	if len(parents) != 2 {
		t.Fatalf("synchronisation commit has %d parents, want 2: %v", len(parents), parents)
	}

	// The sync runs before eligibility (operator ruling 2026-07-11): a
	// Ready-carrying head that is behind its base gets its sync landed even
	// when its verdict is not eligible to merge, so the branch never rots
	// staleness-blocked while it waits.
	_ = os.Remove(filepath.Join(env.stateDir, "merge.args"))
	_ = os.Remove(filepath.Join(env.stateDir, "sync-parents"))
	env.setRunEnv(t, "ffffffffffffffff", map[string]string{"MINOS_FIXTURE_BEHIND_BASE": "1", "MINOS_FIXTURE_VERDICT": "bar-dissent"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	behindIneligible := RunDir(env.runsDir, "local", "minos", "subject", "7", "ffffffffffffffff", RunFinish)
	assertContainsFile(t, filepath.Join(behindIneligible, "finish-summary.md"), "reason=review-pending")
	if _, err := os.Stat(filepath.Join(env.stateDir, "merge.args")); !os.IsNotExist(err) {
		t.Fatal("an ineligible behind-base head reached the forge merge")
	}
	if syncParents, err := os.ReadFile(filepath.Join(env.stateDir, "sync-parents")); err != nil {
		t.Fatal("an ineligible behind-base head was not synchronised:", err)
	} else if len(strings.Fields(string(syncParents))) != 2 {
		t.Fatalf("ineligible-head synchronisation commit parents: %q", syncParents)
	}

	// A historical bar-dissent verdict merges as clean (operator ruling
	// 2026-07-11): zero blocking findings at full coverage, the bar's dissent
	// on the record — findings gate merges, the bar critiques reviews.
	_ = os.Remove(filepath.Join(env.stateDir, "merge.args"))
	env.setRunEnv(t, "5555555555555555", map[string]string{"MINOS_FIXTURE_VERDICT": "bar-dissent"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	dissent := RunDir(env.runsDir, "local", "minos", "subject", "7", "5555555555555555", RunFinish)
	assertContainsFile(t, filepath.Join(dissent, "finish-summary.md"), "outcome=merged")
	assertContainsFile(t, filepath.Join(env.stateDir, "merge.args"), "merge")

	// Eligibility: merge only on the SERVICE'S OWN verdict for the current head,
	// read from its posted review marker. Each of these refuses not-eligible,
	// never reaching merge, and leaves Ready sticky. The last case is the R1
	// regression: an unrelated human APPROVE must not stand in for the service's
	// verdict when the service review is partial.
	cases := []struct {
		name string
		head string
		env  map[string]string
	}{
		{"no-service-review", "1111111111111111", map[string]string{"MINOS_FIXTURE_VERDICT": "none"}},
		{"partial-coverage", "2222222222222222", map[string]string{"MINOS_FIXTURE_VERDICT": "partial-coverage"}},
		{"paused-flaky", "4444444444444444", map[string]string{"MINOS_FIXTURE_VERDICT": "paused-flaky"}},
		{"unrelated-human-approve", "3333333333333333", map[string]string{"MINOS_FIXTURE_VERDICT": "partial-coverage", "MINOS_FIXTURE_HUMAN_APPROVE": "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(filepath.Join(env.stateDir, "merge.args"))
			_ = os.Remove(filepath.Join(env.stateDir, "labels-removed"))
			env.setRunEnv(t, tc.head, tc.env)
			if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
				t.Fatal(err)
			}
			run := RunDir(env.runsDir, "local", "minos", "subject", "7", tc.head, RunFinish)
			assertContainsFile(t, filepath.Join(run, "finish-summary.md"), "reason=not-eligible")
			if tc.name == "paused-flaky" {
				assertContainsFile(t, filepath.Join(run, "eligibility-branch"), "paused-flaky")
			}
			if _, err := os.Stat(filepath.Join(env.stateDir, "merge.args")); !os.IsNotExist(err) {
				t.Fatal("an ineligible head reached the merge")
			}
			if removed, _ := os.ReadFile(filepath.Join(env.stateDir, "labels-removed")); strings.Contains(string(removed), "Ready") {
				t.Fatal("an ineligible finish consumed the Ready label")
			}
		})
	}
}
