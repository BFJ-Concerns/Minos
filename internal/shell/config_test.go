package shell

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadServiceConfigDefaultsAndDuration(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(`
[forges.local]
adaptation = "/tmp/adapt"
api-base = "http://forgejo.local"
webhook-secret-file = "/tmp/secret"
credential-file = "/tmp/token"

[sweep]
liveness-threshold = "5m"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listener.Bind != ":8919" {
		t.Fatalf("unexpected default bind %s", cfg.Listener.Bind)
	}
	if cfg.Sweep.LivenessThreshold.Duration != 5*time.Minute {
		t.Fatalf("unexpected threshold %s", cfg.Sweep.LivenessThreshold.Duration)
	}
}
