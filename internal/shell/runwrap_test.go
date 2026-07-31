package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCommandRefusesUnownedRunDirectory(t *testing.T) {
	runDir := t.TempDir()
	bodyRan := filepath.Join(t.TempDir(), "body-ran")
	body := filepath.Join(t.TempDir(), "body")
	writeScript(t, body, "#!/bin/sh\ntouch \"$BODY_RAN\"\n")
	t.Setenv("MINOS_RUN_DIR", runDir)
	t.Setenv("MINOS_RUN_BODY", body)
	t.Setenv("BODY_RAN", bodyRan)

	err := RunCommand(t.Context(), nil)
	if err == nil {
		t.Fatal("RunCommand accepted an unowned run directory")
	}
	if !strings.Contains(err.Error(), "does not own run directory") {
		t.Fatalf("RunCommand error = %q, want ownership refusal", err)
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("unowned run directory was removed: %v", err)
	}
	if _, err := os.Stat(bodyRan); !os.IsNotExist(err) {
		t.Fatalf("nested run body started or stat failed unexpectedly: %v", err)
	}
}

func TestRunCommandRemovesOwnedRunDirectory(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		marker    string
		handoff   bool
		wantError bool
		wantKept  bool
	}{
		{name: "body succeeds", body: "#!/bin/sh\nexit 0\n"},
		{name: "body fails", body: "#!/bin/sh\nexit 1\n", wantError: true},
		{name: "continuation with handoff", body: "#!/bin/sh\nexit 0\n", marker: "continuation\n", handoff: true, wantKept: true},
		{name: "continuation without handoff", body: "#!/bin/sh\nexit 0\n", marker: "continuation\n"},
		{name: "clean with handoff", body: "#!/bin/sh\nexit 0\n", marker: "clean\n", handoff: true},
		{name: "non-clean with handoff", body: "#!/bin/sh\nexit 0\n", marker: "non-clean\n", handoff: true},
		{name: "garbage with handoff", body: "#!/bin/sh\nexit 0\n", marker: "garbage\n", handoff: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runDir := filepath.Join(t.TempDir(), "run")
			if err := os.Mkdir(runDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(runDir, runOwnerMarker), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if !tt.wantKept {
				readOnlyDir := filepath.Join(runDir, "cache", "go", "modules", "example.test", "module@v1.0.0")
				if err := os.MkdirAll(readOnlyDir, 0o700); err != nil {
					t.Fatal(err)
				}
				readOnlyFile := filepath.Join(readOnlyDir, "module.go")
				if err := os.WriteFile(readOnlyFile, []byte("package module\n"), 0o400); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(readOnlyDir, 0o500); err != nil {
					t.Fatal(err)
				}
			}
			if tt.marker != "" {
				if err := os.WriteFile(filepath.Join(runDir, "lead-complete"), []byte(tt.marker), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			handoff := filepath.Join(t.TempDir(), "handoff.json")
			if tt.handoff {
				if err := os.WriteFile(handoff, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			body := filepath.Join(t.TempDir(), "body")
			writeScript(t, body, tt.body)
			t.Setenv("MINOS_RUN_DIR", runDir)
			t.Setenv("MINOS_RUN_BODY", body)
			t.Setenv("MINOS_HANDOFF", handoff)

			err := RunCommand(t.Context(), nil)
			if (err != nil) != tt.wantError {
				t.Fatalf("RunCommand error = %v, wantError = %t", err, tt.wantError)
			}
			_, statErr := os.Stat(runDir)
			if tt.wantKept && statErr != nil {
				t.Fatalf("continued run directory was removed: %v", statErr)
			}
			if !tt.wantKept && !os.IsNotExist(statErr) {
				t.Fatalf("owned run directory still exists or stat failed unexpectedly: %v", statErr)
			}
		})
	}
}

func TestRunCommandRequiresRunEnvironment(t *testing.T) {
	tests := []struct {
		name    string
		runDir  string
		runBody string
	}{
		{name: "missing run directory", runBody: "/bin/true"},
		{name: "missing run body", runDir: t.TempDir()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MINOS_RUN_DIR", tt.runDir)
			t.Setenv("MINOS_RUN_BODY", tt.runBody)

			err := RunCommand(t.Context(), nil)
			if err == nil {
				t.Fatal("RunCommand accepted incomplete run environment")
			}
			if !strings.Contains(err.Error(), "MINOS_RUN_DIR and MINOS_RUN_BODY are required") {
				t.Fatalf("RunCommand error = %q, want required-variable error", err)
			}
		})
	}
}
