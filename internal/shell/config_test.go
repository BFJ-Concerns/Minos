package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bfj/minos/internal/findings"
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

func TestRepoFindingPolicyDefaultsAndInclusiveThresholds(t *testing.T) {
	repos, err := LoadRepoConfigs(writeRepoConfig(t, validRepoConfig))
	if err != nil {
		t.Fatal(err)
	}
	policy := repos[0].FindingPolicy()
	if policy.PublishThreshold != findings.P1 || policy.RepairThreshold != findings.P3 || policy.Mode != findings.PublishThroughP3Mode {
		t.Fatalf("resolved policy=%#v", policy)
	}

	configured := strings.Replace(validRepoConfig, "[finding-disposition]", "[policy]\npublish-threshold = \"P0\"\nrepair-threshold = \"P2\"\n\n[finding-disposition]", 1)
	repos, err = LoadRepoConfigs(writeRepoConfig(t, configured))
	if err != nil || repos[0].Policy.PublishThreshold != findings.P0 || repos[0].Policy.RepairThreshold != findings.P2 {
		t.Fatalf("configured policy=%#v err=%v", repos, err)
	}
}

func TestRepoFindingPolicyRejectsMissingModeAndNarrowRepair(t *testing.T) {
	missing := strings.Replace(validRepoConfig, "\n[finding-disposition]\nmode = \"publish-through-p3\"\n", "", 1)
	if _, err := LoadRepoConfigs(writeRepoConfig(t, missing)); err == nil || !strings.Contains(err.Error(), "invalid finding policy") {
		t.Fatalf("missing mode error=%v", err)
	}

	narrow := strings.Replace(validRepoConfig, "[finding-disposition]", "[policy]\npublish-threshold = \"P2\"\nrepair-threshold = \"P1\"\n\n[finding-disposition]", 1)
	if _, err := LoadRepoConfigs(writeRepoConfig(t, narrow)); err == nil || !strings.Contains(err.Error(), "narrower") {
		t.Fatalf("narrow repair error=%v", err)
	}
}

func TestDestinationModeRequiresConfiguredDestination(t *testing.T) {
	service := validServiceConfig + `
[finding-destinations.backlog]
adaptation = "/tmp/destination"
endpoint = "fixture://backlog"
credential-file = "/tmp/destination-token"
expected-principal = "minos-service"
`
	root := writeServiceConfig(t, service)
	if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := strings.Replace(validRepoConfig, "mode = \"publish-through-p3\"", "mode = \"destination\"\ndestination = \"backlog\"\ntarget = \"owner/repo\"", 1)
	if err := os.WriteFile(filepath.Join(root, "repos", "subject.toml"), []byte(repo), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfiguredRepos(cfg); err != nil {
		t.Fatalf("configured destination rejected: %v", err)
	}

	unknown := strings.Replace(repo, "destination = \"backlog\"", "destination = \"missing\"", 1)
	if err := os.WriteFile(filepath.Join(root, "repos", "subject.toml"), []byte(unknown), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfiguredRepos(cfg); err == nil || !strings.Contains(err.Error(), "unknown finding destination") {
		t.Fatalf("unknown destination error=%v", err)
	}
}

func TestDispositionModesRejectFieldsFromOtherMode(t *testing.T) {
	fallbackWithDestination := strings.Replace(validRepoConfig, "mode = \"publish-through-p3\"", "mode = \"publish-through-p3\"\ndestination = \"backlog\"\ntarget = \"owner/repo\"", 1)
	if _, err := LoadRepoConfigs(writeRepoConfig(t, fallbackWithDestination)); err == nil || !strings.Contains(err.Error(), "forbids") {
		t.Fatalf("fallback fields error=%v", err)
	}
	destinationWithoutTarget := strings.Replace(validRepoConfig, "mode = \"publish-through-p3\"", "mode = \"destination\"\ndestination = \"backlog\"", 1)
	if _, err := LoadRepoConfigs(writeRepoConfig(t, destinationWithoutTarget)); err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("destination fields error=%v", err)
	}
}

func TestShippedConfigsLoadClean(t *testing.T) {
	projectRoot := filepath.Join("..", "..")
	var shipped []ServiceConfig
	for _, root := range []string{
		filepath.Join(projectRoot, "examples", "config"),
		filepath.Join(projectRoot, "deploy", "etc", "minos"),
	} {
		config, err := LoadServiceConfig(root)
		if err != nil {
			t.Errorf("load %s/service.toml: %v", root, err)
			continue
		}
		shipped = append(shipped, config)
	}
	if len(shipped) == 2 && shipped[0].Runs.MaxConcurrent != shipped[1].Runs.MaxConcurrent {
		t.Errorf("shipped runs.max-concurrent values disagree: example=%d deployment=%d", shipped[0].Runs.MaxConcurrent, shipped[1].Runs.MaxConcurrent)
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

func TestLifecycleSmokeCarriesRequiredServiceBotLogin(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "e2e", "lifecycle-smoke.sh"))
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

[finding-disposition]
mode = "publish-through-p3"

[[eligibility]]
authors = ["*"]
`
