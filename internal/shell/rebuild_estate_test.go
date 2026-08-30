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
	"time"
)

// fixtureStatusTargetURL mirrors the URL guarded-set-status writes: the pull
// request's web URL with the pinned target recorded in the fragment.
func fixtureStatusTargetURL(apiBase string, facts Facts) string {
	webBase := strings.TrimSuffix(strings.TrimSuffix(apiBase, "/"), "/api/v1")
	return fmt.Sprintf("%s/%s/%s/pulls/%s#minos-target-%s", webBase, facts.Owner, facts.Repo, facts.PR, facts.BaseSHA)
}

func TestRebuildEstateAdmissionBootstrapsGroundedLead(t *testing.T) {
	codeRepository, head := createGitRepository(t, "code.txt", "estate reviewed head\n")
	annexeRepository := createGitRepositoryAtHead(t, "README.md", "# Estate commission\n\nDistinctive grounding value: cinnabar-orbit-719.\n")
	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["head"].(map[string]any)["sha"] = head
		pullRequest["head"].(map[string]any)["repo"].(map[string]any)["clone_url"] = codeRepository
		pullRequest["base"].(map[string]any)["sha"] = head
		pullRequest["base"].(map[string]any)["repo"].(map[string]any)["clone_url"] = codeRepository
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
	if result.Decision != "started" || len(starts) != 1 {
		t.Fatalf("first admission = %q, starts = %d; want one start", result, len(starts))
	}
	firstEnvironment := systemdEnvironment(t, starts[0])
	runRecordedBody(t, runBody, firstEnvironment)
	assertContainsFile(t, filepath.Join(firstEnvironment["MINOS_RUN_DIR"], "lead-complete"), "clean")

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
	assertContainsFile(t, record+".auth", "claude-auth-present")
	assertContainsFile(t, record+".auth", "codex-auth-present")
	assertContainsFile(t, record+".argv", "the recorded annexe README as the driving statement")
	runHome := filepath.Join(firstEnvironment["MINOS_RUN_DIR"], "home")
	for _, value := range []string{
		"HOME=" + runHome,
		"CLAUDE_CONFIG_DIR=" + filepath.Join(runHome, ".claude"),
		"CODEX_HOME=" + filepath.Join(runHome, ".codex"),
	} {
		assertContainsFile(t, record+".env", value)
	}
	assertContainsFile(t, record+".argv", "claude-opus-5")

	state.changePullRequest(func(pullRequest map[string]any) { pullRequest["number"] = float64(2) })
	secondFacts := facts
	secondFacts.PR = "2"
	result, err = reconcilePullRequest(t.Context(), cfg, repo, secondFacts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "suppressed" || len(starts) != 1 {
		t.Fatalf("second admission while active = %q, starts = %d; want suppression", result, len(starts))
	}
	activeUnit = ""
	result, err = reconcilePullRequest(t.Context(), cfg, repo, secondFacts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "started" || len(starts) != 2 || !strings.Contains(activeUnit, "pr2") {
		t.Fatalf("second admission after release = %q, active = %q, starts = %d", result, activeUnit, len(starts))
	}
}

func TestRebuildEstateStopsNonCleanLead(t *testing.T) {
	runBody, record, environment := startEstateRunBody(t)
	environment["MINOS_TEST_NON_CLEAN_FINISH"] = "1"
	environment["MINOS_LEAD_SILENCE_TIMEOUT"] = "1"
	environment["MINOS_FAILURE_LOG"] = filepath.Join(environment["MINOS_RUN_DIR"], "failures.log")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, runBody)
	cmd.Env = environmentWithOverrides(environment)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("non-clean run body reached the outer timeout: %v\n%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("non-clean run body failed: %v\n%s", err, output)
	}

	assertContainsFile(t, filepath.Join(environment["MINOS_RUN_DIR"], "lead-complete"), "non-clean")
	assertContainsFile(t, environment["MINOS_FAILURE_LOG"], "stage=review cause=verdict-incomplete")
	assertContainsFile(t, record+".terminal", `"state":"done"`)
	assertContainsFile(t, record+".calls", "stop abcdef12")
	attemptsData, err := os.ReadFile(record + ".attempts")
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := strconv.Atoi(strings.TrimSpace(string(attemptsData)))
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("agent terminal observations = %d, want one before stopping the non-clean lead", attempts)
	}
}

