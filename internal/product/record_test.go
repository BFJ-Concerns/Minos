package product

import "testing"

func TestFormatRecordBindsHeadAndTarget(t *testing.T) {
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

}

func TestFormatRecordRefusesATokenTheGrammarCannotCarry(t *testing.T) {
	if _, err := FormatRecord(map[string]string{"head": "head sha"}); err == nil {
		t.Fatal("a value carrying a space was formatted")
	}
}
