package shell

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const leadPinsFixture = `[roles.lead]
id = "lead-claude"
engine = "claude"
model = "claude-opus-pinned"
family = "anthropic"
`

func TestLeadOnlyPinsFileValidates(t *testing.T) {
	pins := filepath.Join(t.TempDir(), "pins.toml")
	if err := os.WriteFile(pins, []byte(leadPinsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ProvenanceCommand([]string{"lead-pin", pins}, io.Discard); err != nil {
		t.Fatalf("lead-only pins rejected: %v", err)
	}
}

func TestLegacyWorkerPinsFailLoudly(t *testing.T) {
	pins := filepath.Join(t.TempDir(), "pins.toml")
	legacy := leadPinsFixture + `
[roles.specialist]
id = "spec-claude"
engine = "claude"
model = "claude-opus-pinned"
family = "anthropic"
`
	if err := os.WriteFile(pins, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	err := ProvenanceCommand([]string{"lead-pin", pins}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "pins file governs roles.lead only") {
		t.Fatalf("error = %v, want lead-only migration message", err)
	}
}

func TestLeadPinRejectsPlaceholder(t *testing.T) {
	pins := filepath.Join(t.TempDir(), "pins.toml")
	placeholder := strings.Replace(leadPinsFixture, "claude-opus-pinned", "REPLACE_WITH_OPERATOR_CONFIRMED_CLAUDE_MODEL", 1)
	if err := os.WriteFile(pins, []byte(placeholder), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ProvenanceCommand([]string{"lead-pin", pins}, io.Discard); err == nil {
		t.Fatal("lead launcher accepted an unapproved lead pin")
	}
}