func TestRebuildEstateSupervisesTerminalLeadWithoutCompletionMarker(t *testing.T) {
	runBody, record, environment := startEstateRunBody(t)
	environment["MINOS_TEST_UNMARKED_FINISH"] = "1"
	environment["MINOS_LEAD_SILENCE_TIMEOUT"] = "2"
	environment["MINOS_CLAUDE_POLL_SECONDS"] = "0.1"
	environment["MINOS_FAILURE_LOG"] = filepath.Join(environment["MINOS_RUN_DIR"], "failures.log")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, runBody)
	cmd.Env = environmentWithOverrides(environment)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("unmarked run body reached the outer timeout instead of its supervision timeout: %v\n%s", ctx.Err(), output)
	}
	if err == nil {
		t.Fatalf("unmarked terminal lead succeeded without a completion marker\n%s", output)
	}

	if _, statErr := os.Stat(filepath.Join(environment["MINOS_RUN_DIR"], "lead-complete")); !os.IsNotExist(statErr) {
		t.Fatalf("unmarked terminal lead wrote a completion marker or stat failed: %v", statErr)
	}
	assertContainsFile(t, environment["MINOS_FAILURE_LOG"], "stage=lead-supervision cause=Claude lead produced no run activity for 2 seconds (last state: done)")
	assertContainsFile(t, record+".terminal", `"state":"done"`)
	assertContainsFile(t, record+".calls", "stop abcdef12")
}

func startEstateRunBody(t *testing.T) (string, string, map[string]string) {
	t.Helper()
	codeRepository, head := createGitRepository(t, "AGENTS.md", "NON_CLEAN_TERMINAL_SENTINEL_719\n")
	annexeRepository := createGitRepositoryAtHead(t, "README.md", "# Estate commission\n")
	state := newForgejoFixtureState(t)
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["head"].(map[string]any)["sha"] = head
		pullRequest["head"].(map[string]any)["repo"].(map[string]any)["clone_url"] = codeRepository
		pullRequest["base"].(map[string]any)["sha"] = head
		pullRequest["base"].(map[string]any)["repo"].(map[string]any)["clone_url"] = codeRepository
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
	if result.Decision != "started" || len(startArguments) == 0 {
		t.Fatalf("admission = %q, systemd arguments = %v; want recorded start", result, startArguments)
	}
	return runBody, record, systemdEnvironment(t, startArguments)
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
	if result.Decision != "started" || len(startArguments) == 0 {
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

func TestRebuildEstateIncompleteStatusBindsTheHeadAndAnchorsThePinnedTarget(t *testing.T) {
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
		written["target_url"] != fixtureStatusTargetURL(cfg.Forges[facts.Forge].APIBase, Facts{
			Owner: facts.Owner, Repo: facts.Repo, PR: facts.PR, BaseSHA: target,
		}) {
		t.Fatalf("incomplete status writes = %d, payload = %#v", writes, written)
	}
	creator, ok := written["creator"].(map[string]any)
	if !ok || creator["login"] != "Minos" {
		t.Fatalf("incomplete status creator = %#v, want Minos", written["creator"])
	}

	// A status is a head-bound statement: a pinned target the branch has
	// since left behind still publishes, anchored to that pinned target, so
	// a mid-run target push cannot end the round.
	stdout.Reset()
	if err := ForgeCommand(t.Context(), []string{"status", head, target + "-stale", "incomplete"}, &stdout); err != nil {
		t.Fatalf("pinned-target status after target moved: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), `"outcome":"applied"`) {
		t.Fatalf("pinned-target status output = %q, want applied", stdout.String())
	}
	state.mu.Lock()
	writesAfterMove := state.statusWrites
	var movedAnchor any
	if len(state.statuses) > 1 {
		movedAnchor = state.statuses[0]["target_url"]
	}
	state.mu.Unlock()
	if writesAfterMove != 2 {
		t.Fatalf("pinned-target status write count = %d, want two", writesAfterMove)
	}
	wantMovedAnchor := fixtureStatusTargetURL(cfg.Forges[facts.Forge].APIBase, Facts{
		Owner: facts.Owner, Repo: facts.Repo, PR: facts.PR, BaseSHA: target + "-stale",
	})
	if movedAnchor != wantMovedAnchor {
		t.Fatalf("pinned-target status anchor = %#v, want %q", movedAnchor, wantMovedAnchor)
	}
}

func TestForgeStatusSkipsOnlyAnIdenticalDesiredStatus(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, facts := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_PR", facts.PR)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)

	head := state.headSHA()
	target := state.targetSHA()
	for _, desired := range []string{"working", "working", "attention"} {
		var stdout strings.Builder
		if err := ForgeCommand(t.Context(), []string{"status", head, target, desired}, &stdout); err != nil {
			t.Fatalf("status %q: %v\n%s", desired, err, stdout.String())
		}
	}
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["base"].(map[string]any)["sha"] = "new-target"
	})
	newTarget := state.targetSHA()
	var stdout strings.Builder
	if err := ForgeCommand(t.Context(), []string{"status", head, newTarget, "attention"}, &stdout); err != nil {
		t.Fatalf("status with new target: %v\n%s", err, stdout.String())
	}
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["head"].(map[string]any)["sha"] = "new-head"
	})
	newHead := state.headSHA()
	state.setStatuses(nil)
	stdout.Reset()
	if err := ForgeCommand(t.Context(), []string{"status", newHead, newTarget, "attention"}, &stdout); err != nil {
		t.Fatalf("status with new head: %v\n%s", err, stdout.String())
	}

	posts := state.statusPostFacts()
	if len(posts) != 4 {
		t.Fatalf("forge received %d status posts, want exactly four: %#v", len(posts), posts)
	}
	wantTarget := fixtureStatusTargetURL(cfg.Forges[facts.Forge].APIBase, Facts{
		Owner: facts.Owner, Repo: facts.Repo, PR: facts.PR, BaseSHA: target,
	})
	wantNewTarget := fixtureStatusTargetURL(cfg.Forges[facts.Forge].APIBase, Facts{
		Owner: facts.Owner, Repo: facts.Repo, PR: facts.PR, BaseSHA: newTarget,
	})
	for index, want := range []struct {
		head        string
		target      string
		state       string
		description string
	}{
		{head: head, target: wantTarget, state: "pending", description: "Reviewing changes"},
		{head: head, target: wantTarget, state: "failure", description: "Changes need attention"},
		{head: head, target: wantNewTarget, state: "failure", description: "Changes need attention"},
		{head: newHead, target: wantNewTarget, state: "failure", description: "Changes need attention"},
	} {
		post := posts[index]
		if post.Head != want.head || post.Payload["state"] != want.state || post.Payload["description"] != want.description ||
			post.Payload["context"] != "Minos" || post.Payload["target_url"] != want.target {
			t.Fatalf("forge status post %d = %#v, want %#v", index+1, post, want)
		}
	}
}

