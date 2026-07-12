package modeladmission

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type admissionResult struct {
	Admitted bool   `json:"admitted"`
	Level    string `json:"level"`
	Reason   string `json:"reason"`
}

func TestRequestedAliasNeverReplacesResolvedModelEvidence(t *testing.T) {
	tests := []struct {
		name        string
		requested   string
		resolved    any
		counterpart *string
		unresolved  string
		wantCode    int
		want        admissionResult
	}{
		{
			name:      "allowed alias and exact resolved pin is full cross-family",
			requested: "opus", resolved: "claude-opus-4-8", counterpart: pointer("gpt"), unresolved: "stop",
			want: admissionResult{Admitted: true, Level: "full", Reason: "exact_pin_cross_family"},
		},
		{
			name:      "concrete request and exact resolved pin is also full",
			requested: "claude-opus-4-8", resolved: "claude-opus-4-8", counterpart: pointer("gpt"), unresolved: "stop",
			want: admissionResult{Admitted: true, Level: "full", Reason: "exact_pin_cross_family"},
		},
		{
			name:      "unrecognised alias cannot borrow an exact resolved pin",
			requested: "haiku", resolved: "claude-opus-4-8", counterpart: pointer("gpt"), unresolved: "stop", wantCode: 1,
			want: admissionResult{Admitted: false, Level: "none", Reason: "requested_model_unrecognised"},
		},
		{
			name:      "allowed alias cannot excuse a wrong resolved pin",
			requested: "opus", resolved: "claude-fable-5", counterpart: pointer("gpt"), unresolved: "stop", wantCode: 1,
			want: admissionResult{Admitted: false, Level: "none", Reason: "pin_mismatch"},
		},
		{
			name:      "allowed alias with unresolved model stops by default",
			requested: "opus", resolved: nil, counterpart: pointer("gpt"), unresolved: "stop", wantCode: 1,
			want: admissionResult{Admitted: false, Level: "none", Reason: "resolved_model_unavailable"},
		},
		{
			name:      "allowed alias with unresolved model takes explicit limited path",
			requested: "opus", resolved: nil, counterpart: pointer("gpt"), unresolved: "limited",
			want: admissionResult{Admitted: true, Level: "limited", Reason: "resolved_model_unproved"},
		},
		{
			name:      "allowed alias with same-family counterpart is limited",
			requested: "opus", resolved: "claude-opus-4-8", counterpart: pointer("claude"), unresolved: "stop",
			want: admissionResult{Admitted: true, Level: "limited", Reason: "exact_pin_family_degraded"},
		},
		{
			name:      "allowed alias without known counterpart is limited",
			requested: "opus", resolved: "claude-opus-4-8", counterpart: nil, unresolved: "stop",
			want: admissionResult{Admitted: true, Level: "limited", Reason: "exact_pin_family_degraded"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, code := admit(t, test.requested, test.resolved, test.counterpart, test.unresolved)
			if code != test.wantCode {
				t.Fatalf("exit code = %d, want %d; result = %#v", code, test.wantCode, got)
			}
			if got != test.want {
				t.Errorf("result = %#v, want %#v", got, test.want)
			}
		})
	}
}

func admit(t *testing.T, requested string, resolved any, counterpart *string, unresolved string) (admissionResult, int) {
	t.Helper()

	root := t.TempDir()
	policyPath := filepath.Join(root, "policy.json")
	recordPath := filepath.Join(root, "agent.json")
	writeJSON(t, policyPath, map[string]any{
		"schema_version":   1,
		"unresolved_model": unresolved,
		"roles": map[string]any{
			"verifier": map[string]any{
				"guarantee": true,
				"engine":    "claude", "model": "claude-opus-4-8", "family": "claude",
				"request_models": []string{"opus", "claude-opus-4-8"},
				"label_prefixes": []string{"verify:"},
			},
		},
	})
	writeJSON(t, recordPath, map[string]any{
		"schema_version": 2, "kind": "agent_record", "id": 8,
		"label": "verify:claude:0", "engine": "claude",
		"model": requested, "resolved_model": resolved,
	})

	arguments := []string{"--policy", policyPath, "--role", "verifier", "--record", recordPath}
	if counterpart != nil {
		arguments = append(arguments, "--counterpart-family", *counterpart)
	}
	command := exec.Command(admissionScript(t), arguments...)
	output, err := command.CombinedOutput()
	if command.ProcessState == nil {
		t.Fatalf("start admission script: %v", err)
	}
	var result admissionResult
	if decodeErr := json.Unmarshal(output, &result); decodeErr != nil {
		t.Fatalf("decode admission result: %v\n%s", decodeErr, output)
	}
	return result, command.ProcessState.ExitCode()
}

func admissionScript(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "scripts", "review", "admit-worker-model")
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()

	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func pointer(value string) *string { return &value }
