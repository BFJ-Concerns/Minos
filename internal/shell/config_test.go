package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadServiceConfigDuration(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(`
[listener]
bind = ":8919"

[forges.local]
adaptation = "/tmp/adapt"
api-base = "http://forgejo.local"
webhook-secret-file = "/tmp/secret"
credential-file = "/tmp/token"

	[runs]
	dir = "/tmp/runs"
	max-concurrent = 2

[sweep]
liveness-threshold = "5m"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sweep.LivenessThreshold.Duration != 5*time.Minute {
		t.Fatalf("unexpected threshold %s", cfg.Sweep.LivenessThreshold.Duration)
	}
	if cfg.Runs.MaxConcurrent != 2 {
		t.Fatalf("max concurrent = %d, want 2", cfg.Runs.MaxConcurrent)
	}
}

func TestLoadServiceConfigRejectsUnknownKeys(t *testing.T) {
	root := writeServiceConfig(t, validServiceConfig+"\nquiet-typo = true\n")
	_, err := LoadServiceConfig(root)
	if err == nil || !strings.Contains(err.Error(), "quiet-typo") {
		t.Fatalf("error = %v, want unknown key named", err)
	}
}

func TestLoadServiceConfigRejectsMissingRequiredFields(t *testing.T) {
	root := writeServiceConfig(t, strings.Replace(validServiceConfig, "bind = \":8919\"\n", "", 1))
	_, err := LoadServiceConfig(root)
	if err == nil || !strings.Contains(err.Error(), "listener.bind") {
		t.Fatalf("error = %v, want missing listener.bind", err)
	}
}

func TestLoadServiceConfigRequiresPositiveMaxConcurrent(t *testing.T) {
	for _, value := range []string{"", "max-concurrent = 0\n", "max-concurrent = -1\n"} {
		config := strings.Replace(validServiceConfig, "max-concurrent = 2\n", value, 1)
		root := writeServiceConfig(t, config)
		_, err := LoadServiceConfig(root)
		if err == nil || !strings.Contains(err.Error(), "runs.max-concurrent") {
			t.Fatalf("value %q error = %v, want runs.max-concurrent", value, err)
		}
	}
}

func TestLoadRepoConfigsRejectsUnknownKeys(t *testing.T) {
	root := writeRepoConfig(t, validRepoConfig+"\nquiet-typo = true\n")
	_, err := LoadRepoConfigs(root)
	if err == nil || !strings.Contains(err.Error(), "quiet-typo") {
		t.Fatalf("error = %v, want unknown key named", err)
	}
}

func TestLoadRepoConfigsRejectsMissingRequiredFields(t *testing.T) {
	root := writeRepoConfig(t, strings.Replace(validRepoConfig, "owner = \"pump19\"\n", "", 1))
	_, err := LoadRepoConfigs(root)
	if err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("error = %v, want missing owner", err)
	}
}

func TestShippedConfigsLoadClean(t *testing.T) {
	projectRoot := filepath.Join("..", "..")
	for _, root := range []string{
		filepath.Join(projectRoot, "examples", "config"),
		filepath.Join(projectRoot, "deploy", "etc", "pump19"),
	} {
		if _, err := LoadServiceConfig(root); err != nil {
			t.Errorf("load %s/service.toml: %v", root, err)
		}
	}

	if _, err := LoadRepoConfigs(filepath.Join(projectRoot, "examples", "config")); err != nil {
		t.Errorf("load example repository config: %v", err)
	}
	deployExample, err := os.ReadFile(filepath.Join(projectRoot, "deploy", "etc", "pump19", "repos", "owner--repository.toml.example"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRepoConfigs(writeRepoConfig(t, string(deployExample))); err != nil {
		t.Errorf("load deployment repository skeleton: %v", err)
	}
}

func writeServiceConfig(t *testing.T, contents string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeRepoConfig(t *testing.T, contents string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "repos", "subject.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

const validServiceConfig = `[listener]
bind = ":8919"

[forges.local]
adaptation = "/tmp/adapt"
api-base = "http://forgejo.local"
webhook-secret-file = "/tmp/secret"
credential-file = "/tmp/token"

[runs]
dir = "/tmp/runs"
max-concurrent = 2

[sweep]
liveness-threshold = "5m"
`

const validRepoConfig = `forge = "local"
owner = "pump19"
repo = "subject"

[adaptation]
build = "go build ./..."
test = "go test ./..."
skill = "/tmp/review-skill"

[[trigger]]
run = "review"
on = ["pr-opened"]
`
