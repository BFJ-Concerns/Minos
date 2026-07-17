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
	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	runDir := filepath.Join(root, "run")
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

	instructionPath := filepath.Join(root, "lifecycle.md")
	if err := os.WriteFile(instructionPath, []byte("Follow the Minos lifecycle exactly.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(root, "anthropic.token")
	if err := os.WriteFile(credentialPath, []byte("fake-proxy-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(root, "claude-record")
	claudeStub := filepath.Join(root, "claude")
	writeScript(t, claudeStub, `#!/usr/bin/env sh
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
    printf 'agents\n' >>"$record.calls"
    printf '%s\n' '[{"id":"abcdef12","sessionId":"session-one","status":"idle","state":"done"}]'
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
	t.Cleanup(func() { killRecordedProcesses(record + ".pids") })

	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runBodyEnv := map[string]string{
		"MINOS_CLAUDE":                               claudeStub,
		"MINOS_LEAD_MODEL":                           "gpt-5.6-sol",
		"MINOS_ANTHROPIC_CREDENTIAL_FILE":            credentialPath,
		"ANTHROPIC_BASE_URL":                         "http://proxy.test:8317",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":              "gpt-5.6-luna",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS":             "265000",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW":            "265000",
		"MINOS_LIFECYCLE_INSTRUCTION":                instructionPath,
		"MINOS_REVIEW_WORKFLOW":                      "/opt/minos/workflows/review.js",
		"MINOS_ROOT_CAUSE_SKILL":                     skillSource,
		"MINOS_BIN":                                  "/usr/local/bin/minos",
	}
	var config strings.Builder
	for name, value := range runBodyEnv {
		fmt.Fprintf(&config, "%s=%s\n", name, strconv.Quote(value))
	}
	if err := os.WriteFile(filepath.Join(configRoot, "run-body.env"), []byte(config.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	runEnv := map[string]string{
		"MINOS_RUN_DIR":             runDir,
		"MINOS_CONFIG":              configRoot,
		"MINOS_FORGE":               "forgejo",
		"MINOS_WORKSPACE":           filepath.Join(runDir, "workspace"),
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
		"MINOS_TEST_RECORD":         record,
		"MINOS_CLAUDE_POLL_SECONDS": "0",
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

	configDir := filepath.Join(runDir, "claude-config")
	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("CLAUDE_CONFIG_DIR mode = %o, want 700", info.Mode().Perm())
	}
	assertContainsFile(t, filepath.Join(configDir, "skills", "root-cause", "SKILL.md"), "# Root Cause")
	assertContainsFile(t, record+".argv", "--bg")
	assertContainsFile(t, record+".argv", "--model")
	assertContainsFile(t, record+".argv", "gpt-5.6-sol")
	assertContainsFile(t, record+".argv", "Follow the Minos lifecycle exactly.")
	assertContainsFile(t, record+".calls", "agents")
	assertContainsFile(t, record+".calls", "stop abcdef12")

	for _, value := range []string{
		"CLAUDE_CONFIG_DIR=" + configDir,
		"ANTHROPIC_AUTH_TOKEN=fake-proxy-token",
		"ANTHROPIC_BASE_URL=http://proxy.test:8317",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=gpt-5.6-luna",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS=265000",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW=265000",
		"FORCE_COLOR=0",
		"PWD=" + runDir,
		"MINOS_RUN_DIR=" + runDir,
		"MINOS_CONFIG=" + configRoot,
		"MINOS_FORGE=forgejo",
		"MINOS_WORKSPACE=" + filepath.Join(runDir, "workspace"),
		"MINOS_OWNER=owner",
		"MINOS_REPO_NAME=repository",
		"MINOS_PR=17",
		"MINOS_LIFECYCLE_INSTRUCTION=" + instructionPath,
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
		assertContainsFile(t, record+".env", value)
	}
	stdin, err := os.ReadFile(record + ".stdin")
	if err != nil {
		t.Fatal(err)
	}
	if len(stdin) != 0 {
		t.Fatalf("Claude stdin = %q, want empty because instruction is the background prompt", stdin)
	}

	pids := recordedPIDs(t, record+".pids")
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
		if err := syscall.Kill(pid, 0); err == nil {
			return true
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
