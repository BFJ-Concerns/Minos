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
			if got := completedRunStatus(snapshot, "Minos", targetURL); got != test.want {
				t.Fatalf("completedRunStatus() = %t, want %t", got, test.want)
			}
		})
	}
}
