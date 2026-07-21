package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunBodyLaunchesAndStopsIsolatedResidentClaude(t *testing.T) {
	fixture := newRunBodyFixture(t)
	fixture.run(t, nil)

	configDir := filepath.Join(fixture.runDir, "claude-config")
	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("CLAUDE_CONFIG_DIR mode = %o, want 700", info.Mode().Perm())
	}
	assertContainsFile(t, filepath.Join(configDir, "skills", "root-cause", "SKILL.md"), "# Root Cause")
	assertContainsFile(t, filepath.Join(configDir, ".claude.json"), `"hasCompletedOnboarding":true`)
	assertContainsFile(t, filepath.Join(configDir, ".claude.json"), `"bypassPermissionsModeAccepted":true`)
	assertContainsFile(t, fixture.record+".setup", "setup invoked")
	assertContainsFile(t, fixture.record+".argv", "--bg")
	assertContainsFile(t, fixture.record+".argv", "--model")
	assertContainsFile(t, fixture.record+".argv", "anthropic-gpt-5.6-sol")
	assertContainsFile(t, fixture.record+".argv", "Follow the Minos lifecycle exactly.")
	assertContainsFile(t, fixture.record+".calls", "agents")
	assertContainsFile(t, fixture.record+".calls", "stop abcdef12")

	for _, value := range []string{
		"CLAUDE_CONFIG_DIR=" + configDir,
		"ANTHROPIC_AUTH_TOKEN=fake-proxy-token",
		"ANTHROPIC_BASE_URL=http://proxy.test:8317",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=anthropic-gpt-5.6-terra",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS=265000",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW=265000",
		"FORCE_COLOR=0",
		"PWD=" + fixture.runDir,
		"MINOS_RUN_DIR=" + fixture.runDir,
		"MINOS_CONFIG=" + fixture.configRoot,
		"MINOS_FORGE=forgejo",
		"MINOS_WORKSPACE=" + filepath.Join(fixture.runDir, "workspace"),
		"MINOS_ORIENTATION=" + filepath.Join(fixture.runDir, "orientation.json"),
		"MINOS_GIT_AUTHOR_NAME=Minos",
		"MINOS_GIT_AUTHOR_EMAIL=minos@example.invalid",
		"GIT_AUTHOR_NAME=Minos",
		"GIT_AUTHOR_EMAIL=minos@example.invalid",
		"GIT_COMMITTER_NAME=Minos",
		"GIT_COMMITTER_EMAIL=minos@example.invalid",
		"MINOS_OWNER=owner",
		"MINOS_REPO_NAME=repository",
		"MINOS_PR=17",
		"MINOS_LIFECYCLE_INSTRUCTION=" + fixture.instructionPath,
		"MINOS_REVIEW_WORKFLOW=/opt/minos/workflows/review.js",
		"MINOS_BIN=/usr/local/bin/minos",
		"MINOS_HEAD_SHA=head-sha",
		"MINOS_TARGET_SHA=target-sha",
		"MINOS_BASE_REF=main",
		"MINOS_HEAD_BRANCH=feature",
		"MINOS_API_BASE=http://forge.test",
		"MINOS_CREDENTIAL_FILE=/etc/minos/forge.token",
		"MINOS_BUILD_CMD=make build",
		"MINOS_TEST_CMD=make test",
		"MINOS_AUTO_MERGE=true",
	} {
		assertContainsFile(t, fixture.record+".env", value)
	}
	stdin, err := os.ReadFile(fixture.record + ".stdin")
	if err != nil {
		t.Fatal(err)
	}
	if len(stdin) != 0 {
		t.Fatalf("Claude stdin = %q, want empty because instruction is the background prompt", stdin)
	}

	fixture.assertProcessesStopped(t)
}

func TestRunBodyRecognisesRealClaudeTerminalStates(t *testing.T) {
	for _, state := range []string{"done", "blocked", "failed", "stopped"} {
		t.Run(state, func(t *testing.T) {
			fixture := newRunBodyFixture(t)
			fixture.run(t, map[string]string{"MINOS_TEST_TERMINAL_STATE": state})
			assertContainsFile(t, fixture.record+".terminal", `"status":null`)
			assertContainsFile(t, fixture.record+".terminal", `"state":"`+state+`"`)
			assertContainsFile(t, fixture.record+".calls", "stop abcdef12")
			fixture.assertProcessesStopped(t)
		})
	}
}

