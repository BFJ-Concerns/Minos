package shell

import (
	"os"
	"path/filepath"
	"testing"
)

// The configuration set under deploy/etc/minos is what an operator copies to
// /etc/minos; its service.toml and repository example must load as shipped,
// with the example renamed to the .toml the loader globs.
func TestShippedConfigurationSetLoads(t *testing.T) {
	shipped := filepath.Join("..", "..", "deploy", "etc", "minos")
	root := t.TempDir()
	service, err := os.ReadFile(filepath.Join(shipped, "service.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "service.toml"), service, 0o644); err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile(filepath.Join(shipped, "repos", "owner--repository.toml.example"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "repos", "owner--repository.toml"), example, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatalf("shipped service.toml: %v", err)
	}
	repos, err := LoadRepoConfigs(cfg)
	if err != nil {
		t.Fatalf("shipped repository example: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("repositories = %d, want the shipped example alone", len(repos))
	}
	if _, ok := cfg.Forges[repos[0].Forge]; !ok {
		t.Fatalf("repository example names forge %q, which the shipped service.toml does not define", repos[0].Forge)
	}
}
