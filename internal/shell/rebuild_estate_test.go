package shell

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestRebuildEstateAdmissionBootstrapsGroundedLead(t *testing.T) {
	codeRepository, head := createGitRepository(t, "code.txt", "estate reviewed head\n")
	annexeRepository := createGitRepositoryAtHead(t, "README.md", "# Estate commission\n\nDistinctive grounding value: cinnabar-orbit-719.\n")
	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["head"].(map[string]any)["sha"] = head
		pullRequest["head"].(map[string]any)["repo"].(map[string]any)["clone_url"] = codeRepository
	})
	state.setAnnexeCloneURL(annexeRepository)

	cfg, repo, facts := state.service(t)
	runBody, err := filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", "run-body"))
	if err != nil {
		t.Fatal(err)
	}
	repo.Adaptation.RunBody = runBody
	record := writeEstateRunBodyConfig(t, cfg.Root)
	writeServiceConfig(t, cfg)

	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)
	t.Setenv("MINOS_PR", facts.PR)
	for attempt := 1; attempt <= 2; attempt++ {
		if err := ForgeCommand(t.Context(), []string{"claim"}, &strings.Builder{}); err != nil {
			t.Fatalf("claim attempt %d: %v", attempt, err)
		}
	}
	state.mu.Lock()
	assignmentWrites := state.assignmentWrites
	obsoleteAssignmentWrites := state.obsoleteAssignmentWrites
	reactionWrites := state.reactionWrites
	assignees := append([]string(nil), state.assignees...)
	reactions := append([]string(nil), state.reactions...)
	state.mu.Unlock()
	if assignmentWrites != 1 || obsoleteAssignmentWrites != 0 || reactionWrites != 1 ||
		!slices.Contains(assignees, "Minos") || !slices.Contains(reactions, "eyes") {
		t.Fatalf("claim facts = assignment:%d obsolete:%d reaction:%d assignees:%v reactions:%v",
			assignmentWrites, obsoleteAssignmentWrites, reactionWrites, assignees, reactions)
	}

	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	var activeUnit string
	var starts [][]string
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			if activeUnit == "" {
				return nil, nil
			}
			return []byte(activeUnit + " loaded active running Minos lead\n"), nil
		case "systemd-run":
			unitAt := slices.Index(args, "--unit")
			if unitAt < 0 || unitAt+1 >= len(args) {
				t.Fatalf("systemd-run arguments omit unit: %v", args)
			}
			activeUnit = args[unitAt+1]
			starts = append(starts, append([]string(nil), args...))
			return nil, nil
		default:
			t.Fatalf("unexpected command %q", name)
			return nil, nil
		}
	}

	result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
	if err != nil {
		t.Fatal(err)
	}
	if result != "started" || len(starts) != 1 {
		t.Fatalf("first admission = %q, starts = %d; want one start", result, len(starts))
	}
	firstEnvironment := systemdEnvironment(t, starts[0])
	runRecordedBody(t, runBody, firstEnvironment)

	workspace := firstEnvironment["MINOS_WORKSPACE"]
	orientationPath := firstEnvironment["MINOS_ORIENTATION"]
	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != head {
		t.Fatalf("workspace head = %q, want admitted head %q", got, head)
	}
	orientation := readOrientation(t, orientationPath)
	if orientation.Repository != workspace || orientation.Head != head || orientation.Grounding != "annexe" {
		t.Fatalf("orientation = %+v, want admitted workspace, head and annexe grounding", orientation)
	}
	if orientation.Annexe != filepath.Join(firstEnvironment["MINOS_RUN_DIR"], "subject-Annexe") ||
		orientation.Guidance != filepath.Join(orientation.Annexe, "README.md") {
		t.Fatalf("orientation paths = %+v, want adjacent subject annexe README", orientation)
	}
	assertContainsFile(t, orientation.Guidance, "cinnabar-orbit-719")
	assertContainsFile(t, record+".grounding", "cinnabar-orbit-719")
	assertContainsFile(t, record+".acceptance", `"hasCompletedOnboarding":true`)
	assertContainsFile(t, record+".acceptance", `"bypassPermissionsModeAccepted":true`)
	assertContainsFile(t, record+".argv", "read the commission in the recorded annexe README")
	for _, value := range []string{
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=anthropic-gpt-5.6-terra",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS=265000",
		"GIT_AUTHOR_NAME=Minos",
		"GIT_AUTHOR_EMAIL=minos@example.invalid",
	} {
		assertContainsFile(t, record+".env", value)
	}
	assertContainsFile(t, record+".argv", "anthropic-gpt-5.6-sol")

	if err := os.WriteFile(filepath.Join(workspace, "estate-repair.txt"), []byte("repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "estate-repair.txt")
	runGit(t, workspace, "commit", "-m", "fix: exercise estate author")
	if got := gitOutput(t, workspace, "log", "-1", "--format=%an <%ae>"); got != "Minos <minos@example.invalid>" {
		t.Fatalf("repair author = %q, want Minos identity", got)
	}

	state.changePullRequest(func(pullRequest map[string]any) { pullRequest["number"] = float64(2) })
	secondFacts := facts
	secondFacts.PR = "2"
	result, err = reconcilePullRequest(t.Context(), cfg, repo, secondFacts)
	if err != nil {
		t.Fatal(err)
	}
	if result != "suppressed" || len(starts) != 1 {
		t.Fatalf("second admission while active = %q, starts = %d; want suppression", result, len(starts))
	}
	activeUnit = ""
	result, err = reconcilePullRequest(t.Context(), cfg, repo, secondFacts)
	if err != nil {
		t.Fatal(err)
	}
	if result != "started" || len(starts) != 2 || !strings.Contains(activeUnit, "pr2") {
		t.Fatalf("second admission after release = %q, active = %q, starts = %d", result, activeUnit, len(starts))
	}
}

func TestRebuildEstateBootstrapRefusesHeadMoveBeforeLeadLaunch(t *testing.T) {
	codeRepository, admittedHead := createGitRepository(t, "code.txt", "admitted head\n")
	if err := os.WriteFile(filepath.Join(codeRepository, "code.txt"), []byte("moved head\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, codeRepository, "add", "code.txt")
	runGit(t, codeRepository, "commit", "-m", "move fixture head")
	movedHead := gitOutput(t, codeRepository, "rev-parse", "HEAD")

	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["head"].(map[string]any)["sha"] = admittedHead
		pullRequest["head"].(map[string]any)["repo"].(map[string]any)["clone_url"] = codeRepository
	})
	cfg, repo, facts := state.service(t)
	runBody, err := filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", "run-body"))
	if err != nil {
		t.Fatal(err)
	}
	repo.Adaptation.RunBody = runBody
	record := writeEstateRunBodyConfig(t, cfg.Root)

	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	var startArguments []string
	commandCombinedOutput = func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			return nil, nil
		case "systemd-run":
			startArguments = append([]string(nil), arguments...)
			return nil, nil
		default:
			t.Fatalf("unexpected command %q", name)
			return nil, nil
		}
	}
	result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
	if err != nil {
		t.Fatal(err)
	}
	if result != "started" || len(startArguments) == 0 {
		t.Fatalf("admission = %q, systemd arguments = %v; want recorded start", result, startArguments)
	}

	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["head"].(map[string]any)["sha"] = movedHead
	})
	environment := systemdEnvironment(t, startArguments)
	cmd := exec.Command(runBody)
	cmd.Env = environmentWithOverrides(environment)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("run body accepted moved head:\n%s", output)
	}
	want := fmt.Sprintf("head moved from %s to %s", admittedHead, movedHead)
	if !strings.Contains(string(output), want) {
		t.Fatalf("run body failure = %q, want %q", output, want)
	}
	if _, err := os.Stat(record + ".argv"); !os.IsNotExist(err) {
		t.Fatalf("lead recorder was invoked after moved head: %v", err)
	}
	if _, err := os.Stat(environment["MINOS_WORKSPACE"]); !os.IsNotExist(err) {
		t.Fatalf("workspace exists after moved-head refusal: %v", err)
	}
}

