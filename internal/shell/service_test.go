package shell

import (
	"testing"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/product"
)

func TestAlreadyReviewedRequiresMinosReviewOnCurrentHead(t *testing.T) {
	tests := []struct {
		name    string
		reviews []forge.Review
		want    bool
	}{
		{
			name:    "current head review blocks without status or product record",
			reviews: []forge.Review{{User: "Minos", CommitID: "head", Body: "ordinary review"}},
			want:    true,
		},
		{
			name:    "review from another account does not block",
			reviews: []forge.Review{{User: "reviewer", CommitID: "head"}},
		},
		{
			name:    "review of an older head does not block",
			reviews: []forge.Review{{User: "Minos", CommitID: "old-head"}},
		},
		{name: "no reviews"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forge.Snapshot{HeadSHA: "head", Reviews: test.reviews}
			if got := alreadyReviewed(snapshot, "Minos"); got != test.want {
				t.Fatalf("alreadyReviewed() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestTrustedReviewSurvivesOnlyMinosAuthoredMovement(t *testing.T) {
	snapshot := forge.Snapshot{HeadSHA: "current", Reviews: []forge.Review{{ID: 7, User: "Minos", CommitID: "reviewed", State: "APPROVED"}}}
	for _, test := range []struct {
		name   string
		middle string
		want   bool
	}{
		{name: "Minos-only movement", middle: "Minos", want: true},
		{name: "foreign movement", middle: "contributor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			commits := []forge.Commit{{SHA: "reviewed", Author: "contributor"}, {SHA: "middle", Author: test.middle}, {SHA: "current", Author: "Minos"}}
			_, got := trustedReview(snapshot, commits, "Minos")
			if got != test.want {
				t.Fatalf("trustedReview() found = %t, want %t", got, test.want)
			}
		})
	}
}

func TestCheckCausedVerdictIsSpentOnlyByTargetMovementOnANonFork(t *testing.T) {
	review := forge.Review{
		State: "REQUEST_CHANGES",
		Body:  "Required checks failed.\n\n<!-- Minos: cause=required-checks head=head target=old-target -->",
	}
	for _, test := range []struct {
		name     string
		snapshot forge.Snapshot
		want     bool
	}{
		{
			name: "moved target in the same repository",
			snapshot: forge.Snapshot{
				TargetSHA: "new-target", HeadRepository: "owner/repo", TargetRepository: "owner/repo",
			},
			want: true,
		},
		{
			name: "unchanged target",
			snapshot: forge.Snapshot{
				TargetSHA: "old-target", HeadRepository: "owner/repo", TargetRepository: "owner/repo",
			},
		},
		{
			name: "fork target movement",
			snapshot: forge.Snapshot{
				TargetSHA: "new-target", HeadRepository: "contributor/repo", TargetRepository: "owner/repo",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := checkCausedVerdictSpent(review, test.snapshot); got != test.want {
				t.Fatalf("checkCausedVerdictSpent() = %t, want %t", got, test.want)
			}
		})
	}

	findingsReview := review
	findingsReview.Body = "Confirmed findings remain.\n\n<!-- Minos: head=head target=old-target -->"
	if checkCausedVerdictSpent(findingsReview, forge.Snapshot{
		TargetSHA: "new-target", HeadRepository: "owner/repo", TargetRepository: "owner/repo",
	}) {
		t.Fatal("findings-caused verdict was spent by target movement")
	}

	chainWaitReview := forge.Review{
		State: "APPROVED",
		Body:  "Waiting for the preceding member.\n\n<!-- Minos: cause=chain-wait head=head target=old-target -->",
	}
	if !checkCausedVerdictSpent(chainWaitReview, forge.Snapshot{
		TargetSHA: "new-target", HeadRepository: "owner/repo", TargetRepository: "owner/repo",
	}) {
		t.Fatal("chain-wait approval was not spent by target movement")
	}
	if checkCausedVerdictSpent(chainWaitReview, forge.Snapshot{
		TargetSHA: "old-target", HeadRepository: "owner/repo", TargetRepository: "owner/repo",
	}) {
		t.Fatal("chain-wait approval was spent before its target moved")
	}
}

func TestContinuationPriorityPrefersAnUnfinishedMinosRun(t *testing.T) {
	tests := []struct {
		name     string
		statuses []forge.Status
		want     int
	}{
		{
			name: "incomplete current head",
			statuses: []forge.Status{{
				Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
				Creator: "Minos", Description: product.Incomplete().Description(),
			}},
			want: 0,
		},
		{
			name: "working current head",
			statuses: []forge.Status{{
				Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
				Creator: "Minos", Description: product.Working().Description(),
			}},
			want: 0,
		},
		{
			name: "continued current head",
			statuses: []forge.Status{{
				Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
				Creator: "Minos", Description: product.Continuation().Description(),
			}},
			want: 0,
		},
		{
			name: "other account does not claim continuation",
			statuses: []forge.Status{{
				Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
				Creator: "SomeBot", Description: product.Incomplete().Description(),
			}},
			want: 1,
		},
		{
			name: "held status is not unfinished",
			statuses: []forge.Status{{
				Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
				Creator: "Minos", Description: product.Held().Description(),
			}},
			want: 1,
		},
		{
			name: "clean terminal status is not unfinished",
			statuses: []forge.Status{{
				Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
				Creator: "Minos", Description: product.Clean().Description(),
			}},
			want: 1,
		},
		{
			name: "later terminal status supersedes an incomplete status",
			statuses: []forge.Status{
				{
					ID: 11, Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
					Creator: "Minos", Description: product.Incomplete().Description(),
				},
				{
					ID: 12, Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
					Creator: "Minos", Description: product.Merged().Description(),
				},
			},
			want: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forge.Snapshot{Statuses: test.statuses}
			if got := continuationPriority(snapshot, "Minos"); got != test.want {
				t.Fatalf("continuationPriority() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestCompletedRunStatusMarksCleanAttentionAndMergedHeads(t *testing.T) {
	const targetURL = "https://forge.example/owner/repo/pulls/1#minos-target-base"
	ownedStatus := func(id int64, description string) forge.Status {
		return forge.Status{
			ID: id, Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
			Creator: "Minos", Description: description, TargetURL: targetURL,
		}
	}
	tests := []struct {
		name     string
		statuses []forge.Status
		want     bool
	}{
		{name: "clean status completes the run", statuses: []forge.Status{ownedStatus(1, product.Clean().Description())}, want: true},
		{name: "attention status completes the run", statuses: []forge.Status{ownedStatus(1, product.Attention().Description())}, want: true},
		{name: "merged status completes the run", statuses: []forge.Status{ownedStatus(1, product.Merged().Description())}, want: true},
		{
			name: "held status bound to the current environment completes the run",
			statuses: []forge.Status{{
				ID: 1, Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
				Creator: "Minos", Description: product.Held().Description(),
				TargetURL: targetURL + "+minos-env-currentstamp",
			}},
			want: true,
		},
		{
			name: "held status bound to an older environment is spent",
			statuses: []forge.Status{{
				ID: 1, Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
				Creator: "Minos", Description: product.Held().Description(),
				TargetURL: targetURL + "+minos-env-olderstamp",
			}},
		},
		{name: "legacy held status with no environment binding is spent", statuses: []forge.Status{ownedStatus(1, product.Held().Description())}},
		{name: "incomplete status leaves the head eligible", statuses: []forge.Status{ownedStatus(1, product.Incomplete().Description())}},
		{name: "working status leaves the head eligible", statuses: []forge.Status{ownedStatus(1, product.Working().Description())}},
		{name: "continuation status leaves the head eligible", statuses: []forge.Status{ownedStatus(1, product.Continuation().Description())}},
		{
			name: "later working status supersedes an older clean marker",
			statuses: []forge.Status{
				ownedStatus(11, product.Clean().Description()),
				ownedStatus(12, product.Working().Description()),
			},
		},
		{
			name: "newer foreign status does not mask this pull request's clean marker",
			statuses: []forge.Status{
				ownedStatus(11, product.Clean().Description()),
				{
					ID: 12, Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
					Creator: "Minos", Description: product.Working().Description(),
					TargetURL: "https://forge.example/owner/repo/pulls/2#minos-target-base",
				},
			},
			want: true,
		},
		{
			name: "another account's clean status is not a marker",
			statuses: []forge.Status{{
				Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
				Creator: "SomeBot", Description: product.Clean().Description(),
			}},
		},
		{
			name: "another pull request's clean status is not a marker",
			statuses: []forge.Status{{
				Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
				Creator: "Minos", Description: product.Clean().Description(),
				TargetURL: "https://forge.example/owner/repo/pulls/2#minos-target-base",
			}},
		},
		{name: "no statuses"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forge.Snapshot{Statuses: test.statuses}
			if got := completedRunStatus(snapshot, "Minos", targetURL, "currentstamp"); got != test.want {
				t.Fatalf("completedRunStatus() = %t, want %t", got, test.want)
			}
		})
	}
}