func TestRunBodyRetriesTransientAgentQuery(t *testing.T) {
	fixture := newRunBodyFixture(t)
	fixture.run(t, map[string]string{"MINOS_TEST_FAIL_AGENTS_ONCE": "1"})

	calls, err := os.ReadFile(fixture.record + ".calls")
	if err != nil {
		t.Fatal(err)
	}
	if string(calls) != "agents failed\nagents\nstop abcdef12\n" {
		t.Fatalf("Claude calls = %q, want one failed query followed by recovery and stop", calls)
	}
	assertContainsFile(t, fixture.record+".attempts", "2")
	fixture.assertProcessesStopped(t)
}

func TestProcessStatusIsZombie(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   bool
	}{
		{name: "zombie", status: "Name:\tsleep\nState:\tZ (zombie)\n", want: true},
		{name: "running", status: "Name:\tsleep\nState:\tR (running)\n", want: false},
		{name: "missing state", status: "Name:\tsleep\n", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := processStatusIsZombie([]byte(test.status)); got != test.want {
				t.Fatalf("processStatusIsZombie() = %t, want %t", got, test.want)
			}
		})
	}
}

type runBodyFixture struct {
	root            string
	configRoot      string
	runDir          string
	record          string
	instructionPath string
	claudeStub      string
	setupStub       string
}

