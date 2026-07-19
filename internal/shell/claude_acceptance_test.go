package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRealClaudeRequiresSeededBypassAcceptance(t *testing.T) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI is not installed")
	}

	unseeded := filepath.Join(t.TempDir(), "claude-config")
	if err := os.Mkdir(unseeded, 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := runAcceptanceProbe(t, claude, unseeded)
	if err == nil {
		t.Fatalf("unseeded Claude config accepted bypass mode:\n%s", output)
	}
	if !strings.Contains(output, "requires accepting the disclaimer first") {
		t.Fatalf("unseeded Claude failure did not report the bypass disclaimer:\n%s", output)
	}

	seeded := filepath.Join(t.TempDir(), "claude-config")
	if err := os.Mkdir(seeded, 0o700); err != nil {
		t.Fatal(err)
	}
	state := []byte("{\"hasCompletedOnboarding\":true,\"bypassPermissionsModeAccepted\":true}\n")
	if err := os.WriteFile(filepath.Join(seeded, ".claude.json"), state, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err = runAcceptanceProbe(t, claude, seeded)
	if err != nil {
		t.Fatalf("seeded Claude config refused bypass mode: %v\n%s", err, output)
	}
	sessionID := regexp.MustCompile(`(?i)backgrounded[^[:xdigit:]]*([[:xdigit:]]{6,})`).FindStringSubmatch(output)
	if len(sessionID) != 2 {
		t.Fatalf("seeded Claude config passed the disclaimer but did not report a session ID:\n%s", output)
	}
	stopAcceptanceProbe(t, claude, seeded, sessionID[1])
}

func runAcceptanceProbe(t *testing.T, claude, configDir string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, claude,
		"--bg",
		"--dangerously-skip-permissions",
		"--model", "anthropic-gpt-5.6-sol",
		"Acceptance bootstrap probe; do not perform any work.",
	)
	cmd.Env = acceptanceProbeEnvironment(configDir)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("Claude acceptance probe timed out:\n%s", output)
	}
	return string(output), err
}

func stopAcceptanceProbe(t *testing.T, claude, configDir, sessionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, claude, "stop", sessionID)
	cmd.Env = acceptanceProbeEnvironment(configDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stop Claude acceptance probe: %v\n%s", err, output)
	}
}

func acceptanceProbeEnvironment(configDir string) []string {
	names := map[string]bool{
		"CLAUDE_CONFIG_DIR":                        true,
		"ANTHROPIC_API_KEY":                        true,
		"ANTHROPIC_AUTH_TOKEN":                     true,
		"ANTHROPIC_BASE_URL":                       true,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": true,
	}
	environment := make([]string, 0, len(os.Environ())+5)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if !names[name] {
			environment = append(environment, value)
		}
	}
	return append(environment,
		"CLAUDE_CONFIG_DIR="+configDir,
		"ANTHROPIC_API_KEY=minos-acceptance-probe",
		"ANTHROPIC_AUTH_TOKEN=minos-acceptance-probe",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	)
}
