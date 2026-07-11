package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFlakyRunBodyLandsRepairAndKeepsFailuresOffThePR drives the flaky kind
// through the real wrapper with a deterministic lead stand-in. It proves the
// successful credential-separated landing and the deliberately private exits:
// anything short of a landed repair leaves Flaky Tests standing and records no
// comment or commit status on the pull request.
func TestFlakyRunBodyLandsRepairAndKeepsFailuresOffThePR(t *testing.T) {
	env := setupRunBodyHarness(t, "flaky", "flaky-engine-standin", "flaky.md")

	mission, err := os.ReadFile(filepath.Join("..", "..", "missions", "flaky.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mission), "The `"+LabelFlakyTests+"` label is the standing safety condition") {
		t.Fatalf("flaky mission does not name canonical label %q", LabelFlakyTests)
	}
	for path, needle := range map[string]string{
		filepath.Join("..", "..", "missions", "review.md"):                                                "apply `" + LabelFlakyTests + "`",
		filepath.Join("..", "..", "scripts", "e2e", "flaky-engine-standin"):                               `"` + LabelFlakyTests + `"`,
		filepath.Join("..", "..", "examples", "config", "repos", "forgejo-org--BFJ-Concerns--Widget.toml"): "label-added:" + LabelFlakyTests,
		filepath.Join("..", "..", "deploy", "etc", "pump19", "repos", "owner--repository.toml.example"):   "label-added:" + LabelFlakyTests,
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), needle) {
			t.Fatalf("%s does not carry canonical flaky label spelling %q", path, LabelFlakyTests)
		}
	}

	env.setRunEnv(t, "aaaaaaaaaaaaaaaa", map[string]string{
		"PUMP19_FIXTURE_LABELS": "Flaky Tests",
	})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	landed := RunDir(env.runsDir, "local", "pump19", "subject", "7", "aaaaaaaaaaaaaaaa", RunFlaky)
	assertContainsFile(t, filepath.Join(landed, "flaky-summary.md"), "outcome=landed")
	assertContainsFile(t, filepath.Join(landed, "flaky-summary.md"), "run=flaky")
	assertPostedSummaryOmitsModelIdentity(t, filepath.Join(landed, "flaky-summary.md"))
	wantSkill, err := filepath.Abs(filepath.Join("..", "..", "skills", "foundry", "root-cause", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	assertContainsFile(t, filepath.Join(landed, "skill.path"), wantSkill)
	assertContainsFile(t, filepath.Join(env.stateDir, "commit-push.args"), "Pump-19")
	assertContainsFile(t, filepath.Join(env.stateDir, "status.args"), "pump19/flaky\nsuccess")
	assertLastOperation(t, filepath.Join(env.stateDir, "operations"), "remove-label:Flaky Tests")

	// The repair push supplies a new head and the strictly-last label removal
	// lifts the pause. The ordinary review implication is therefore eligible on
	// the new head; the review integration test proves that this path converges.
	reviewRepo := RepoConfig{Triggers: []TriggerRule{{Run: "review", Authors: []string{"*"}}}}
	next := Facts{HeadSHA: "landedsha", Author: "contributor"}
	if decision, ok := reconcileDecision(reviewRepo, next, nil, ""); !ok || decision != RunReview {
		t.Fatalf("post-repair implication = %s ok=%v, want review", decision, ok)
	}

	cases := []struct {
		name    string
		head    string
		extra   map[string]string
		wantErr bool
	}{
		{name: "fruitless", head: "bbbbbbbbbbbbbbbb", extra: map[string]string{"PUMP19_STANDIN_FLAKY_OUTCOME": "fruitless", "PUMP19_FIXTURE_COMMIT_OUTCOME": "fruitless"}},
		{name: "unwritable", head: "cccccccccccccccc", extra: map[string]string{"PUMP19_FIXTURE_FORK": "1"}},
		{name: "suspected", head: "dddddddddddddddd", extra: map[string]string{"PUMP19_STANDIN_FLAKY_OUTCOME": "suspected"}},
		{name: "stopped", head: "eeeeeeeeeeeeeeee", extra: map[string]string{"PUMP19_STANDIN_FLAKY_OUTCOME": "stopped"}},
		{name: "failed", head: "ffffffffffffffff", extra: map[string]string{"PUMP19_STANDIN_FLAKY_OUTCOME": "failed"}, wantErr: true},
		{name: "withdrawn", head: "9999999999999999", extra: map[string]string{"PUMP19_FIXTURE_LABELS": "none"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"comments", "status.args", "labels-removed", "operations", "commit-push.args"} {
				_ = os.Remove(filepath.Join(env.stateDir, name))
			}
			if _, set := tc.extra["PUMP19_FIXTURE_LABELS"]; !set {
				tc.extra["PUMP19_FIXTURE_LABELS"] = "Flaky Tests"
			}
			env.setRunEnv(t, tc.head, tc.extra)
			err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot})
			if tc.wantErr && err == nil {
				t.Fatal("failed flaky scenario unexpectedly completed")
			}
			if !tc.wantErr && err != nil {
				t.Fatal(err)
			}
			run := RunDir(env.runsDir, "local", "pump19", "subject", "7", tc.head, RunFlaky)
			assertContainsFile(t, filepath.Join(run, "run.log"), "Pump-19 flaky")
			for _, name := range []string{"comments", "status.args"} {
				if _, err := os.Stat(filepath.Join(env.stateDir, name)); !os.IsNotExist(err) {
					t.Fatalf("%s flaky outcome wrote %s: %v", tc.name, name, err)
				}
			}
			removedPath := filepath.Join(env.stateDir, "labels-removed")
			if fixtureLineCount(t, removedPath, LabelFlakyTests) != 0 {
				removed, _ := os.ReadFile(removedPath)
				t.Fatalf("%s flaky outcome removed %s:\n%s", tc.name, LabelFlakyTests, removed)
			}
		})
	}
}

func assertLastOperation(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if got := lines[len(lines)-1]; got != want {
		t.Fatalf("last operation = %q, want %q\n%s", got, want, data)
	}
}
