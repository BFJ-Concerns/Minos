package shell

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVendoredEnsembleDiscoversClaudeTranscriptFromExplicitSeed(t *testing.T) {
	seed := os.Getenv("MINOS_TEST_CLAUDE_CONFIG_SEED")
	if seed == "" {
		t.Skip("live transcript probe not exercised: MINOS_TEST_CLAUDE_CONFIG_SEED is not set")
	}
	seed = filepath.Clean(seed)
	if !filepath.IsAbs(seed) {
		t.Fatal("MINOS_TEST_CLAUDE_CONFIG_SEED must be an absolute path")
	}
	refuseOperatorToolHome(t, seed)
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("live transcript probe not exercised: claude CLI is not installed")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("live transcript probe not exercised: node is not installed")
	}

	root := t.TempDir()
	home := filepath.Join(root, "home")
	claudeConfig := filepath.Join(home, ".claude")
	codexConfig := filepath.Join(home, ".codex")
	projectsDir := filepath.Join(claudeConfig, "projects")
	for _, path := range []string{claudeConfig, codexConfig, projectsDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	copySeedDirectory(t, seed, claudeConfig)
	state := []byte("{\"hasCompletedOnboarding\":true,\"bypassPermissionsModeAccepted\":true}\n")
	if err := os.WriteFile(filepath.Join(claudeConfig, ".claude.json"), state, 0o600); err != nil {
		t.Fatal(err)
	}

	workflow := filepath.Join(root, "transcript-probe.js")
	source := `export const meta = {
  name: "minos-transcript-discovery-probe",
  description: "Confirm that a Claude worker result is recovered from its transcript"
};

const answer = await agent(
  "Reply with exactly MINOS_TRANSCRIPT_PROBE_OK and no other text.",
  { engine: "claude", model: "claude-opus-5", label: "transcript-probe" }
);
return { answer };
`
	if err := os.WriteFile(workflow, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := filepath.Abs(filepath.Join("..", "..", "runtime", "ensemble.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	recordDir := filepath.Join(root, "records")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.CommandContext(t.Context(), "node", bundle, workflow)
	cmd.Dir = root
	cmd.Env = transcriptProbeEnvironment(home, recordDir)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("vendored Ensemble transcript probe failed: %v\n%s", err, stderr.String())
	}
	var result struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode transcript probe result: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if result.Answer != "MINOS_TRANSCRIPT_PROBE_OK" {
		t.Fatalf("transcript probe answer = %q, want exact marker", result.Answer)
	}

	foundTranscript := false
	if err := filepath.WalkDir(projectsDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			foundTranscript = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !foundTranscript {
		t.Fatalf("Claude result returned but no transcript appeared beneath %s", projectsDir)
	}
}

func transcriptProbeEnvironment(home, recordDir string) []string {
	blocked := func(name string) bool {
		if name == "HOME" || name == "CLAUDE_CONFIG_DIR" || name == "CODEX_HOME" {
			return true
		}
		for _, prefix := range []string{"ANTHROPIC_", "CLAUDE_CODE_", "ENSEMBLE_", "XDG_"} {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		}
		return false
	}
	environment := make([]string, 0, len(os.Environ())+7)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if !blocked(name) {
			environment = append(environment, value)
		}
	}
	return append(environment,
		"HOME="+home,
		"CLAUDE_CONFIG_DIR="+filepath.Join(home, ".claude"),
		"CODEX_HOME="+filepath.Join(home, ".codex"),
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"ENSEMBLE_RUN_RECORD=on",
		"ENSEMBLE_RUN_RECORD_DIR="+recordDir,
	)
}

func refuseOperatorToolHome(t *testing.T, seed string) {
	t.Helper()
	info, err := os.Lstat(seed)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("live transcript seed root must not be a symlink: %s", seed)
	}
	operatorHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	resolvedSeed := resolvedPath(seed)
	for _, forbidden := range []string{
		filepath.Join(operatorHome, ".claude"),
		filepath.Join(operatorHome, ".codex"),
	} {
		if pathIsWithin(resolvedSeed, filepath.Clean(forbidden)) {
			t.Fatalf("live transcript seed must not use the operator tool home: %s", seed)
		}
	}
}

func resolvedPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func pathIsWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func copySeedDirectory(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("test seed contains unsupported symlink: %s", path)
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}
