package shell

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunBodyLaunchesAndStopsIsolatedResidentClaude(t *testing.T) {
	fixture := newRunBodyFixture(t)
	fixture.run(t, nil)

	homeDir := filepath.Join(fixture.runDir, "home")
	configDir := filepath.Join(homeDir, ".claude")
	codexConfigDir := filepath.Join(homeDir, ".codex")
	projectsDir := filepath.Join(homeDir, ".claude", "projects")
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
	assertRegularFile(t, filepath.Join(configDir, ".credentials.json"))
	assertRegularFile(t, filepath.Join(codexConfigDir, "auth.json"))
	if info, err := os.Stat(projectsDir); err != nil || !info.IsDir() {
		t.Fatalf("control-plane transcript directory = %s: %v", projectsDir, err)
	}
	if filepath.Dir(projectsDir) != configDir {
		t.Fatalf("control-plane transcript directory %q is not inside seeded Claude config %q", projectsDir, configDir)
	}
	assertContainsFile(t, fixture.record+".setup", "setup invoked")
	assertContainsFile(t, fixture.record+".argv", "--bg")
	assertContainsFile(t, fixture.record+".argv", "--model")
	assertContainsFile(t, fixture.record+".argv", "claude-opus-4-8")
	assertContainsFile(t, fixture.record+".argv", "Follow the Minos lifecycle exactly.")
	assertContainsFile(t, fixture.record+".calls", "agents")
	assertContainsFile(t, fixture.record+".calls", "stop abcdef12")

	for _, value := range []string{
		"HOME=" + homeDir,
		"CLAUDE_CONFIG_DIR=" + configDir,
		"CODEX_HOME=" + codexConfigDir,
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
		"MINOS_REVIEW_WORKFLOW=/opt/minos/workflows/adjudicated-review",
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
	assertContainsFile(t, fixture.record+".auth", "claude-auth-present")
	assertContainsFile(t, fixture.record+".auth", "codex-auth-present")
	assertContainsFile(t, fixture.record+".projects", projectsDir)
	assertFileOmitsEnvironmentNames(t, fixture.record+".env", []string{
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW",
		"MINOS_ANTHROPIC_CREDENTIAL_FILE",
	})
	if after := snapshotDirectory(t, fixture.ambientHome); !reflect.DeepEqual(after, fixture.ambientHomeBefore) {
		t.Fatalf("ambient home changed\nbefore: %#v\nafter:  %#v", fixture.ambientHomeBefore, after)
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

func TestVendoredEnsembleBundleRunsWithoutRepositoryDependencies(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	bundle, err := filepath.Abs(filepath.Join("..", "..", "runtime", "ensemble.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cmd := exec.Command("node", bundle, "workflows")
	cmd.Dir = root
	cmd.Env = environmentWithOverrides(map[string]string{
		"HOME":              filepath.Join(root, "home"),
		"CLAUDE_CONFIG_DIR": filepath.Join(root, "home", ".claude"),
		"CODEX_HOME":        filepath.Join(root, "home", ".codex"),
		"XDG_CONFIG_HOME":   filepath.Join(root, "home", ".config"),
		"XDG_DATA_HOME":     filepath.Join(root, "home", ".local", "share"),
	})
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("vendored Ensemble bundle is not self-contained: %v\n%s", err, output)
	}
}

func TestInstallReviewRuntimeVerifiesLauncherAndInstallsSiblingArtefacts(t *testing.T) {
	sourceRoot := t.TempDir()
	for _, directory := range []string{"scripts", "runtime", "workflows"} {
		if err := os.MkdirAll(filepath.Join(sourceRoot, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyFixtureFile(t, filepath.Join("..", "..", "scripts", "install-review-runtime"), filepath.Join(sourceRoot, "scripts", "install-review-runtime"), 0o755)
	for _, name := range []string{"ensemble.mjs", "ensemble.mjs.sha256", "ensemble.source-version"} {
		copyFixtureFile(t, filepath.Join("..", "..", "runtime", name), filepath.Join(sourceRoot, "runtime", name), 0o644)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", "adjudicated-review"), []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", "run-record-adjudicator.mjs"), []byte("export function adjudicate() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "opt", "minos")
	cmd := exec.Command(filepath.Join(sourceRoot, "scripts", "install-review-runtime"), destination)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install review runtime: %v\n%s", err, output)
	}
	for _, path := range []string{
		filepath.Join(destination, "runtime", "ensemble.mjs"),
		filepath.Join(destination, "runtime", "ensemble.mjs.sha256"),
		filepath.Join(destination, "runtime", "ensemble.source-version"),
		filepath.Join(destination, "workflows", "adjudicated-review"),
		filepath.Join(destination, "workflows", "run-record-adjudicator.mjs"),
	} {
		assertRegularFile(t, path)
	}
	info, err := os.Stat(filepath.Join(destination, "workflows", "adjudicated-review"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("installed adjudication wrapper mode = %o, want 755", info.Mode().Perm())
	}
	installed, err := os.ReadFile(filepath.Join(destination, "runtime", "ensemble.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(installed)); got != "16815cd1c2a512d2361fcde94fd10df3212cadc5f706221400d37b8367e3305c" {
		t.Fatalf("installed Ensemble digest = %s", got)
	}
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
	root              string
	configRoot        string
	runDir            string
	record            string
	instructionPath   string
	claudeStub        string
	setupStub         string
	ambientHome       string
	ambientHomeBefore map[string]string
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
		ambientHome:     filepath.Join(root, "ambient-home"),
	}
	for _, path := range []string{
		filepath.Join(fixture.ambientHome, ".claude", "operator-state"),
		filepath.Join(fixture.ambientHome, ".codex", "operator-state"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("must remain byte-unchanged\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture.ambientHomeBefore = snapshotDirectory(t, fixture.ambientHome)
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
    test -f "$CLAUDE_CONFIG_DIR/.credentials.json"
    test -f "$CODEX_HOME/auth.json"
    printf '%s\n%s\n' 'claude-auth-present' 'codex-auth-present' >"$record.auth"
    printf '%s\n' "$HOME/.claude/projects" >"$record.projects"
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
		"MINOS_CLAUDE":                fixture.claudeStub,
		"MINOS_LEAD_MODEL":            "claude-opus-4-8",
		"MINOS_GIT_AUTHOR_NAME":       "Minos",
		"MINOS_GIT_AUTHOR_EMAIL":      "minos@example.invalid",
		"MINOS_CLAUDE_CONFIG_SEED":    claudeSeed,
		"MINOS_CODEX_CONFIG_SEED":     codexSeed,
		"MINOS_LIFECYCLE_INSTRUCTION": fixture.instructionPath,
		"MINOS_REVIEW_WORKFLOW":       "/opt/minos/workflows/adjudicated-review",
		"MINOS_ROOT_CAUSE_SKILL":      skillSource,
		"MINOS_SETUP_WORKSPACE":       fixture.setupStub,
		"MINOS_BIN":                   "/usr/local/bin/minos",
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
		"MINOS_RUN_DIR":                 f.runDir,
		"MINOS_CONFIG":                  f.configRoot,
		"MINOS_FORGE":                   "forgejo",
		"MINOS_WORKSPACE":               filepath.Join(f.runDir, "workspace"),
		"MINOS_ORIENTATION":             filepath.Join(f.runDir, "orientation.json"),
		"MINOS_OWNER":                   "owner",
		"MINOS_REPO_NAME":               "repository",
		"MINOS_PR":                      "17",
		"MINOS_HEAD_SHA":                "head-sha",
		"MINOS_TARGET_SHA":              "target-sha",
		"MINOS_BASE_REF":                "main",
		"MINOS_HEAD_BRANCH":             "feature",
		"MINOS_API_BASE":                "http://forge.test",
		"MINOS_CREDENTIAL_FILE":         "/etc/minos/forge.token",
		"MINOS_BUILD_CMD":               "make build",
		"MINOS_TEST_CMD":                "make test",
		"MINOS_AUTO_MERGE":              "true",
		"MINOS_RUN_BODY":                "/opt/minos/run-body/run-body",
		"MINOS_TEST_RECORD":             f.record,
		"MINOS_CLAUDE_POLL_SECONDS":     "0",
		"HOME":                          f.ambientHome,
		"CLAUDE_CONFIG_DIR":             filepath.Join(f.ambientHome, ".claude"),
		"CODEX_HOME":                    filepath.Join(f.ambientHome, ".codex"),
		"ANTHROPIC_AUTH_TOKEN":          "obsolete-proxy-token-must-not-reach-lead",
		"ANTHROPIC_BASE_URL":            "http://obsolete-proxy.invalid",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "obsolete-haiku-alias",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS":             "265000",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW":            "265000",
		"MINOS_ANTHROPIC_CREDENTIAL_FILE":            filepath.Join(f.ambientHome, "obsolete.token"),
	}
	for name, value := range extraEnv {
		runEnv[name] = value
	}
	script := filepath.Join("..", "..", "scripts", "run-body", "run-body")
	cmd := exec.Command(script)
	cmd.Env = environmentWithOverrides(runEnv)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run-body failed: %v\n%s", err, out)
	}
}

func assertRegularFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular file", path)
	}
}

func copyFixtureFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, mode); err != nil {
		t.Fatal(err)
	}
}

func assertFileOmitsEnvironmentNames(t *testing.T, path string, names []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for _, name := range names {
		prefix := name + "="
		for _, line := range lines {
			if strings.HasPrefix(line, prefix) {
				t.Fatalf("%s contains retired environment name %s", path, name)
			}
		}
	}
}

func snapshotDirectory(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry := fmt.Sprintf("%s:%o", info.Mode().Type(), info.Mode().Perm())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += fmt.Sprintf(":%x", sha256.Sum256(data))
		}
		snapshot[relative] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
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
