package product

import "testing"

func TestHeadAndTargetRoundTripInTrailingRecord(t *testing.T) {
	record, err := FormatRecord(map[string]string{
		"head":          "head-sha",
		RecordTargetKey: "target-sha",
	})
	if err != nil {
		t.Fatal(err)
	}
	if record != "<!-- Minos: head=head-sha target=target-sha -->" {
		t.Fatalf("record = %q", record)
	}

	values, ok := TrailingRecord("Confirmed findings remain.\n\n" + record + "\n")
	if !ok || values["head"] != "head-sha" || values[RecordTargetKey] != "target-sha" {
		t.Fatalf("trailing record = %#v, valid = %t", values, ok)
	}
}
