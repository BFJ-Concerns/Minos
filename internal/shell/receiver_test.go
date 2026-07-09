package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveReceiverFactsBindsReadyActorToLabelApplication(t *testing.T) {
	adaptation := testLabelEventAdaptation(t, "added", LabelReady, "mallory", "bob")
	facts := Facts{Occasion: "label-updated", Owner: "pump19", Repo: "subject", PR: "1", Actor: "bob", Labels: []string{LabelReady}}
	got, err := resolveReceiverFacts(t.Context(), adaptation, facts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Occasion != "label-added:Ready" {
		t.Fatalf("occasion = %q", got.Occasion)
	}
	if got.Actor != "mallory" {
		t.Fatalf("actor = %q, want label actor mallory", got.Actor)
	}
}

func TestResolveReceiverFactsSupportsGenericLabelRemovalVocabulary(t *testing.T) {
	adaptation := testLabelEventAdaptation(t, "removed", "Needs Work", "ignored", "bob")
	facts := Facts{Occasion: "label-updated", Owner: "pump19", Repo: "subject", PR: "1", Actor: "bob"}
	got, err := resolveReceiverFacts(t.Context(), adaptation, facts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Occasion != "label-removed:Needs Work" {
		t.Fatalf("occasion = %q", got.Occasion)
	}
	if got.Actor != "bob" {
		t.Fatalf("actor = %q, want event actor bob", got.Actor)
	}
}

func testLabelEventAdaptation(t *testing.T, action, label, labelActor, eventActor string) Adaptation {
	t.Helper()
	dir := t.TempDir()
	latest := "#!/usr/bin/env sh\n" +
		"printf 'ACTION=%s\\nLABEL=%s\\nACTOR=%s\\n' '" + action + "' '" + label + "' '" + eventActor + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "latest-label-event"), []byte(latest), 0o755); err != nil {
		t.Fatal(err)
	}
	labelActorScript := "#!/usr/bin/env sh\nprintf '%s\\n' '" + labelActor + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "label-actor"), []byte(labelActorScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return Adaptation{Dir: dir}
}
