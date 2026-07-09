package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveReceiverFactsBindsReadyActorToLabelApplication(t *testing.T) {
	adaptation := fixtureTimelineAdaptation(t, "timeline-ready-added.json")
	facts := Facts{Occasion: "label-updated", Owner: "pump19", Repo: "subject", PR: "1", Actor: "mallory", Labels: []string{LabelReady}}
	got, err := resolveReceiverFacts(t.Context(), adaptation, facts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Occasion != "label-added:Ready" {
		t.Fatalf("occasion = %q", got.Occasion)
	}
	if got.Actor != "bob" {
		t.Fatalf("actor = %q, want label actor bob", got.Actor)
	}
}

func TestResolveReceiverFactsSupportsGenericLabelRemovalVocabulary(t *testing.T) {
	adaptation := fixtureTimelineAdaptation(t, "timeline-ready-removed.json")
	facts := Facts{Occasion: "label-updated", Owner: "pump19", Repo: "subject", PR: "1", Actor: "mallory"}
	got, err := resolveReceiverFacts(t.Context(), adaptation, facts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Occasion != "label-removed:Ready" {
		t.Fatalf("occasion = %q", got.Occasion)
	}
	if got.Actor != "bob" {
		t.Fatalf("actor = %q, want event actor bob", got.Actor)
	}
}

func fixtureTimelineAdaptation(t *testing.T, fixture string) Adaptation {
	t.Helper()
	fakeBin := t.TempDir()
	curl := filepath.Join(fakeBin, "curl")
	data, err := os.ReadFile(filepath.Join("testdata", "forgejo14", fixture))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(curl, []byte("#!/usr/bin/env sh\ncat <<'JSON'\n"+string(data)+"\nJSON\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	return Adaptation{
		Dir:        filepath.Join("..", "..", "scripts", "adaptations", "forgejo"),
		APIBase:    "http://forge.invalid",
		Credential: "token",
	}
}
