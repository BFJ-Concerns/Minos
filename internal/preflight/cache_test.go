package preflight

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCachedGateReusesFreshResultAndExpiresIt(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "preflight.toml")
	writePreflightConfig(t, config)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	runs := 0
	gate := CachedGate{
		ConfigPath: config, CachePath: filepath.Join(root, "cache.json"), TTL: 5 * time.Minute,
		Now: func() time.Time { return now },
		Run: func(context.Context, Config) Report {
			runs++
			return Report{Passed: true, Results: []Result{{Name: "test", Passed: true}}}
		},
	}
	first, cached, err := gate.Check(t.Context())
	if err != nil || cached || !first.Passed || runs != 1 {
		t.Fatalf("first Check() report=%#v cached=%v runs=%d err=%v", first, cached, runs, err)
	}
	second, cached, err := gate.Check(t.Context())
	if err != nil || !cached || !second.Passed || runs != 1 {
		t.Fatalf("second Check() report=%#v cached=%v runs=%d err=%v", second, cached, runs, err)
	}
	now = now.Add(6 * time.Minute)
	_, cached, err = gate.Check(t.Context())
	if err != nil || cached || runs != 2 {
		t.Fatalf("expired Check() cached=%v runs=%d err=%v", cached, runs, err)
	}
}

func TestCachedGateInvalidatesWhenConfigurationChanges(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "preflight.toml")
	writePreflightConfig(t, config)
	runs := 0
	gate := CachedGate{
		ConfigPath: config, CachePath: filepath.Join(root, "cache.json"), TTL: time.Hour,
		Run: func(context.Context, Config) Report { runs++; return Report{Passed: true} },
	}
	if _, _, err := gate.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(config, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n# deployment change\n"); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, cached, err := gate.Check(t.Context()); err != nil || cached || runs != 2 {
		t.Fatalf("changed Check() cached=%v runs=%d err=%v", cached, runs, err)
	}
}

func TestCachedGateCollapsesConcurrentMisses(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "preflight.toml")
	writePreflightConfig(t, config)
	var runs atomic.Int64
	gate := CachedGate{
		ConfigPath: config, CachePath: filepath.Join(root, "cache.json"), TTL: time.Hour,
		Run: func(context.Context, Config) Report {
			runs.Add(1)
			time.Sleep(50 * time.Millisecond)
			return Report{Passed: true}
		},
	}
	const callers = 8
	start := make(chan struct{})
	errors := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			report, _, err := gate.Check(t.Context())
			if err == nil && !report.Passed {
				err = context.Canceled
			}
			errors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent Check(): %v", err)
		}
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("preflight probes = %d, want one", got)
	}
}

func writePreflightConfig(t *testing.T, path string) {
	t.Helper()
	data := `[forge]
api-base = "http://forge.invalid/api/v1"
credential-file = "/secret"
expected-login = "Minos"
permission-repository = "owner/repo"
required-permissions = ["pull"]

[[engine]]
name = "test"
command = "engine"
args = ["--model", "{model}"]
models = ["pinned"]

[registry]
enabled = false

[alert]
directory = "/incidents"

[toolchain]
binaries = ["minos"]
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
