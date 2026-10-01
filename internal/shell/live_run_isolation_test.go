package shell

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Tests execute run scripts with inherited environments. Remove operational
// run variables before any test runs; retain only the explicit transcript-test
// opt-in, which identifies an isolated configuration seed rather than live state.
func TestMain(m *testing.M) {
	if err := stripRunTestEnvironment(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func stripRunTestEnvironment() error {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "MINOS_TEST_CLAUDE_CONFIG_SEED" {
			continue
		}
		if strings.HasPrefix(name, "MINOS_") || strings.HasPrefix(name, "ENSEMBLE_") {
			if err := os.Unsetenv(name); err != nil {
				return fmt.Errorf("clear test environment %s: %w", name, err)
			}
		}
	}
	return nil
}

func TestRunTestEnvironmentPreservesOnlyTranscriptOptIn(t *testing.T) {
	for _, test := range []struct {
		name string
		seed string
	}{
		{name: "disabled"},
		{name: "explicit seed", seed: t.TempDir()},
	} {
		t.Run(test.name, func(t *testing.T) {
			seed := test.seed
			t.Setenv("MINOS_TEST_CLAUDE_CONFIG_SEED", seed)
			for _, name := range []string{"MINOS_RUN_DIR", "MINOS_ARCHIVE_CONFIG", "MINOS_TEST_OTHER", "ENSEMBLE_RUN_RECORD_DIR"} {
				t.Setenv(name, "ambient-live-state")
			}
			if err := stripRunTestEnvironment(); err != nil {
				t.Fatal(err)
			}
			if got := os.Getenv("MINOS_TEST_CLAUDE_CONFIG_SEED"); got != seed {
				t.Fatalf("transcript opt-in = %q, want %q", got, seed)
			}
			for _, name := range []string{"MINOS_RUN_DIR", "MINOS_ARCHIVE_CONFIG", "MINOS_TEST_OTHER", "ENSEMBLE_RUN_RECORD_DIR"} {
				if _, present := os.LookupEnv(name); present {
					t.Fatalf("operational environment %s survived the scrub", name)
				}
			}
		})
	}
}
