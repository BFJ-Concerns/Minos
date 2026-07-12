package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShippedConfigLoadsAfterPlaceholdersAreSubstituted(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "etc", "minos", "preflight.toml"))
	if err != nil {
		t.Fatal(err)
	}
	replacements := map[string]string{
		"REPLACE_WITH_FORGEJO_BASE_URL":  "https://forgejo.example.invalid",
		"REPLACE_WITH_FORGEJO_BOT_LOGIN": "minos",
		"REPLACE_WITH_OWNER":             "owner",
		"REPLACE_WITH_REPOSITORY":        "repository",
	}
	contents := string(data)
	for old, replacement := range replacements {
		contents = strings.ReplaceAll(contents, old, replacement)
	}
	path := filepath.Join(t.TempDir(), "preflight.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Engines) != 1 || cfg.Forge.ExpectedLogin != "minos" || len(Probes(cfg)) != 5 {
		t.Fatalf("unexpected shipped config: %#v", cfg)
	}
}

func TestEngineCommandMustBindPinnedModel(t *testing.T) {
	var cfg Config
	cfg.Forge.APIBase = "https://forgejo.example.invalid/api/v1"
	cfg.Forge.CredentialFile = "/secret"
	cfg.Forge.ExpectedLogin = "minos"
	cfg.Forge.PermissionRepository = "owner/repo"
	cfg.Forge.RequiredPermissions = []string{"push"}
	cfg.Engines = []EngineConfig{{Name: "claude", Command: "claude", Args: []string{"--print"}, Models: []string{"pinned"}}}
	cfg.Alert.Directory = "/incidents"
	cfg.Toolchain.Binaries = []string{"git"}
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "args must contain {model}") {
		t.Fatalf("model-independent engine command accepted: %v", err)
	}
}
