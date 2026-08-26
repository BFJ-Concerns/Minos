package shell

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bfj/minos/internal/product"
)

func TestFinishingMovedTargetCommandsReadFreshFixtureSnapshot(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, facts := state.service(t)
	configureFixtureForgeCommand(t, cfg, facts)

	sync := runMovedTargetSyncJourney(t)
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["head"].(map[string]any)["sha"] = sync.pushedHead
		pullRequest["base"].(map[string]any)["sha"] = sync.target
	})
	// The fixture forge is not connected to the bare Git remote, so seed the
	// successful check which a live forge would attach to the pushed head.
	state.setStatuses([]map[string]any{{
		"id": float64(8), "context": "CI / test", "status": "success",
		"description": "fresh checks passed", "creator": map[string]any{"login": "forgejo-actions"},
	}})

	var snapshotOutput bytes.Buffer
	if err := ForgeCommand(t.Context(), []string{"snapshot"}, &snapshotOutput); err != nil {
		t.Fatalf("fresh forge snapshot: %v", err)
	}
	var snapshot struct {
		HeadSHA   string `json:"head_sha"`
		TargetSHA string `json:"target_sha"`
		Statuses  []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	if err := json.Unmarshal(snapshotOutput.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.HeadSHA != sync.pushedHead || snapshot.TargetSHA != sync.target ||
		len(snapshot.Statuses) != 1 || snapshot.Statuses[0].Context != "CI / test" || snapshot.Statuses[0].State != "success" {
		t.Fatalf("fresh snapshot = %#v", snapshot)
	}
}

func TestFinishingUnmovedTargetCommandsPublishBoundHold(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, facts := state.service(t)
	configureFixtureForgeCommand(t, cfg, facts)
	head, target := state.headSHA(), state.targetSHA()

	commentPath := writeJSONFixture(t, map[string]any{
		"body": "Held at: finishing\nCI / test failed while provisioning the toolchain; the reconciled tree is excluded by the captured evidence.",
	})
	for attempt := 1; attempt <= 2; attempt++ {
		if err := ForgeCommand(t.Context(), []string{"comment", head, target, commentPath}, &bytes.Buffer{}); err != nil {
			t.Fatalf("held comment attempt %d: %v", attempt, err)
		}
		if err := ForgeCommand(t.Context(), []string{"status", head, target, "held"}, &bytes.Buffer{}); err != nil {
			t.Fatalf("held status attempt %d: %v", attempt, err)
		}
	}

	posts := state.statusPostFacts()
	wantTargetURL := cfg.Forges[facts.Forge].APIBase + "/minos-e2e-owner/subject/pulls/1#minos-target-" + target +
		"+minos-env-" + currentEnvironmentStamp(cfg)
	if len(posts) != 1 || posts[0].Head != head || posts[0].Payload["state"] != "pending" ||
		posts[0].Payload["description"] != product.Held().Description() ||
		posts[0].Payload["target_url"] != wantTargetURL {
		t.Fatalf("held status posts = %#v", posts)
	}
	comments := state.issueCommentFacts()
	if len(comments) != 1 || !strings.HasPrefix(comments[0], "Held at: finishing\n") {
		t.Fatalf("held comments = %#v", comments)
	}
}

func configureFixtureForgeCommand(t *testing.T, cfg ServiceConfig, facts Facts) {
	t.Helper()
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)
	t.Setenv("MINOS_PR", facts.PR)
}

