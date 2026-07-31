package shell

import "testing"

func TestSweepDecisionMessageExposesDeferrals(t *testing.T) {
	facts := Facts{Owner: "owner", Repo: "repository", PR: "12"}
	if got := sweepDecisionMessage(facts, "deferred: open dependencies: owner/prerequisite#7"); got != "owner/repository#12: deferred: open dependencies: owner/prerequisite#7" {
		t.Fatalf("message = %q", got)
	}
	if got := sweepDecisionMessage(facts, "nothing"); got != "" {
		t.Fatalf("nothing message = %q, want empty", got)
	}
}
