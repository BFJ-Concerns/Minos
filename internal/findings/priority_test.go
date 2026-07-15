package findings

import (
	"encoding/json"
	"testing"
)

func TestPriorityClosedEncodingAndInclusiveOrder(t *testing.T) {
	valid := []Priority{P0, P1, P2, P3}
	for _, priority := range valid {
		encoded, err := json.Marshal(priority)
		if err != nil {
			t.Fatalf("marshal %s: %v", priority, err)
		}
		var decoded Priority
		if err := json.Unmarshal(encoded, &decoded); err != nil || decoded != priority {
			t.Fatalf("round trip %s: decoded=%s err=%v", priority, decoded, err)
		}
	}
	for _, value := range []string{"", "p1", "1", "P4", "critical"} {
		if _, err := ParsePriority(value); err == nil {
			t.Errorf("ParsePriority(%q) succeeded", value)
		}
	}
	for priorityIndex, priority := range valid {
		for thresholdIndex, threshold := range valid {
			want := priorityIndex <= thresholdIndex
			if got := AtOrAbove(priority, threshold); got != want {
				t.Errorf("AtOrAbove(%s, %s)=%v want %v", priority, threshold, got, want)
			}
		}
	}
}

func TestResolvedPolicyRejectsNarrowerRepairSet(t *testing.T) {
	for publishIndex, publish := range []Priority{P0, P1, P2, P3} {
		for repairIndex, repair := range []Priority{P0, P1, P2, P3} {
			policy := ResolvedPolicy{PublishThreshold: publish, RepairThreshold: repair, Mode: PublishThroughP3Mode}
			err := policy.Validate()
			wantValid := repairIndex >= publishIndex
			if (err == nil) != wantValid {
				t.Errorf("publish=%s repair=%s err=%v wantValid=%v", publish, repair, err, wantValid)
			}
		}
	}
}

func TestAssuranceIsDistinctClosedVocabulary(t *testing.T) {
	encoded, err := json.Marshal(AgentJudgement)
	if err != nil || string(encoded) != `"agent-judgement"` {
		t.Fatalf("encoded=%s err=%v", encoded, err)
	}
	for _, value := range []string{"full", "limited", "deterministic", ""} {
		if _, err := ParseAssurance(value); err == nil {
			t.Errorf("ParseAssurance(%q) succeeded", value)
		}
	}
}