func TestSnapshotWatcherReturnsTheTrustedChangedSnapshot(t *testing.T) {
	baseline := map[string]any{
		"head_sha": "head", "target_sha": "target",
		"statuses": []any{}, "labels": []any{}, "check_decision": "pending",
		"dependencies_available": true, "dependency_error": "", "open_dependencies": []any{},
	}
	baselinePath := writeJSONFixture(t, baseline)

	for _, changedField := range []string{
		"head_sha", "target_sha", "statuses", "labels",
		"dependencies_available", "dependency_error", "open_dependencies",
	} {
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
			case "dependencies_available":
				changed[changedField] = false
			case "dependency_error":
				changed[changedField] = "forge returned HTTP 503"
			case "open_dependencies":
				changed[changedField] = []any{map[string]any{"repository": "owner/prerequisite", "number": 7}}
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
		runMovedTargetSyncJourney(t)
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

func TestSyncMemberTargetReconcilesMovedTargetAndKeepsPrimaryPin(t *testing.T) {
	for _, method := range []string{"merge", "rebase"} {
		t.Run(method, func(t *testing.T) {
			remote, primary := createSyncFixture(t, true, false)
			installPushCounter(t, remote)
			commonDir := installMemberPushGuard(t, primary)
			installMemberProtectedRef(t, commonDir, "feature")
			head := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
			target := gitOutput(t, remote, "rev-parse", "refs/heads/main")
			primaryPin := gitOutput(t, primary, "rev-parse", "HEAD")
			runGit(t, primary, "update-ref", "refs/minos/target", primaryPin)
			member := filepath.Join(t.TempDir(), "member")
			runGit(t, primary, "worktree", "add", "--detach", member, target)

			output, err := runSyncMemberTarget(member, head, target, method)
			if err != nil || !strings.Contains(string(output), `"outcome":"synced"`) ||
				!strings.Contains(string(output), `"method":"`+method+`"`) {
				t.Fatalf("sync-member-target: %v\n%s", err, output)
			}
			pushedHead := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
			if pushedHead == head {
				t.Fatal("member target sync did not publish a new source head")
			}
			if got := gitOutput(t, member, "rev-parse", "HEAD"); got != pushedHead {
				t.Fatalf("member HEAD = %q, want published head %q", got, pushedHead)
			}
			if got := readPushCount(t, remote); got != 1 {
				t.Fatalf("pushes = %d, want one", got)
			}
			assertContainsFile(t, filepath.Join(commonDir, "minos-protected-ref.member-2"), "refs/heads/feature")
			if err := os.WriteFile(filepath.Join(member, "unpermitted.txt"), []byte("unpermitted\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, member, "add", "unpermitted.txt")
			runGit(t, member, "commit", "-m", "unpermitted member update")
			push := exec.Command("git", "-C", member, "push", "origin", "HEAD:refs/heads/feature")
			if output, err := push.CombinedOutput(); err == nil || !strings.Contains(string(output), "controlled branch updates") {
				t.Fatalf("unpermitted member push = %v\n%s", err, output)
			}
			if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != pushedHead {
				t.Fatalf("remote member branch moved without a permit: got %q, want %q", got, pushedHead)
			}
			if err := exec.Command("git", "--git-dir", remote, "merge-base", "--is-ancestor", target, pushedHead).Run(); err != nil {
				t.Fatal("published member source does not contain moved target")
			}
			if got := gitOutput(t, primary, "rev-parse", "HEAD"); got != primaryPin {
				t.Fatalf("primary HEAD = %q, want unchanged %q", got, primaryPin)
			}
			if got := gitOutput(t, primary, "rev-parse", "refs/minos/target"); got != primaryPin {
				t.Fatalf("primary target pin = %q, want unchanged %q", got, primaryPin)
			}
		})
	}
}

func TestSyncMemberTargetRejectsTargetMovedPastExpectedSnapshot(t *testing.T) {
	remote, primary := createSyncFixture(t, true, false)
	commonDir := gitOutput(t, primary, "rev-parse", "--path-format=absolute", "--git-common-dir")
	installMemberProtectedRef(t, commonDir, "feature")
	head := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
	expectedTarget := gitOutput(t, remote, "rev-parse", "refs/heads/main")

	targetMover := filepath.Join(t.TempDir(), "target-mover")
	runGit(t, t.TempDir(), "clone", "--branch", "main", remote, targetMover)
	runGit(t, targetMover, "config", "user.name", "Fixture")
	runGit(t, targetMover, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(targetMover, "later-target.txt"), []byte("later target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, targetMover, "add", "later-target.txt")
	runGit(t, targetMover, "commit", "-m", "later target")
	runGit(t, targetMover, "push", "origin", "main")
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/main"); got == expectedTarget {
		t.Fatal("fixture did not move the target past the expected snapshot")
	}

	member := filepath.Join(t.TempDir(), "member")
	runGit(t, primary, "worktree", "add", "--detach", member, expectedTarget)
	memberHead := gitOutput(t, member, "rev-parse", "HEAD")
	output, err := runSyncMemberTarget(member, head, expectedTarget, "merge")
	if err == nil || !strings.Contains(string(output), "member source or target moved") {
		t.Fatalf("member sync accepted a target newer than its snapshot: %v\n%s", err, output)
	}
	if got := gitOutput(t, member, "rev-parse", "HEAD"); got != memberHead {
		t.Fatalf("member HEAD changed before target snapshot rejection: got %q, want %q", got, memberHead)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != head {
		t.Fatalf("remote member branch was published despite target snapshot rejection: got %q, want %q", got, head)
	}
}

func TestSyncMemberTargetConflictCanBeResolvedAndResumed(t *testing.T) {
	for _, method := range []string{"merge", "rebase"} {
		t.Run(method, func(t *testing.T) {
			remote, primary := createSyncFixture(t, true, true)
			installPushCounter(t, remote)
			commonDir := installMemberPushGuard(t, primary)
			installMemberProtectedRef(t, commonDir, "feature")
			head := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
			target := gitOutput(t, remote, "rev-parse", "refs/heads/main")
			member := filepath.Join(t.TempDir(), "member")
			runGit(t, primary, "worktree", "add", "--detach", member, target)

			output, err := runSyncMemberTarget(member, head, target, method)
			if err == nil || !strings.Contains(string(output), `"outcome":"conflict"`) ||
				!strings.Contains(string(output), `"operation":"`+method+`"`) {
				t.Fatalf("first member sync = %v\n%s", err, output)
			}
			assertContainsFile(t, filepath.Join(commonDir, "minos-member-sync-2"), head)
			output, err = runSyncMemberTarget(member, head, target, method)
			if err == nil || !strings.Contains(string(output), "finish the current merge or rebase") {
				t.Fatalf("unresolved member sync = %v\n%s", err, output)
			}
			if err := os.WriteFile(filepath.Join(member, "shared.txt"), []byte("resolved\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, member, "add", "shared.txt")
			if method == "rebase" {
				runGit(t, member, "-c", "core.editor=true", "rebase", "--continue")
			} else {
				runGit(t, member, "commit", "-m", "Merge target branch for grouped member")
			}
			resolved := gitOutput(t, member, "rev-parse", "HEAD")
			output, err = runSyncMemberTarget(member, head, target, method)
			if err != nil || !strings.Contains(string(output), `"outcome":"synced"`) ||
				!strings.Contains(string(output), `"method":"`+method+`"`) {
				t.Fatalf("resumed member sync = %v\n%s", err, output)
			}
			if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != resolved {
				t.Fatalf("remote member branch = %q, want resolved head %q", got, resolved)
			}
			if got := readPushCount(t, remote); got != 1 {
				t.Fatalf("pushes = %d, want one", got)
			}
			if _, err := os.Stat(filepath.Join(commonDir, "minos-member-sync-2")); !os.IsNotExist(err) {
				t.Fatalf("completed member reconciliation record remains: %v", err)
			}
		})
	}
}

func TestSyncMemberTargetConcurrentSourceMoveLosesPushLease(t *testing.T) {
	remote, primary := createSyncFixture(t, true, false)
	commonDir := installMemberPushGuard(t, primary)
	installMemberProtectedRef(t, commonDir, "feature")
	head := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
	target := gitOutput(t, remote, "rev-parse", "refs/heads/main")
	member := filepath.Join(t.TempDir(), "member")
	runGit(t, primary, "worktree", "add", "--detach", member, target)
	guard, err := filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", "pre-push-guard"))
	if err != nil {
		t.Fatal(err)
	}
	commonDir = gitOutput(t, primary, "rev-parse", "--path-format=absolute", "--git-common-dir")
	hook := "#!/bin/sh\nset -eu\ngit --git-dir=\"" + remote + "\" update-ref refs/heads/feature \"" + target + "\"\nexec \"" + guard + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(commonDir, "hooks", "pre-push"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	output, err := runSyncMemberTarget(member, head, target, "merge")
	if err == nil {
		t.Fatalf("member sync unexpectedly overwrote the concurrent source move:\n%s", output)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != target {
		t.Fatalf("remote member branch = %q, want concurrent value %q", got, target)
	}
}

func installMemberPushGuard(t *testing.T, workspace string) string {
	t.Helper()
	commonDir := gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
	guard, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-body", "pre-push-guard"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "hooks", "pre-push"), guard, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "minos-protected-ref"), []byte("refs/heads/primary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return commonDir
}

func installMemberProtectedRef(t *testing.T, commonDir, branch string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(commonDir, "minos-protected-ref.member-2"), []byte("refs/heads/"+branch+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runSyncMemberTarget(workspace, head, target, method string) ([]byte, error) {
	command := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "sync-member-target"), workspace, "2", "feature", "main", head, target, method)
	return command.CombinedOutput()
}

type movedTargetSyncFacts struct {
	pushedHead string
	target     string
}

func runMovedTargetSyncJourney(t *testing.T) movedTargetSyncFacts {
	t.Helper()
	remote, workspace := createSyncFixture(t, true, false)
	installPushCounter(t, remote)
	head := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
	target := gitOutput(t, remote, "rev-parse", "refs/heads/main")
	output, err := runSyncTarget(workspace, head, target, "merge")
	if err != nil || !strings.Contains(output, `"outcome":"synced"`) {
		t.Fatalf("sync-target: %v\n%s", err, output)
	}
	pushedHead := gitOutput(t, remote, "rev-parse", "refs/heads/feature")
	if pushedHead == head {
		t.Fatal("target sync did not move the source head")
	}
	if got := readPushCount(t, remote); got != 1 {
		t.Fatalf("pushes = %d, want one", got)
	}
	if err := exec.Command("git", "--git-dir", remote, "merge-base", "--is-ancestor", target, pushedHead).Run(); err != nil {
		t.Fatal("remote source does not contain moved target")
	}
	return movedTargetSyncFacts{pushedHead: pushedHead, target: target}
}

func createSyncFixture(t *testing.T, moveTarget, conflict bool) (string, string) {
	t.Helper()
	remote, seed := createBareFixtureRemote(t, "shared.txt", "base\n")
	runGit(t, seed, "config", "user.name", "Fixture")
	runGit(t, seed, "config", "user.email", "fixture@example.invalid")
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