func TestRebuildEstateTerminalRecoveryBindsPullRequestHeadTargetAndStatus(t *testing.T) {
	for _, test := range []struct {
		name          string
		changeStatus  func(map[string]any)
		wantRecovered bool
	}{
		{name: "matching terminal status", wantRecovered: false},
		{name: "different pull request", wantRecovered: true, changeStatus: func(status map[string]any) {
			status["target_url"] = strings.Replace(status["target_url"].(string), "/pulls/1#", "/pulls/2#", 1)
		}},
		{name: "different target", wantRecovered: true, changeStatus: func(status map[string]any) {
			status["target_url"] = strings.Replace(status["target_url"].(string), "#minos-target-", "#minos-target-other-", 1)
		}},
		{name: "different terminal state", wantRecovered: true, changeStatus: func(status map[string]any) {
			status["status"] = "failure"
		}},
		{name: "different status description", wantRecovered: true, changeStatus: func(status map[string]any) {
			status["description"] = "Not the Minos terminal description"
		}},
		{name: "different status creator", wantRecovered: true, changeStatus: func(status map[string]any) {
			status["creator"] = map[string]any{"login": "another-bot"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			state.setReviews([]map[string]any{{
				"id": 41, "state": "APPROVED", "commit_id": state.headSHA(),
				"body": "terminal review", "user": map[string]any{"login": "Minos"},
			}})
			cfg, repo, facts := state.service(t)
			facts.HeadSHA = state.headSHA()
			facts.BaseSHA = state.targetSHA()
			status := map[string]any{
				"id": 7, "context": "Minos", "status": "success", "description": "Changes approved",
				"target_url": statusTargetURL(cfg.Forges["forgejo"].APIBase, facts),
				"creator":    map[string]any{"login": "Minos"},
			}
			if test.changeStatus != nil {
				test.changeStatus(status)
			}
			state.setStatuses([]map[string]any{status})

			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				t.Fatalf("terminally reviewed pull request reached %s", name)
				return nil, nil
			}

			result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
			if err != nil {
				t.Fatal(err)
			}
			want := "nothing"
			if test.wantRecovered {
				want = "recovered"
			}
			if result != want {
				t.Fatalf("result = %q, want %q", result, want)
			}
			writes, repairedTarget := state.statusWriteFacts()
			if test.wantRecovered {
				if writes != 1 || repairedTarget != statusTargetURL(cfg.Forges["forgejo"].APIBase, facts) {
					t.Fatalf("recovery writes = %d, target = %v; want one exact identity write", writes, repairedTarget)
				}
			} else if writes != 0 {
				t.Fatalf("matching status caused %d recovery writes", writes)
			}
			statusReads := state.statusReadFacts()
			if len(statusReads) == 0 {
				t.Fatal("terminal reconciliation made no current-head status read")
			}
			for _, commit := range statusReads {
				if commit != facts.HeadSHA {
					t.Fatalf("status read commit = %q, want current head %q", commit, facts.HeadSHA)
				}
			}
		})
	}
}

