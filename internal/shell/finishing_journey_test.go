package shell

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSnapshotWatcherReturnsTheTrustedChangedSnapshot(t *testing.T) {
	baseline := map[string]any{
		"head_sha": "head", "target_sha": "target",
		"statuses": []any{}, "labels": []any{}, "check_decision": "pending",
	}
	baselinePath := writeJSONFixture(t, baseline)

	for _, changedField := range []string{"head_sha", "target_sha", "statuses", "labels"} {
		t.Run(changedField, func(t *testing.T) {
			changed := mapsClone(baseline)
			switch changedField {
			case "head_sha":
				changed[changedField] = "new-head"
			case "target_sha":
				changed[changedField] = "new-target"
			case "statuses":
				changed[changedField] = []any{map[string]any{"id": 2, "context": "build", "state": "success"}}
			case "labels":
				changed[changedField] = []any{"Flaky Test"}
			}
			changedPath := writeJSONFixture(t, changed)
			counter := filepath.Join(t.TempDir(), "counter")
			stub := writeExecutable(t, `#!/bin/sh
set -eu
count=0
[ ! -f "$WATCH_COUNTER" ] || count=$(sed -n '1p' "$WATCH_COUNTER")
count=$((count + 1))
printf '%s\n' "$count" >"$WATCH_COUNTER"
if [ "$count" -eq 1 ]; then
  sed -n '1,$p' "$WATCH_BASELINE"
else
  sed -n '1,$p' "$WATCH_CHANGED"
fi
`)
			cmd := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "watch-snapshot"), baselinePath, "5", "0")
			cmd.Env = append(os.Environ(), "MINOS_BIN="+stub, "WATCH_COUNTER="+counter, "WATCH_BASELINE="+baselinePath, "WATCH_CHANGED="+changedPath)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("watch-snapshot: %v\n%s", err, output)
			}
			var result struct {
				Outcome  string         `json:"outcome"`
				Snapshot map[string]any `json:"snapshot"`
			}
			if err := json.Unmarshal(output, &result); err != nil {
				t.Fatal(err)
			}
			if result.Outcome != "changed" || reflect.DeepEqual(result.Snapshot[changedField], baseline[changedField]) {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestSnapshotWatcherTimesOutWithAFreshTrustedSnapshot(t *testing.T) {
	baseline := writeJSONFixture(t, map[string]any{
		"head_sha": "head", "target_sha": "target", "statuses": []any{}, "labels": []any{},
	})
	stub := writeExecutable(t, "#!/bin/sh\nsed -n '1,$p' \"$WATCH_BASELINE\"\n")
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "watch-snapshot"), baseline, "0", "0")
	cmd.Env = append(os.Environ(), "MINOS_BIN="+stub, "WATCH_BASELINE="+baseline)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("watch-snapshot: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), `"outcome":"timeout"`) || !strings.Contains(string(output), `"head_sha":"head"`) {
		t.Fatalf("timeout output = %s", output)
	}
}

