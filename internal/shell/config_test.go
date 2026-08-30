package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testServiceConfig = `[service]
bot-login = "Minos"
[listener]
bind = ":8919"
[forges.local]
adaptation = "/tmp/adapt"
api-base = "http://forge.local"
webhook-secret-file = "/tmp/secret"
credential-file = "/tmp/token"
[runs]
dir = "/tmp/runs"
failure-log = "/tmp/failures.log"
max-concurrent = 2
[ensemble]
concurrency-claude = 10
concurrency-codex = 6
`

func TestLoadServiceConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(testServiceConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil || cfg.Service.BotLogin != "Minos" || cfg.Runs.Dir != "/tmp/runs" || cfg.Runs.FailureLog != "/tmp/failures.log" || cfg.Runs.MaxConcurrent != 2 || cfg.Ensemble.ConcurrencyClaude != 10 || cfg.Ensemble.ConcurrencyCodex != 6 {
		t.Fatalf("config = %#v, error = %v", cfg, err)
	}
}

func TestLoadServiceConfigDefaultsMaxConcurrentToOne(t *testing.T) {
	root := t.TempDir()
	contents := strings.Replace(testServiceConfig, "max-concurrent = 2\n", "", 1)
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil || cfg.MaxConcurrentRuns() != 1 {
		t.Fatalf("max-concurrent = %d, error = %v; want one live run", cfg.MaxConcurrentRuns(), err)
	}
}

func TestLoadServiceConfigRejectsUnusableMaxConcurrent(t *testing.T) {
	for _, setting := range []string{"max-concurrent = -1\n", "max-concurrent = 23\n"} {
		t.Run(strings.TrimSpace(setting), func(t *testing.T) {
			root := t.TempDir()
			contents := strings.Replace(testServiceConfig, "max-concurrent = 2\n", setting, 1)
			if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadServiceConfig(root)
			if err == nil || !strings.Contains(err.Error(), "max-concurrent") {
				t.Fatalf("error = %v, want max-concurrent validation", err)
			}
		})
	}
}

func TestLoadServiceConfigRequiresPositiveEnsembleConcurrency(t *testing.T) {
	for _, setting := range []string{
		"concurrency-claude = 0\nconcurrency-codex = 6\n",
		"concurrency-claude = 10\nconcurrency-codex = 0\n",
	} {
		t.Run(strings.TrimSpace(setting), func(t *testing.T) {
			root := t.TempDir()
			contents := strings.Replace(testServiceConfig,
				"concurrency-claude = 10\nconcurrency-codex = 6\n", setting, 1)
			if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadServiceConfig(root)
			if err == nil || !strings.Contains(err.Error(), "ensemble concurrency") {
				t.Fatalf("error = %v, want ensemble concurrency validation", err)
			}
		})
	}
}

func TestLoadServiceConfigDefaultsEnsembleConcurrency(t *testing.T) {
	root := t.TempDir()
	contents := strings.Replace(testServiceConfig,
		"[ensemble]\nconcurrency-claude = 10\nconcurrency-codex = 6\n", "", 1)
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil || cfg.Ensemble.ConcurrencyClaude != 2 || cfg.Ensemble.ConcurrencyCodex != 2 {
		t.Fatalf("ensemble defaults = %+v, error = %v", cfg.Ensemble, err)
	}
}

func TestLoadServiceConfigRejectsUnknownKeys(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(testServiceConfig+"unknown = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadServiceConfig(root)
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRepoConfigDecodesWorkInProgressBranchPrefixes(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "forge = \"local\"\nowner = \"owner\"\nrepo = \"repo\"\nwork-in-progress-branch-prefixes = [\"structural/\"]\n[adaptation]\nrun-body = \"/tmp/run-body\"\n"
	if err := os.WriteFile(filepath.Join(root, "repos", "repo.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	repos, err := LoadRepoConfigs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || len(repos[0].WorkInProgressBranchPrefixes) != 1 || repos[0].WorkInProgressBranchPrefixes[0] != "structural/" {
		t.Fatalf("work-in-progress prefixes = %+v", repos)
	}
}

func TestLoadRepoConfigRejectsEmptyWorkInProgressBranchPrefix(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "forge = \"local\"\nowner = \"owner\"\nrepo = \"repo\"\nwork-in-progress-branch-prefixes = [\"\"]\n[adaptation]\nrun-body = \"/tmp/run-body\"\n"
	if err := os.WriteFile(filepath.Join(root, "repos", "repo.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRepoConfigs(root)
	if err == nil || !strings.Contains(err.Error(), "work-in-progress-branch-prefixes") {
		t.Fatalf("error = %v, want empty prefix validation", err)
	}
}

func TestLoadRepoConfigDefaultsReviewThreshold(t *testing.T) {
	for _, test := range []struct {
		name          string
		block         string
		wantThreshold string
	}{
		{name: "unset", wantThreshold: "High"},
		{
			name:          "explicit threshold survives",
			block:         "[review]\nthreshold = \"Medium\"\n",
			wantThreshold: "Medium",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
				t.Fatal(err)
			}
			contents := "forge = \"local\"\nowner = \"owner\"\nrepo = \"repo\"\n[adaptation]\nrun-body = \"/tmp/run-body\"\n" + test.block
			if err := os.WriteFile(filepath.Join(root, "repos", "repo.toml"), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			repos, err := LoadRepoConfigs(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(repos) != 1 {
				t.Fatalf("repos = %d, want 1", len(repos))
			}
			if repos[0].Review.Threshold != test.wantThreshold {
				t.Fatalf("threshold = %q, want %q", repos[0].Review.Threshold, test.wantThreshold)
			}
		})
	}
}

func TestLoadRepoConfigValidatesReviewThreshold(t *testing.T) {
	for _, test := range []struct {
		name  string
		block string
		want  string
	}{
		{name: "threshold", block: "[review]\nthreshold = \"Urgent\"", want: "review.threshold"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
				t.Fatal(err)
			}
			contents := "forge = \"local\"\nowner = \"owner\"\nrepo = \"repo\"\n[adaptation]\nrun-body = \"/tmp/run-body\"\n" + test.block + "\n"
			if err := os.WriteFile(filepath.Join(root, "repos", "repo.toml"), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadRepoConfigs(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
