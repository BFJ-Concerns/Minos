package product

import "testing"

func TestReviewCauseAndTargetRoundTripInTrailingRecord(t *testing.T) {
	record, err := FormatRecord(map[string]string{
		RecordCauseKey:  RecordCauseRequiredChecks,
		"head":          "head-sha",
		RecordTargetKey: "target-sha",
	})
	if err != nil {
		t.Fatal(err)
	}
	if record != "<!-- Minos: cause=required-checks head=head-sha target=target-sha -->" {
		t.Fatalf("record = %q", record)
	}

	values, ok := TrailingRecord("A required check failed.\n\n" + record + "\n")
	if !ok || values[RecordCauseKey] != RecordCauseRequiredChecks || values[RecordTargetKey] != "target-sha" {
		t.Fatalf("trailing record = %#v, valid = %t", values, ok)
	}
}

func TestTrailingRecordDoesNotInventACauseForOlderReviews(t *testing.T) {
	values, ok := TrailingRecord("Confirmed findings remain.\n\n<!-- Minos: head=head-sha target=target-sha -->")
	if !ok {
		t.Fatal("existing product record was not recognised")
	}
	if cause := values[RecordCauseKey]; cause != "" {
		t.Fatalf("cause = %q, want absent", cause)
	}
}