func newRunBodyFixture(t *testing.T) runBodyFixture {
	t.Helper()
	root := t.TempDir()
	fixture := runBodyFixture{
		root: root, configRoot: filepath.Join(root, "config"),
		runDir: filepath.Join(root, "run"), record: filepath.Join(root, "claude-record"),
		instructionPath: filepath.Join(root, "lifecycle.md"),
		claudeStub:      filepath.Join(root, "claude"),
		setupStub:       filepath.Join(root, "setup-workspace"),
	}
	skillSource := filepath.Join(root, "root-cause")
	if err := os.MkdirAll(filepath.Join(skillSource, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillSource, "SKILL.md"), []byte("# Root Cause\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillSource, "references", "proof.md"), []byte("proof\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.instructionPath, []byte("Follow the Minos lifecycle exactly.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(root, "anthropic.token")
	if err := os.WriteFile(credentialPath, []byte("fake-proxy-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeScript(t, fixture.claudeStub, `#!/usr/bin/env sh
set -eu
record="${MINOS_TEST_RECORD:?}"
case "$1" in
  --bg)
    : >"$record.argv"
    for argument in "$@"; do
      printf '%s\n' "$argument" >>"$record.argv"
    done
    env | sort >"$record.env"
    cat >"$record.stdin"
    sleep 300 </dev/null >/dev/null 2>&1 &
    lead_pid=$!
    sleep 300 </dev/null >/dev/null 2>&1 &
    task_pid=$!
    printf '%s\n%s\n' "$lead_pid" "$task_pid" >"$record.pids"
    printf 'Agent backgrounded: abcdef12\n'
    ;;
  agents)
    attempts=0
    if [ -f "$record.attempts" ]; then
      IFS= read -r attempts <"$record.attempts"
    fi
    attempts=$((attempts + 1))
    printf '%s\n' "$attempts" >"$record.attempts"
    if [ "${MINOS_TEST_FAIL_AGENTS_ONCE:-}" = "1" ] && [ "$attempts" -eq 1 ]; then
      printf 'agents failed\n' >>"$record.calls"
      exit 1
    fi
    printf 'agents\n' >>"$record.calls"
    terminal_state="${MINOS_TEST_TERMINAL_STATE:-done}"
    printf '[{"id":"abcdef12","sessionId":"session-one","status":null,"state":"%s"}]\n' "$terminal_state" |
      tee "$record.terminal"
    ;;
  stop)
    printf 'stop %s\n' "$2" >>"$record.calls"
    while IFS= read -r pid; do
      kill "$pid" 2>/dev/null || true
    done <"$record.pids"
    ;;
  *)
    printf 'unexpected command: %s\n' "$*" >&2
    exit 1
    ;;
esac
`)
	writeScript(t, fixture.setupStub, `#!/usr/bin/env sh
set -eu
mkdir -p "$MINOS_WORKSPACE"
printf '%s\n' '{"grounding":"repository","reason":"annexe-not-found"}' >"$MINOS_ORIENTATION"
printf 'setup invoked\n' >"${MINOS_TEST_RECORD}.setup"
`)
	t.Cleanup(func() { killRecordedProcesses(fixture.record + ".pids") })

	if err := os.MkdirAll(fixture.configRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runBodyEnv := map[string]string{
		"MINOS_CLAUDE":                               fixture.claudeStub,
		"MINOS_LEAD_MODEL":                           "anthropic-gpt-5.6-sol",
		"MINOS_GIT_AUTHOR_NAME":                      "Minos",
		"MINOS_GIT_AUTHOR_EMAIL":                     "minos@example.invalid",
		"MINOS_ANTHROPIC_CREDENTIAL_FILE":            credentialPath,
		"ANTHROPIC_BASE_URL":                         "http://proxy.test:8317",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":              "anthropic-gpt-5.6-terra",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS":             "265000",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW":            "265000",
		"MINOS_LIFECYCLE_INSTRUCTION":                fixture.instructionPath,
		"MINOS_REVIEW_WORKFLOW":                      "/opt/minos/workflows/review.js",
		"MINOS_ROOT_CAUSE_SKILL":                     skillSource,
		"MINOS_SETUP_WORKSPACE":                      fixture.setupStub,
		"MINOS_BIN":                                  "/usr/local/bin/minos",
	}
	var config strings.Builder
	for name, value := range runBodyEnv {
		fmt.Fprintf(&config, "%s=%s\n", name, strconv.Quote(value))
	}
	if err := os.WriteFile(filepath.Join(fixture.configRoot, "run-body.env"), []byte(config.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f runBodyFixture) run(t *testing.T, extraEnv map[string]string) {
	t.Helper()
	runEnv := map[string]string{
		"MINOS_RUN_DIR":             f.runDir,
		"MINOS_CONFIG":              f.configRoot,
		"MINOS_FORGE":               "forgejo",
		"MINOS_WORKSPACE":           filepath.Join(f.runDir, "workspace"),
		"MINOS_ORIENTATION":         filepath.Join(f.runDir, "orientation.json"),
		"MINOS_OWNER":               "owner",
		"MINOS_REPO_NAME":           "repository",
		"MINOS_PR":                  "17",
		"MINOS_HEAD_SHA":            "head-sha",
		"MINOS_TARGET_SHA":          "target-sha",
		"MINOS_BASE_REF":            "main",
		"MINOS_HEAD_BRANCH":         "feature",
		"MINOS_API_BASE":            "http://forge.test",
		"MINOS_CREDENTIAL_FILE":     "/etc/minos/forge.token",
		"MINOS_BUILD_CMD":           "make build",
		"MINOS_TEST_CMD":            "make test",
		"MINOS_AUTO_MERGE":          "true",
		"MINOS_RUN_BODY":            "/opt/minos/run-body/run-body",
		"MINOS_TEST_RECORD":         f.record,
		"MINOS_CLAUDE_POLL_SECONDS": "0",
	}
	for name, value := range extraEnv {
		runEnv[name] = value
	}
	script := filepath.Join("..", "..", "scripts", "run-body", "run-body")
	cmd := exec.Command(script)
	cmd.Env = os.Environ()
	for name, value := range runEnv {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run-body failed: %v\n%s", err, out)
	}
}

func (f runBodyFixture) assertProcessesStopped(t *testing.T) {
	t.Helper()
	pids := recordedPIDs(t, f.record+".pids")
	deadline := time.Now().Add(2 * time.Second)
	for processesExist(pids) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processesExist(pids) {
		t.Fatalf("resident process tree still exists after stop: %v", pids)
	}
}

func recordedPIDs(t *testing.T, path string) []int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	return pids
}

func processesExist(pids []int) bool {
	for _, pid := range pids {
		if processExists(pid) {
			return true
		}
	}
	return false
}

func processExists(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return true
	}
	return !processStatusIsZombie(status)
}

func processStatusIsZombie(status []byte) bool {
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "State:" {
			return fields[1] == "Z"
		}
	}
	return false
}

func killRecordedProcesses(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}
