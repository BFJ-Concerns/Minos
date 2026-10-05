package shell

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BFJ-Concerns/Minos/deploy"
)

// The installed units are the shipped ones rendered for the deployment:
// the runs slice carries the configured envelope and every service names
// the configuration root it was installed from.

func unitsTestConfig(t *testing.T, envelopeGiB string) ServiceConfig {
	t.Helper()
	root := t.TempDir()
	contents := strings.Replace(testServiceConfig, "max-concurrent = 2\n", "max-concurrent = 2\nmemory-envelope-gib = "+envelopeGiB+"\n", 1)
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestInstallUnitsRendersTheRunsSliceFromTheConfiguredEnvelope(t *testing.T) {
	cfg := unitsTestConfig(t, "8")
	directory := filepath.Join(t.TempDir(), "systemd", "user")
	var report bytes.Buffer
	if err := installUnits(cfg, directory, &report); err != nil {
		t.Fatalf("install units: %v", err)
	}

	shipped, err := fs.ReadDir(deploy.Units, "systemd/user")
	if err != nil {
		t.Fatal(err)
	}
	if len(shipped) < 5 {
		t.Fatalf("shipped units = %d, want the receiver, sweep, timer, alert and slice", len(shipped))
	}
	for _, entry := range shipped {
		installed, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatalf("%s was not installed: %v", entry.Name(), err)
		}
		if !strings.Contains(report.String(), filepath.Join(directory, entry.Name())+"\n") {
			t.Fatalf("report omits %s:\n%s", entry.Name(), report.String())
		}
		original, _ := fs.ReadFile(deploy.Units, "systemd/user/"+entry.Name())
		switch {
		case entry.Name() == runsSliceUnit:
			if !strings.Contains(string(installed), "MemoryHigh=6G\nMemoryMax=8G\n") {
				t.Fatalf("installed slice does not carry the configured envelope:\n%s", installed)
			}
			if strings.Contains(string(installed), "22G") {
				t.Fatalf("installed slice still carries the shipped default:\n%s", installed)
			}
		case strings.HasSuffix(entry.Name(), ".service"):
			if !strings.Contains(string(installed), "--config "+cfg.Root) || strings.Contains(string(installed), "--config /etc/minos") {
				t.Fatalf("installed %s does not name the configuration root %s:\n%s", entry.Name(), cfg.Root, installed)
			}
		default:
			if !bytes.Equal(installed, original) {
				t.Fatalf("installed %s differs from the shipped unit", entry.Name())
			}
		}
	}
}

func TestInstallUnitsReplacesAnEditedUnitOnReinstall(t *testing.T) {
	cfg := unitsTestConfig(t, "22")
	directory := t.TempDir()
	if err := installUnits(cfg, directory, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	slice := filepath.Join(directory, runsSliceUnit)
	if err := os.WriteFile(slice, []byte("[Slice]\nMemoryMax=1G\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installUnits(cfg, directory, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(slice)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installed), "MemoryHigh=20G\nMemoryMax=22G\n") {
		t.Fatalf("reinstall left the edited slice in place:\n%s", installed)
	}
	if _, err := os.Stat(slice + ".next"); !os.IsNotExist(err) {
		t.Fatalf("staging file survived the install: %v", err)
	}
}

func TestInstallUnitsKeepsTheHighWatermarkAboveZeroForATinyEnvelope(t *testing.T) {
	rendered, err := renderRunsSlice([]byte("[Slice]\nMemoryHigh=20G\nMemoryMax=22G\n"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(rendered) != "[Slice]\nMemoryHigh=1G\nMemoryMax=2G\n" {
		t.Fatalf("rendered = %q", rendered)
	}
	if _, err := renderRunsSlice([]byte("[Slice]\nMemoryMax=22G\n"), 8); err == nil || !strings.Contains(err.Error(), "MemoryHigh") {
		t.Fatalf("a slice without a MemoryHigh line rendered: %v", err)
	}
}

func TestInstallUnitsRefusesAServiceWithoutTheConfigMarker(t *testing.T) {
	cfg := unitsTestConfig(t, "8")
	if _, err := renderUnit("minos-extra.service", []byte("[Service]\nExecStart=/usr/local/bin/minos extra\n"), cfg); err == nil || !strings.Contains(err.Error(), "--config /etc/minos") {
		t.Fatalf("a service without the configuration root rendered: %v", err)
	}
	rendered, err := renderUnit("minos-extra.service", []byte("ExecStart=/usr/local/bin/minos extra --config /etc/minos\n"), cfg)
	if err != nil || string(rendered) != "ExecStart=/usr/local/bin/minos extra --config "+cfg.Root+"\n" {
		t.Fatalf("rendered = %q, %v", rendered, err)
	}
}

func TestInstallUnitsQuotesAConfigurationRootSystemdWouldSplit(t *testing.T) {
	cfg := unitsTestConfig(t, "8")
	cfg.Root = `/srv/minos "config" dir`
	rendered, err := renderUnit("minos-sweep.service", []byte("ExecStart=/usr/local/bin/minos sweep --config /etc/minos\n"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := "ExecStart=/usr/local/bin/minos sweep --config \"/srv/minos \\\"config\\\" dir\"\n"
	if string(rendered) != want {
		t.Fatalf("rendered = %q, want %q", rendered, want)
	}
	if systemdArgument("/etc/minos") != "/etc/minos" {
		t.Fatal("a plain path was quoted")
	}
}

func TestInstallUnitsCommandReadsTheConfigRootAndRefusesBadUsage(t *testing.T) {
	cfg := unitsTestConfig(t, "12")
	directory := t.TempDir()
	var stdout bytes.Buffer
	if err := InstallUnitsCommand([]string{"--config", cfg.Root, directory}, &stdout); err != nil {
		t.Fatalf("install-units: %v", err)
	}
	installed, err := os.ReadFile(filepath.Join(directory, runsSliceUnit))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installed), "MemoryHigh=10G\nMemoryMax=12G\n") {
		t.Fatalf("installed slice = %s", installed)
	}
	for _, args := range [][]string{{}, {"--config", cfg.Root}, {directory, "extra"}} {
		if err := InstallUnitsCommand(args, &stdout); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("args %v: error = %v, want usage", args, err)
		}
	}
	occupied := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(occupied, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallUnitsCommand([]string{"--config", cfg.Root, occupied}, &stdout); err == nil {
		t.Fatal("installing into a file succeeded")
	}
}
