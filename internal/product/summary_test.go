package product

import (
	"strings"
	"testing"
)

func TestSummaryRecordIsTheCompletionMarkerOfTheHeadItNames(t *testing.T) {
	tests := []struct {
		state     SummaryState
		want      State
		completed bool
	}{
		{SummaryInspecting, State{}, false},
		{SummaryReviewing, State{}, false},
		{SummaryIncomplete, State{}, false},
		{SummaryClean, Clean(), true},
		{SummaryChangesRequested, Attention(), true},
	}
	for _, test := range tests {
		record := SummaryRecord{Head: "head-1", Target: "target", State: test.state}
		if got, completed := record.Completion("head-1"); got != test.want || completed != test.completed {
			t.Fatalf("%s at its own head = (%#v, %t)", test.state, got, completed)
		}
		if _, completed := record.Completion("head-2"); completed {
			t.Fatalf("%s completed a head the record does not name", test.state)
		}
	}
}

func TestSummaryRecordSurvivesItsCommentBody(t *testing.T) {
	record := SummaryRecord{Head: "head-2", Target: "target", State: SummaryReviewing, FinishedHead: "head-1", FinishedState: SummaryClean}
	body, err := ComposeSummary(record, "**Minos: reviewing.**\n")
	if err != nil {
		t.Fatal(err)
	}
	want := "<!-- Minos summary: head=head-2 target=target state=reviewing finished-head=head-1 finished-state=clean -->\n**Minos: reviewing.**\n"
	if body != want {
		t.Fatalf("body = %q", body)
	}
	if !strings.HasPrefix(body, SummaryOpening) {
		t.Fatalf("body does not open with %q", SummaryOpening)
	}
	if parsed, found := ParseSummary(body); !found || parsed != record {
		t.Fatalf("parsed = (%#v, %t)", parsed, found)
	}
}

func TestSummaryRecordRefusesWhatTheGrammarCannotCarry(t *testing.T) {
	for name, record := range map[string]SummaryRecord{
		"a head carrying a second token": {Head: "head state=clean", Target: "target", State: SummaryIncomplete},
		"no head":                        {Target: "target", State: SummaryClean},
		"an unknown state":               {Head: "head", Target: "target", State: "approved"},
		"a finished head beside a finished state": {
			Head: "head-2", Target: "target", State: SummaryClean, FinishedHead: "head-1", FinishedState: SummaryClean,
		},
		"a last finished head that was not finished": {
			Head: "head-2", Target: "target", State: SummaryReviewing, FinishedHead: "head-1", FinishedState: SummaryIncomplete,
		},
	} {
		if line, err := record.Line(); err == nil {
			t.Fatalf("%s was rendered: %q", name, line)
		}
	}
	for name, body := range map[string]string{
		"a review's record":         "<!-- Minos: head=head target=target -->\ntext",
		"a record below the top":    "text\n<!-- Minos summary: head=head target=target state=clean -->",
		"a record with no state":    "<!-- Minos summary: head=head target=target -->\ntext",
		"a record with no target":   "<!-- Minos summary: head=head state=clean -->\ntext",
		"a record with a bad state": "<!-- Minos summary: head=head target=target state=approved -->\ntext",
	} {
		if record, found := ParseSummary(body); found {
			t.Fatalf("%s parsed as a summary: %#v", name, record)
		}
	}
	if record, found := ParseSummary("<!-- Minos summary: head=head target=target state=clean rounds=2 -->\r\ntext"); !found || record.State != SummaryClean {
		t.Fatalf("a record with a key from a later version = (%#v, %t)", record, found)
	}
}

func TestNextCarriesTheLastFinishedHeadAcrossUnfinishedStates(t *testing.T) {
	var record SummaryRecord
	record = record.Next("head-1", "target", SummaryInspecting)
	if _, _, found := record.LastFinished(); found {
		t.Fatalf("a first claim carried a finished head: %#v", record)
	}
	record = record.Next("head-1", "target", SummaryChangesRequested)
	record = record.Next("head-2", "target", SummaryInspecting)
	record = record.Next("head-2", "target", SummaryIncomplete)
	record = record.Next("head-3", "target", SummaryReviewing)
	if head, state, found := record.LastFinished(); !found || head != "head-1" || state != SummaryChangesRequested {
		t.Fatalf("last finished = (%q, %q, %t)", head, state, found)
	}
	if _, err := record.Line(); err != nil {
		t.Fatal(err)
	}
	record = record.Next("head-3", "target", SummaryClean)
	if head, state, found := record.LastFinished(); !found || head != "head-3" || state != SummaryClean || record.FinishedHead != "" {
		t.Fatalf("a finished head is not its own last finished head: %#v", record)
	}
}
