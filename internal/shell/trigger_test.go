package shell

import "testing"

func TestEvaluateTriggersSkipsDraftsAndMatchesAuthor(t *testing.T) {
	drafts := false
	repo := RepoConfig{
		Triggers: []TriggerRule{{
			Run:     "review",
			On:      []string{"pr-opened"},
			Authors: []string{"bob"},
			Drafts:  &drafts,
		}},
	}
	facts := Facts{Occasion: "pr-opened", Author: "bob", Draft: false}
	decision, ok := EvaluateTriggers(facts, repo)
	if !ok || decision.Kind != RunReview {
		t.Fatalf("expected review trigger, got %#v %v", decision, ok)
	}
	facts.Draft = true
	if _, ok := EvaluateTriggers(facts, repo); ok {
		t.Fatal("draft PR matched a drafts=false rule")
	}
}

func TestNewHeadOccasionsSkipPayloadLabelGate(t *testing.T) {
	if !NewHeadOccasion("pr-synchronized") {
		t.Fatal("synchronised PR should be a new-head occasion")
	}
	if NewHeadOccasion("pr-edited") {
		t.Fatal("edited PR should not skip the payload label gate")
	}
}

func TestFlakyLabelTriggerUsesExactVocabularyAndActorGuard(t *testing.T) {
	inFlight, err := InFlightLabel(RunFlaky)
	if err != nil {
		t.Fatal(err)
	}
	if inFlight == LabelFlakyTests {
		t.Fatal("standing flaky label was reused as the crash-recovery claim")
	}
	repo := RepoConfig{Triggers: []TriggerRule{{
		Run:    "flaky",
		On:     []string{"label-added:" + LabelFlakyTests},
		Actors: []string{"ci-bot", "minos"},
	}}}
	facts := Facts{Occasion: "label-added:" + LabelFlakyTests, Actor: "ci-bot"}

	decision, ok := EvaluateTriggers(facts, repo)
	if !ok || decision.Kind != RunFlaky {
		t.Fatalf("expected flaky trigger, got %#v %v", decision, ok)
	}
	facts.Actor = "contributor"
	if _, ok := EvaluateTriggers(facts, repo); ok {
		t.Fatal("unauthorised flaky-label actor matched the trigger")
	}
}
