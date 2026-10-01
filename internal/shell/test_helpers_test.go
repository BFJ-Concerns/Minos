package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertContainsFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s does not contain %q\n%s", path, want, data)
	}
}

// installArchiveReceiverSSH stands in for the archive host: the fixture ssh
// hands the client's request to the real archive-receiver over DESTINATION,
// exactly as the forced command bound to the Minos key does, so the whole
// transport runs inside the temp directory with both ends' logic real. A
// caller that sets SSH_LOG in the environment also gets each request
// appended there.
func installArchiveReceiverSSH(t *testing.T, bin, destination string) {
	t.Helper()
	receiver, err := filepath.Abs(filepath.Join("..", "..", "scripts", "archive-receiver"))
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(bin, "ssh"), fmt.Sprintf(`#!/usr/bin/env sh
last=""
for argument in "$@"; do last="$argument"; done
[ -z "${SSH_LOG:-}" ] || printf '%%s\n' "$last" >>"$SSH_LOG"
SSH_ORIGINAL_COMMAND="$last" exec %q %q
`, receiver, destination))
}

// setTestRunCeilings supplies explicit resolved inputs to tests that bypass
// LoadServiceConfig. Loader/default behaviour is exercised by config tests.
func setTestRunCeilings(cfg *ServiceConfig) {
	cfg.Runs.MemoryEnvelopeGiB = 22
	cfg.Runs.DurationCeiling = "43200.000000000s"
	cfg.Runs.PressureThresholdPercent = 85
}
