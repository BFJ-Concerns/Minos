package product

import "testing"

func TestIncompleteStateExtendsProductVocabulary(t *testing.T) {
	state := Incomplete()
	if state.Name() != "incomplete" || state.ForgeState() != "error" || state.Description() != "Review incomplete" {
		t.Fatalf("incomplete state = %#v", state)
	}
	if !state.Valid() {
		t.Fatal("incomplete state is not part of the product vocabulary")
	}
}

func TestContinuationStateExtendsProductVocabulary(t *testing.T) {
	state := Continuation()
	if state.Name() != "continuation" || state.ForgeState() != "pending" ||
		state.Description() != "Review continuing in a fresh run" {
		t.Fatalf("continuation state = %#v", state)
	}
	if !state.Valid() {
		t.Fatal("continuation state is not valid")
	}
}

func TestCompletionMarkerRequiresMatchingForgeStateAndDescription(t *testing.T) {
	tests := []struct {
		name        string
		forgeState  string
		description string
		want        State
		found       bool
	}{
		{name: "clean status", forgeState: Clean().ForgeState(), description: Clean().Description(), want: Clean(), found: true},
		{name: "attention status", forgeState: Attention().ForgeState(), description: Attention().Description(), want: Attention(), found: true},
		{name: "clean description with failure state", forgeState: Attention().ForgeState(), description: Clean().Description()},
		{name: "attention description with success state", forgeState: Clean().ForgeState(), description: Attention().Description()},
		{name: "unfinished status", forgeState: Working().ForgeState(), description: Working().Description()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, found := CompletionMarker(test.forgeState, test.description)
			if got != test.want || found != test.found {
				t.Fatalf("CompletionMarker(%q, %q) = (%#v, %t), want (%#v, %t)", test.forgeState, test.description, got, found, test.want, test.found)
			}
		})
	}
}
