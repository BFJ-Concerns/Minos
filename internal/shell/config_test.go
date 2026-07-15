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