func TestRebuildEstateCleanReviewAddsCompletionReaction(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, facts := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)
	t.Setenv("MINOS_PR", facts.PR)

	for attempt := 1; attempt <= 2; attempt++ {
		if err := ForgeCommand(t.Context(), []string{"reaction", state.headSHA(), state.targetSHA(), "+1"}, &bytes.Buffer{}); err != nil {
			t.Fatalf("reaction attempt %d: %v", attempt, err)
		}
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.reactionWrites != 1 || !slices.Contains(state.reactions, "+1") {
		t.Fatalf("reactions = %v, writes = %d, want one +1 write", state.reactions, state.reactionWrites)
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
    test -f "$CLAUDE_CONFIG_DIR/.credentials.json"
    test -f "$CODEX_HOME/auth.json"
    printf '%s\n%s\n' 'claude-auth-present' 'codex-auth-present' >"$record.auth"
    guidance="$(jq -r '.guidance' "$MINOS_ORIENTATION")"
    cat "$guidance" >"$record.grounding"
    if [ "${MINOS_TEST_NON_CLEAN_FINISH:-}" = "1" ]; then
      printf 'timestamp=fixture pull_request=owner/repository#1 head=fixture stage=review cause=verdict-incomplete\n' \
        >>"$MINOS_FAILURE_LOG"
      printf 'non-clean\n' >"$MINOS_RUN_DIR/lead-complete"
    elif [ "${MINOS_TEST_UNMARKED_FINISH:-}" = "1" ]; then
      :
    else
      printf 'clean\n' >"$MINOS_RUN_DIR/lead-complete"
    fi
    printf 'Agent backgrounded: abcdef12\n'
    ;;
  agents)
    attempts=0
    [ ! -f "$record.attempts" ] || IFS= read -r attempts <"$record.attempts"
    attempts=$((attempts + 1))
    printf '%s\n' "$attempts" >"$record.attempts"
    printf '%s\n' '[{"id":"abcdef12","state":"done"}]' | tee "$record.terminal"
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
	claudeSeed := filepath.Join(root, "claude-seed")
	codexSeed := filepath.Join(root, "codex-seed")
	if err := os.MkdirAll(claudeSeed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexSeed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeSeed, ".credentials.json"), []byte("fixture Claude subscription state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexSeed, "auth.json"), []byte("fixture Codex ChatGPT state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(root, "skills")
	for _, casting := range []string{"claude-code", "codex"} {
		if err := os.MkdirAll(filepath.Join(skills, casting), 0o755); err != nil {
			t.Fatal(err)
		}
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
		"MINOS_CLAUDE":                claude,
		"MINOS_LEAD_MODEL":            "claude-opus-5",
		"MINOS_CLAUDE_CONFIG_SEED":    claudeSeed,
		"MINOS_CODEX_CONFIG_SEED":     codexSeed,
		"MINOS_LIFECYCLE_INSTRUCTION": instruction,
		"MINOS_REVIEW_WORKFLOW":       "/opt/minos/workflows/adjudicated-review",
		"MINOS_SKILLS_DIR":            skills,
		"MINOS_SETUP_WORKSPACE":       setup,
		"MINOS_BIN":                   "/usr/local/bin/minos",
		"MINOS_CLAUDE_POLL_SECONDS":   "0",
		"MINOS_TEST_RECORD":           record,
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
