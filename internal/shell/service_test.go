package shell

import (
	"testing"

	"bfj/minos/internal/forge"
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
