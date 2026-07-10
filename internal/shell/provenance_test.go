package shell

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pinsFixture = `[roles.lead]
id = "lead-claude"
engine = "claude"
model = "claude-opus-pinned"
family = "anthropic"

[roles.specialist]
id = "spec-claude"
engine = "claude"
model = "claude-opus-pinned"
family = "anthropic"

[[roles.verifiers]]
id = "verify-codex"
engine = "codex"
model = "gpt-pinned"
family = "openai"

[[roles.bar-judges]]
id = "bar-codex"
engine = "codex"
model = "gpt-pinned"
family = "openai"
`

func TestAssembleProvenanceRecordsCrossFamilyAndUnknownModel(t *testing.T) {
	dir := t.TempDir()
	pins := filepath.Join(dir, "pins.toml")
	resolved := filepath.Join(dir, "resolved.json")
	if err := os.WriteFile(pins, []byte(pinsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	data := `[
{"id":"lead-claude","resolved_model":"claude-opus-pinned"},
{"id":"spec-claude","resolved_model":"claude-opus-pinned"},
{"id":"verify-codex","resolved_model":"model-unknown"},
{"id":"bar-codex","resolved_model":"model-unknown"}
]`
	if err := os.WriteFile(resolved, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err := assembleProvenance(pins, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 || records[2].Split != "cross_family" || !records[2].Degraded || records[2].ResolvedModel != "model-unknown" {
		t.Fatalf("unexpected provenance: %#v", records)
	}
	encoded, err := json.Marshal(records)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("provenance was not serialisable: %v", err)
	}
}

func TestAssembleProvenanceFailsLoudlyOnResolvedMismatch(t *testing.T) {
	dir := t.TempDir()
	pins := filepath.Join(dir, "pins.toml")
	resolved := filepath.Join(dir, "resolved.json")
	if err := os.WriteFile(pins, []byte(pinsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	data := `[
{"id":"lead-claude","resolved_model":"floating-alias-surprise"},
{"id":"spec-claude","resolved_model":"claude-opus-pinned"},
{"id":"verify-codex","resolved_model":"model-unknown"},
{"id":"bar-codex","resolved_model":"model-unknown"}
]`
	if err := os.WriteFile(resolved, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := assembleProvenance(pins, resolved); err == nil {
		t.Fatal("resolved model mismatch was accepted")
	}
}

func TestCombineResolvedModelsWritesOneFlatArray(t *testing.T) {
	dir := t.TempDir()
	lead := filepath.Join(dir, "lead.json")
	workers := filepath.Join(dir, "workers.json")
	output := filepath.Join(dir, "combined.json")
	if err := os.WriteFile(lead, []byte(`[{"id":"lead-claude","resolved_model":"claude-opus-pinned"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workers, []byte(`[{"id":"verify-codex","resolved_model":"model-unknown"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := combineResolvedModels(lead, workers, output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var records []resolvedModel
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != "lead-claude" || records[1].ID != "verify-codex" {
		t.Fatalf("combined records = %#v", records)
	}
}

func TestLeadPinRejectsPlaceholderInAnyRole(t *testing.T) {
	dir := t.TempDir()
	pins := filepath.Join(dir, "pins.toml")
	placeholder := strings.Replace(pinsFixture, `model = "gpt-pinned"`, `model = "REPLACE_WITH_OPERATOR_CONFIRMED_CODEX_MODEL"`, 1)
	if err := os.WriteFile(pins, []byte(placeholder), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ProvenanceCommand([]string{"lead-pin", pins}, io.Discard); err == nil {
		t.Fatal("lead launcher accepted an unapproved worker pin")
	}
}