func TestSyncTargetNoOpCleanAndConflictJourneys(t *testing.T) {
	t.Run("clean target is unchanged", func(t *testing.T) {
		remote, workspace := createSyncFixture(t, false, false)
		head := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
		target := gitOutput(t, remote, "rev-parse", "refs/heads/main")
		output, err := runSyncTarget(workspace, head, target, "merge")
		if err != nil || !strings.Contains(output, `"outcome":"unchanged"`) {
			t.Fatalf("sync-target: %v\n%s", err, output)
		}
	})

	t.Run("moved target is merged and pushed once", func(t *testing.T) {
		remote, workspace := createSyncFixture(t, true, false)
		installPushCounter(t, remote)
		head := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
		target := gitOutput(t, remote, "rev-parse", "refs/heads/main")
		output, err := runSyncTarget(workspace, head, target, "merge")
		if err != nil || !strings.Contains(output, `"outcome":"synced"`) {
			t.Fatalf("sync-target: %v\n%s", err, output)
		}
		if got := readPushCount(t, remote); got != 1 {
			t.Fatalf("pushes = %d, want one", got)
		}
		if err := exec.Command("git", "--git-dir", remote, "merge-base", "--is-ancestor", target, "refs/heads/feature").Run(); err != nil {
			t.Fatal("remote source does not contain moved target")
		}
	})

	t.Run("forge rebase style is honoured", func(t *testing.T) {
		remote, workspace := createSyncFixture(t, true, false)
		installPushCounter(t, remote)
		head := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
		target := gitOutput(t, remote, "rev-parse", "refs/heads/main")
		output, err := runSyncTarget(workspace, head, target, "rebase")
		if err != nil || !strings.Contains(output, `"method":"rebase"`) {
			t.Fatalf("sync-target: %v\n%s", err, output)
		}
		if got := readPushCount(t, remote); got != 1 {
			t.Fatalf("pushes = %d, want one", got)
		}
		if err := exec.Command("git", "--git-dir", remote, "merge-base", "--is-ancestor", target, "refs/heads/feature").Run(); err != nil {
			t.Fatal("rebased source does not contain moved target")
		}
	})

	t.Run("a concurrent source move loses the exact-head push lease", func(t *testing.T) {
		remote, workspace := createSyncFixture(t, true, false)
		head := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
		target := gitOutput(t, remote, "rev-parse", "refs/heads/main")
		hook := filepath.Join(workspace, ".git", "hooks", "pre-push")
		body := "#!/bin/sh\ngit --git-dir='" + remote + "' update-ref refs/heads/feature '" + target + "'\n"
		if err := os.WriteFile(hook, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}

		output, err := runSyncTarget(workspace, head, target, "merge")
		if err == nil {
			t.Fatalf("sync unexpectedly overwrote the concurrent source move:\n%s", output)
		}
		if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != target {
			t.Fatalf("remote source = %q, want concurrent value %q", got, target)
		}
	})

	t.Run("lead can resolve a real conflict and resume the scripted push", func(t *testing.T) {
		remote, workspace := createSyncFixture(t, true, true)
		installPushCounter(t, remote)
		head := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
		target := gitOutput(t, remote, "rev-parse", "refs/heads/main")
		output, err := runSyncTarget(workspace, head, target, "merge")
		if err == nil || !strings.Contains(output, `"outcome":"conflict"`) {
			t.Fatalf("first sync = %v\n%s", err, output)
		}
		if err := os.WriteFile(filepath.Join(workspace, "shared.txt"), []byte("resolved\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, workspace, "add", "shared.txt")
		runGit(t, workspace, "commit", "-m", "Merge target branch")
		output, err = runSyncTarget(workspace, head, target, "merge")
		if err != nil || !strings.Contains(output, `"outcome":"synced"`) {
			t.Fatalf("resumed sync = %v\n%s", err, output)
		}
		if got := readPushCount(t, remote); got != 1 {
			t.Fatalf("pushes = %d, want one", got)
		}
	})
}

func createSyncFixture(t *testing.T, moveTarget, conflict bool) (string, string) {
	t.Helper()
	remote, seed := createBareFixtureRemote(t, "shared.txt", "base\n")
	runGit(t, seed, "checkout", "-b", "feature")
	if conflict {
		if err := os.WriteFile(filepath.Join(seed, "shared.txt"), []byte("feature\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(filepath.Join(seed, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, seed, "add", ".")
	runGit(t, seed, "commit", "-m", "feature")
	runGit(t, seed, "push", "origin", "feature")
	if moveTarget {
		runGit(t, seed, "checkout", "main")
		name := "target.txt"
		if conflict {
			name = "shared.txt"
		}
		if err := os.WriteFile(filepath.Join(seed, name), []byte("target\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, seed, "add", ".")
		runGit(t, seed, "commit", "-m", "target")
		runGit(t, seed, "push", "origin", "main")
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	runGit(t, t.TempDir(), "clone", "--branch", "feature", remote, workspace)
	runGit(t, workspace, "config", "user.name", "Minos")
	runGit(t, workspace, "config", "user.email", "minos@example.invalid")
	return remote, workspace
}

func runSyncTarget(workspace, head, target, method string) (string, error) {
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "sync-target"), workspace, "feature", "main", head, target, method)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func writeExecutable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "command")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
