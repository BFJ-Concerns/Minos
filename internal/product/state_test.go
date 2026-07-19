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
