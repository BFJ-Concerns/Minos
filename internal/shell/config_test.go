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
[service]
bot-login = "Minos"

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

func TestLoadServiceConfigRequiresBotLogin(t *testing.T) {
	root := writeServiceConfig(t, strings.Replace(validServiceConfig, "[service]\nbot-login = \"Minos\"\n\n", "", 1))
	_, err := LoadServiceConfig(root)
	if err == nil || !strings.Contains(err.Error(), "service.bot-login") {
		t.Fatalf("error = %v, want missing service.bot-login", err)
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
	root := writeRepoConfig(t, strings.Replace(validRepoConfig, "owner = \"minos-e2e-owner\"\n", "", 1))
	_, err := LoadRepoConfigs(root)
	if err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("error = %v, want missing owner", err)
	}
}

func TestLoadRepoConfigsAcceptsAbsentFindIngest(t *testing.T) {
	root := writeRepoConfig(t, validRepoConfig)
	repos, err := LoadRepoConfigs(root)
	if err != nil {
		t.Fatal(err)
	}
	if repos[0].FindIngest != nil {
		t.Fatalf("find ingest = %#v, want unconfigured", repos[0].FindIngest)
	}
}

func TestLoadRepoConfigsRequiresCompleteSafeFindIngest(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{name: "missing repository", config: "\n[find-ingest]\npath = \"ISSUES.md\"\n", want: "find-ingest.repository"},
		{name: "missing path", config: "\n[find-ingest]\nrepository = \"BFJ-Concerns/Minos-Annexe\"\n", want: "find-ingest.path"},
		{name: "malformed repository", config: "\n[find-ingest]\nrepository = \"Minos-Annexe\"\npath = \"ISSUES.md\"\n", want: "find-ingest.repository"},
		{name: "absolute path", config: "\n[find-ingest]\nrepository = \"BFJ-Concerns/Minos-Annexe\"\npath = \"/tmp/ISSUES.md\"\n", want: "find-ingest.path"},
		{name: "escaping path", config: "\n[find-ingest]\nrepository = \"BFJ-Concerns/Minos-Annexe\"\npath = \"../ISSUES.md\"\n", want: "find-ingest.path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeRepoConfig(t, validRepoConfig+tt.config)
			_, err := LoadRepoConfigs(root)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestLoadRepoConfigsAcceptsConfiguredFindIngest(t *testing.T) {
	root := writeRepoConfig(t, validRepoConfig+`
[find-ingest]
repository = "BFJ-Concerns/Minos-Annexe"
path = "logs/finds.md"
`)
	repos, err := LoadRepoConfigs(root)
	if err != nil {
		t.Fatal(err)
	}
	if repos[0].FindIngest == nil || repos[0].FindIngest.Repository != "BFJ-Concerns/Minos-Annexe" || repos[0].FindIngest.Path != "logs/finds.md" {
		t.Fatalf("find ingest = %#v", repos[0].FindIngest)
	}
}

func TestRepoConfigAllowsCIToCarryBuildAndTestGate(t *testing.T) {
	config := strings.Replace(validRepoConfig, "build = \"go build ./...\"\ntest = \"go test ./...\"\n", "", 1)
	config += "\n[ci]\nrequired-checks = [\"ci/build-and-test\"]\n"
	if _, err := LoadRepoConfigs(writeRepoConfig(t, config)); err != nil {
		t.Fatalf("CI-carried repository config was rejected: %v", err)
	}
}

func TestRepoConfigWithoutRequiredCIMustSupplyBuildAndTest(t *testing.T) {
	config := strings.Replace(validRepoConfig, "build = \"go build ./...\"\ntest = \"go test ./...\"\n", "", 1)
	_, err := LoadRepoConfigs(writeRepoConfig(t, config))
	if err == nil || !strings.Contains(err.Error(), "adaptation.build, adaptation.test") {
		t.Fatalf("error = %v, want missing build and test commands", err)
	}
}

func TestRepoConfigRejectsBlankRequiredCheck(t *testing.T) {
	config := validRepoConfig + "\n[ci]\nrequired-checks = [\"\"]\n"
	_, err := LoadRepoConfigs(writeRepoConfig(t, config))
	if err == nil || !strings.Contains(err.Error(), "ci.required-checks[0]") {
		t.Fatalf("error = %v, want blank required check named", err)
	}
}

func TestShippedConfigsLoadClean(t *testing.T) {
	projectRoot := filepath.Join("..", "..")
	for _, root := range []string{
		filepath.Join(projectRoot, "examples", "config"),
		filepath.Join(projectRoot, "deploy", "etc", "minos"),
	} {
		if _, err := LoadServiceConfig(root); err != nil {
			t.Errorf("load %s/service.toml: %v", root, err)
		}
	}

	if _, err := LoadRepoConfigs(filepath.Join(projectRoot, "examples", "config")); err != nil {
		t.Errorf("load example repository config: %v", err)
	}
	deployExample, err := os.ReadFile(filepath.Join(projectRoot, "deploy", "etc", "minos", "repos", "owner--repository.toml.example"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRepoConfigs(writeRepoConfig(t, string(deployExample))); err != nil {
		t.Errorf("load deployment repository skeleton: %v", err)
	}
}

func TestForgejoSmokeCarriesRequiredServiceBotLogin(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "e2e", "forgejo-smoke.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "[service]\nbot-login = \"Minos\"") {
		t.Fatal("Forgejo e2e service.toml omits required service.bot-login")
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

const validServiceConfig = `[service]
bot-login = "Minos"

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
`

const validRepoConfig = `forge = "local"
owner = "minos-e2e-owner"
repo = "subject"

[adaptation]
build = "go build ./..."
test = "go test ./..."
skill = "/tmp/review-skill"

[[eligibility]]
authors = ["*"]
`
