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
