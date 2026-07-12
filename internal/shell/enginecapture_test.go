package shell

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureClaudeRecordsAndChecksEarlyServedModel(t *testing.T) {
	dir := t.TempDir()
	pins := filepath.Join(dir, "pins.toml")
	stream := filepath.Join(dir, "lead.jsonl")
	resolved := filepath.Join(dir, "resolved-lead.json")
	metrics := filepath.Join(dir, "lead-metrics.json")
	launcher := filepath.Join(dir, "claude-fixture")
	if err := os.WriteFile(pins, []byte(leadPinsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	writeScript(t, launcher, `#!/usr/bin/env sh
cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","model":"claude-opus-pinned"}'
printf '%s\n' '{"type":"assistant","message":{"model":"claude-opus-pinned","usage":{"input_tokens":10,"cache_creation_input_tokens":90,"cache_read_input_tokens":100,"output_tokens":12}}}'
printf '%s\n' '{"type":"assistant","message":{"model":"claude-opus-pinned","usage":{"input_tokens":5,"cache_creation_input_tokens":25,"cache_read_input_tokens":230,"output_tokens":18}}}'
printf '%s\n' '{"type":"system","subtype":"compact_boundary"}'
printf '%s\n' '{"type":"result","subtype":"success","usage":{"input_tokens":12,"cache_creation_input_tokens":115,"cache_read_input_tokens":330,"output_tokens":30},"modelUsage":{"claude-opus-pinned":{}}}'
`)
	var diagnostics bytes.Buffer
	if err := CaptureClaudeCommand(t.Context(), []string{"--pins", pins, "--output", stream, "--resolved", resolved, "--metrics", metrics, launcher}, strings.NewReader("mission"), &diagnostics); err != nil {
		t.Fatal(err)
	}
	assertContainsFile(t, stream, `"subtype":"init"`)
	var records []resolvedModel
	data, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "lead-claude" || records[0].ResolvedModel != "claude-opus-pinned" {
		t.Fatalf("unexpected lead record: %#v", records)
	}
	if !strings.Contains(diagnostics.String(), "type=system subtype=init") {
		t.Fatalf("missing stream diagnostic: %q", diagnostics.String())
	}
	var measured leadMetrics
	data, err = os.ReadFile(metrics)
	if err != nil || json.Unmarshal(data, &measured) != nil {
		t.Fatalf("read metrics: %v data=%q", err, data)
	}
	if measured.InputTokens != 457 || measured.DirectInputTokens != 12 || measured.CacheCreationInputTokens != 115 || measured.CacheReadInputTokens != 330 || measured.OutputTokens != 30 {
		t.Fatalf("lead token metrics=%#v", measured)
	}
	if measured.PromptStart != 200 || measured.PromptPeak != 260 || measured.PromptGrowth != 60 || measured.CompactionEvents != 1 || measured.StreamBytes == 0 {
		t.Fatalf("lead metrics=%#v", measured)
	}
}

func TestCaptureClaudeFailsLoudlyOnEarlyModelMismatch(t *testing.T) {
	dir := t.TempDir()
	pins := filepath.Join(dir, "pins.toml")
	launcher := filepath.Join(dir, "claude-fixture")
	if err := os.WriteFile(pins, []byte(leadPinsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	writeScript(t, launcher, "#!/usr/bin/env sh\nprintf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"model\":\"floating-alias-surprise\"}'\n")
	err := CaptureClaudeCommand(t.Context(), []string{
		"--pins", pins,
		"--output", filepath.Join(dir, "lead.jsonl"),
		"--resolved", filepath.Join(dir, "resolved-lead.json"),
		"--metrics", filepath.Join(dir, "lead-metrics.json"),
		launcher,
	}, strings.NewReader("mission"), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "model pin mismatch") {
		t.Fatalf("mismatch error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "resolved-lead.json")); !os.IsNotExist(err) {
		t.Fatalf("mismatch wrote a resolved record: %v", err)
	}
}

func TestCaptureClaudeFailsWhenStreamHasNoResolvedModel(t *testing.T) {
	dir := t.TempDir()
	pins := filepath.Join(dir, "pins.toml")
	launcher := filepath.Join(dir, "claude-fixture")
	resolved := filepath.Join(dir, "resolved-lead.json")
	if err := os.WriteFile(pins, []byte(leadPinsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	writeScript(t, launcher, "#!/usr/bin/env sh\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\"}'\n")
	err := CaptureClaudeCommand(t.Context(), []string{
		"--pins", pins,
		"--output", filepath.Join(dir, "lead.jsonl"),
		"--resolved", resolved,
		"--metrics", filepath.Join(dir, "lead-metrics.json"),
		launcher,
	}, strings.NewReader("mission"), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "without a resolved lead model") {
		t.Fatalf("missing-model error = %v", err)
	}
	if _, err := os.Stat(resolved); !os.IsNotExist(err) {
		t.Fatalf("missing-model stream wrote a resolved record: %v", err)
	}
}
