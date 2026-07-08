package shell

import "testing"

func TestFindingHandleGrammar(t *testing.T) {
	if !ValidFindingHandle("F-7KQ3") {
		t.Fatal("expected sample handle to match")
	}
	for _, invalid := range []string{"F-ILOU", "F-abc1", "X-7KQ3", "F-7KQ33"} {
		if ValidFindingHandle(invalid) {
			t.Fatalf("invalid handle accepted: %s", invalid)
		}
	}
}

func TestMintFindingHandleAvoidsExisting(t *testing.T) {
	existing := map[string]bool{}
	for i := 0; i < 128; i++ {
		handle, err := MintFindingHandle(existing)
		if err != nil {
			t.Fatal(err)
		}
		if existing[handle] {
			t.Fatalf("minted duplicate handle %s", handle)
		}
		existing[handle] = true
	}
}