func TestRebuildEstateIncompleteStatusUsesGuardedTargetBoundForgePath(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, facts := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)
	t.Setenv("MINOS_PR", facts.PR)

	head := state.headSHA()
	target := state.targetSHA()
	var stdout strings.Builder
	if err := ForgeCommand(t.Context(), []string{"status", head, target, "incomplete"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"outcome":"applied"`) {
		t.Fatalf("incomplete status output = %q, want applied", stdout.String())
	}

	state.mu.Lock()
	writes := state.statusWrites
	if len(state.statuses) == 0 {
		state.mu.Unlock()
		t.Fatal("incomplete status write was not recorded")
	}
	written := make(map[string]any, len(state.statuses[0]))
	for key, value := range state.statuses[0] {
		written[key] = value
	}
	state.mu.Unlock()
	if writes != 1 || written["state"] != "error" || written["context"] != "Minos" ||
		written["description"] != "Review incomplete" ||
		written["target_url"] != statusTargetURL(cfg.Forges[facts.Forge].APIBase, Facts{
			Owner: facts.Owner, Repo: facts.Repo, PR: facts.PR, BaseSHA: target,
		}) {
		t.Fatalf("incomplete status writes = %d, payload = %#v", writes, written)
	}
	creator, ok := written["creator"].(map[string]any)
	if !ok || creator["login"] != "Minos" {
		t.Fatalf("incomplete status creator = %#v, want Minos", written["creator"])
	}

	stdout.Reset()
	err := ForgeCommand(t.Context(), []string{"status", head, target + "-stale", "incomplete"}, &stdout)
	if err == nil {
		t.Fatal("stale target accepted an incomplete status")
	}
	if !strings.Contains(err.Error(), "status rejected") || !strings.Contains(stdout.String(), `"outcome":"rejected"`) {
		t.Fatalf("stale-target result = %q, error = %v; want guarded rejection", stdout.String(), err)
	}
	state.mu.Lock()
	writesAfterRejection := state.statusWrites
	state.mu.Unlock()
	if writesAfterRejection != 1 {
		t.Fatalf("stale target changed status write count to %d, want one", writesAfterRejection)
	}
}

func TestRebuildEstateReviewCompletionReactionJourneys(t *testing.T) {
	for _, test := range []struct {
		name        string
		findingsFix bool
	}{
		{name: "clean brief stage"},
		{name: "brief findings fixed on a fresh head", findingsFix: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			cfg, _, facts := state.service(t)
			writeServiceConfig(t, cfg)
			t.Setenv("MINOS_CONFIG", cfg.Root)
			t.Setenv("MINOS_FORGE", facts.Forge)
			t.Setenv("MINOS_OWNER", facts.Owner)
			t.Setenv("MINOS_REPO_NAME", facts.Repo)
			t.Setenv("MINOS_PR", facts.PR)

			target := state.targetSHA()
			head := state.headSHA()
			wantReviews := 0
			if test.findingsFix {
				directory := t.TempDir()
				bodyPath := filepath.Join(directory, "brief.md")
				commentsPath := filepath.Join(directory, "comments.json")
				if err := os.WriteFile(bodyPath, []byte("Repository review brief findings.\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(commentsPath, []byte(`[{"path":"internal/state.go","body":"Brief concern.","new_position":41}]`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := ForgeCommand(t.Context(), []string{"review", head, target, "comment", bodyPath, commentsPath}, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
				wantReviews = 1
				state.changePullRequest(func(pullRequest map[string]any) {
					pullRequest["head"].(map[string]any)["sha"] = "feedfacefeedfacefeedfacefeedfacefeedface"
				})
				head = state.headSHA()
			}

			for attempt := 1; attempt <= 2; attempt++ {
				if err := ForgeCommand(t.Context(), []string{"reaction", head, target, "+1"}, &bytes.Buffer{}); err != nil {
					t.Fatalf("reaction attempt %d: %v", attempt, err)
				}
			}

			state.mu.Lock()
			defer state.mu.Unlock()
			if state.reactionWrites != 1 || !slices.Contains(state.reactions, "+1") {
				t.Fatalf("reactions = %v, writes = %d, want one +1 write", state.reactions, state.reactionWrites)
			}
			if state.reviewWrites != wantReviews {
				t.Fatalf("review writes = %d, want %d", state.reviewWrites, wantReviews)
			}
			if test.findingsFix && state.reviewPayloads[0]["commit_id"] == head {
				t.Fatalf("brief findings review and completion reaction both used %q; want reaction on the fresh repaired head", head)
			}
		})
	}
}

func TestRebuildEstateAllRunReachedTerminalOutcomesBindEyesCleanup(t *testing.T) {
	lifecyclePath := filepath.Join("..", "..", "lifecycle", "lifecycle.md")
	lifecycle, err := os.ReadFile(lifecyclePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		instruction string
		merged      bool
		remove      bool
	}{
		{name: "merged", instruction: "Merged,", merged: true, remove: true},
		{name: "request changes", instruction: "request-changes", remove: true},
		{name: "clean without auto merge", instruction: "clean-without-auto-merge", remove: true},
		{name: "incomplete", instruction: "every incomplete outcome", remove: true},
		{name: "crash", instruction: "a crash alone leaves it", remove: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !strings.Contains(string(lifecycle), test.instruction) {
				t.Fatalf("lifecycle omits terminal outcome instruction %q", test.instruction)
			}
			state := newForgejoFixtureState(t)
			state.reactions = []string{"eyes"}
			if test.merged {
				state.changePullRequest(func(pullRequest map[string]any) {
					pullRequest["merged"] = true
					pullRequest["state"] = "closed"
				})
			}
			cfg, _, facts := state.service(t)
			writeServiceConfig(t, cfg)
			t.Setenv("MINOS_CONFIG", cfg.Root)
			t.Setenv("MINOS_FORGE", facts.Forge)
			t.Setenv("MINOS_OWNER", facts.Owner)
			t.Setenv("MINOS_REPO_NAME", facts.Repo)
			t.Setenv("MINOS_PR", facts.PR)

			if test.remove {
				for attempt := 1; attempt <= 2; attempt++ {
					if err := ForgeCommand(t.Context(), []string{"reaction-remove", state.headSHA(), state.targetSHA(), "eyes"}, &bytes.Buffer{}); err != nil {
						t.Fatalf("reaction removal attempt %d: %v", attempt, err)
					}
				}
			}

			state.mu.Lock()
			defer state.mu.Unlock()
			if test.remove {
				if slices.Contains(state.reactions, "eyes") || state.reactionDeleteWrites != 1 {
					t.Fatalf("reactions = %v, delete writes = %d, want eyes absent after one write", state.reactions, state.reactionDeleteWrites)
				}
			} else if !slices.Contains(state.reactions, "eyes") || state.reactionDeleteWrites != 0 {
				t.Fatalf("crash cleanup changed reactions = %v, delete writes = %d", state.reactions, state.reactionDeleteWrites)
			}
		})
	}
}

func writeEstateRunBodyConfig(t *testing.T, configRoot string) string {
	t.Helper()
	root := t.TempDir()
	record := filepath.Join(root, "lead-record")
	claude := filepath.Join(root, "claude")
	writeScript(t, claude, `#!/usr/bin/env sh
set -eu
record="${MINOS_TEST_RECORD:?}"
case "$1" in
  --bg)
    : >"$record.argv"
    for argument in "$@"; do printf '%s\n' "$argument" >>"$record.argv"; done
    env | sort >"$record.env"
    cp "$CLAUDE_CONFIG_DIR/.claude.json" "$record.acceptance"
    guidance="$(jq -r '.guidance' "$MINOS_ORIENTATION")"
    cat "$guidance" >"$record.grounding"
    printf 'Agent backgrounded: abcdef12\n'
    ;;
  agents)
    printf '%s\n' '[{"id":"abcdef12","state":"done"}]'
    ;;
  stop)
    printf 'stop %s\n' "$2" >>"$record.calls"
    ;;
  *)
    printf 'unexpected command: %s\n' "$*" >&2
    exit 1
    ;;
