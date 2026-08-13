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

func TestHeldStateExtendsProductVocabulary(t *testing.T) {
	state := Held()
	if state.Name() != "held" || state.ForgeState() != "pending" || state.Description() != "Blocked on target branch" {
		t.Fatalf("held state = %#v", state)
	}
	if !state.Valid() {
		t.Fatal("held state is not part of the product vocabulary")
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
