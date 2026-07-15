package shell

import (
	"testing"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/product"
)

func TestAlreadyReviewedRequiresReviewAndLatestTerminalStatus(t *testing.T) {
	record, err := product.FormatRecord(map[string]string{"head": "head", "target": "target"})
	if err != nil {
		t.Fatal(err)
	}
	matchingReview := forge.Review{User: "Minos", CommitID: "head", Body: record}
	status := func(id int64, state product.State) forge.Status {
		return forge.Status{
			ID: id, Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
			Creator: "Minos", State: forge.StatusState(state.ForgeState()), Description: state.Description(),
		}
	}

	tests := []struct {
		name     string
		reviews  []forge.Review
		statuses []forge.Status
		want     bool
	}{
		{name: "matching review without status is incomplete", reviews: []forge.Review{matchingReview}},
		{name: "clean conclusion is complete", reviews: []forge.Review{matchingReview}, statuses: []forge.Status{status(1, product.Clean())}, want: true},
		{name: "attention conclusion is complete", reviews: []forge.Review{matchingReview}, statuses: []forge.Status{status(1, product.Attention())}, want: true},
		{
			name:    "legacy failure description is incomplete",
			reviews: []forge.Review{matchingReview},
			statuses: []forge.Status{{
				ID: 1, Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
				Creator: "Minos", State: forge.StatusFailure, Description: "Review needs attention",
			}},
		},
		{
			name:    "newer working status makes older conclusion incomplete",
			reviews: []forge.Review{matchingReview},
			statuses: []forge.Status{
				status(1, product.Clean()),
				status(2, product.Working()),
			},
		},
		{
			name:    "other creator status is ignored",
			reviews: []forge.Review{matchingReview},
			statuses: []forge.Status{{
				ID: 1, Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
				Creator: "someone-else", State: forge.StatusSuccess, Description: product.Clean().Description(),
			}},
		},
		{
			name:     "review for another head is incomplete",
			reviews:  []forge.Review{{User: "Minos", CommitID: "old-head", Body: record}},
			statuses: []forge.Status{status(1, product.Clean())},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forge.Snapshot{
				HeadSHA: "head", TargetSHA: "target", Reviews: test.reviews, Statuses: test.statuses,
			}
			if got := alreadyReviewed(snapshot, "Minos"); got != test.want {
				t.Fatalf("alreadyReviewed() = %t, want %t", got, test.want)
			}
		})
	}
}
