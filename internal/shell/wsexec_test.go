package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScrubEnvRemovesOnlyConfiguredVariables(t *testing.T) {
	got := scrubEnv([]string{
		"MINOS_FORGE_TOKEN=secret",
		"MODEL_KEY=secret",
		"PATH=/bin",
	}, []string{"MINOS_FORGE_TOKEN", "MODEL_KEY"})
	want := []string{"PATH=/bin"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("scrubbed env = %#v, want %#v", got, want)
	}
}

func TestWorkspaceExecCommandRunsInWorkspaceWithScrubbedEnvironment(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceExecConfig(t, root)
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINOS_WORKSPACE", workspace)
	t.Setenv("MINOS_FORGE_TOKEN", "secret")
	t.Setenv("MODEL_KEY", "secret")
	cwdFile := filepath.Join(root, "cwd.txt")
	envFile := filepath.Join(root, "env.txt")
	err := WorkspaceExecCommand(t.Context(), []string{
		"--config", root,
		"sh", "-c", "pwd >\"$1\"; env >\"$2\"", "sh", cwdFile, envFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.ReadFile(cwdFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(cwd)) != workspace {
		t.Fatalf("child cwd = %q, want %q", strings.TrimSpace(string(cwd)), workspace)
	}
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), "MINOS_FORGE_TOKEN=") || strings.Contains(string(env), "MODEL_KEY=") {
		t.Fatalf("child environment was not scrubbed:\n%s", env)
	}
}

func TestWorkspaceExecCommandRefusesMissingWorkspace(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceExecConfig(t, root)
	t.Setenv("MINOS_WORKSPACE", filepath.Join(root, "missing"))
	err := WorkspaceExecCommand(t.Context(), []string{"--config", root, "true"})
	if err == nil {
		t.Fatal("expected missing workspace to fail")
	}
}

func writeWorkspaceExecConfig(t *testing.T, root string) {
	t.Helper()
	data := validServiceConfig + `
[scrub]
vars = ["MINOS_FORGE_TOKEN", "MODEL_KEY"]
`
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
