package shell

import (
	"os"
	"strings"
	"testing"
)

// Tests in this package execute run-body, archive-run and the dispatch
// scripts for real, and build child environments from os.Environ(). Inside a
// live Minos run that inherited environment points at the run's own record
// directory and the long-term archive, so a fixture would write into live
// operational state. Strip the whole MINOS_*/ENSEMBLE_* namespace before any
// test runs; each test sets the values it needs explicitly.
func TestMain(m *testing.M) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "MINOS_") || strings.HasPrefix(name, "ENSEMBLE_") {
			os.Unsetenv(name)
		}
	}
	os.Exit(m.Run())
}