esac
`)
	proxyCredential := filepath.Join(root, "proxy.token")
	if err := os.WriteFile(proxyCredential, []byte("fake-proxy-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(root, "root-cause")
	if err := os.Mkdir(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("# Root Cause\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setup, err := filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", "setup-workspace"))
	if err != nil {
		t.Fatal(err)
	}
	instruction, err := filepath.Abs(filepath.Join("..", "..", "lifecycle", "lifecycle.md"))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"MINOS_CLAUDE":                               claude,
		"MINOS_LEAD_MODEL":                           "anthropic-gpt-5.6-sol",
		"MINOS_GIT_AUTHOR_NAME":                      "Minos",
		"MINOS_GIT_AUTHOR_EMAIL":                     "minos@example.invalid",
		"MINOS_ANTHROPIC_CREDENTIAL_FILE":            proxyCredential,
		"ANTHROPIC_BASE_URL":                         "http://proxy.test:8317",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":              "anthropic-gpt-5.6-terra",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS":             "265000",
		"MINOS_LIFECYCLE_INSTRUCTION":                instruction,
		"MINOS_REVIEW_WORKFLOW":                      "/opt/minos/workflows/review.js",
		"MINOS_ROOT_CAUSE_SKILL":                     skill,
		"MINOS_SETUP_WORKSPACE":                      setup,
		"MINOS_BIN":                                  "/usr/local/bin/minos",
		"MINOS_CLAUDE_POLL_SECONDS":                  "0",
		"MINOS_TEST_RECORD":                          record,
	}
	var config strings.Builder
	for name, value := range values {
		fmt.Fprintf(&config, "%s=%s\n", name, strconv.Quote(value))
	}
	if err := os.WriteFile(filepath.Join(configRoot, "run-body.env"), []byte(config.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return record
}

func systemdEnvironment(t *testing.T, arguments []string) map[string]string {
	t.Helper()
	environment := make(map[string]string)
	for index := 0; index < len(arguments); index++ {
		if arguments[index] != "--setenv" || index+1 >= len(arguments) {
			continue
		}
		name, value, found := strings.Cut(arguments[index+1], "=")
		if !found {
			t.Fatalf("invalid systemd environment argument %q", arguments[index+1])
		}
		environment[name] = value
		index++
	}
	return environment
}

func runRecordedBody(t *testing.T, runBody string, overrides map[string]string) {
	t.Helper()
	cmd := exec.Command(runBody)
	cmd.Env = environmentWithOverrides(overrides)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recorded run body failed: %v\n%s", err, output)
	}
}

func environmentWithOverrides(overrides map[string]string) []string {
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if _, replaced := overrides[name]; !replaced {
			environment = append(environment, value)
		}
	}
	for name, value := range overrides {
		environment = append(environment, name+"="+value)
	}
	return environment
}
