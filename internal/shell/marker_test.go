package shell

import "testing"

func TestMarkerRoundTrip(t *testing.T) {
	line, err := FormatMarker(map[string]string{
		"run":      "review",
		"head":     "4f9c2d1a8b3e",
		"verdict":  "converged",
		"coverage": "full",
		"bar":      "passed",
	})
	if err != nil {
		t.Fatal(err)
	}
	values, err := ParseMarker(line)
	if err != nil {
		t.Fatal(err)
	}
	if values["run"] != "review" || values["coverage"] != "full" {
		t.Fatalf("unexpected marker values: %#v", values)
	}
}

func TestMarkerRejectsSpacesInValues(t *testing.T) {
	if _, err := ParseMarker("Pump-19: run=review coverage=not full"); err == nil {
		t.Fatal("marker with spaced value was accepted")
	}
}

func TestMarkerRejectsDuplicateKeys(t *testing.T) {
	if _, err := ParseMarker("Pump-19: run=review run=fix"); err == nil {
		t.Fatal("marker with duplicate keys was accepted")
	}
}
