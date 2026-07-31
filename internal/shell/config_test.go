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
`

func TestLoadServiceConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(testServiceConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil || cfg.Service.BotLogin != "Minos" || cfg.Runs.Dir != "/tmp/runs" {
		t.Fatalf("config = %#v, error = %v", cfg, err)
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

func TestLoadRepoConfigDefaultsReviewLoopKnobs(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "forge = \"local\"\nowner = \"owner\"\nrepo = \"repo\"\n[adaptation]\nrun-body = \"/tmp/run-body\"\n"
	if err := os.WriteFile(filepath.Join(root, "repos", "repo.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	repos, err := LoadRepoConfigs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Review.Threshold != "High" || repos[0].Review.MaximumRounds != 0 {
		t.Fatalf("review defaults = %+v", repos)
	}
}

func TestLoadRepoConfigValidatesReviewLoopKnobs(t *testing.T) {
	for _, test := range []struct {
		name   string
		review string
		want   string
	}{
		{name: "threshold", review: "threshold = \"Urgent\"", want: "review.threshold"},
		{name: "maximum rounds", review: "maximum-rounds = -1", want: "review.maximum-rounds"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
				t.Fatal(err)
			}
			contents := "forge = \"local\"\nowner = \"owner\"\nrepo = \"repo\"\n[adaptation]\nrun-body = \"/tmp/run-body\"\n[review]\n" + test.review + "\n"
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
