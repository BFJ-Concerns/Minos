package shell

import (
	"testing"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/product"
)

func TestTerminalCurrentReviewMarksCompletion(t *testing.T) {
	tests := []struct {
		name    string
		reviews []forge.Review
		want    bool
	}{
		{
			name:    "approved review on the current head",
			reviews: []forge.Review{{User: "Minos", CommitID: "head", State: "APPROVED"}},
			want:    true,
		},
		{
			name:    "comment review on the current head is not terminal",
			reviews: []forge.Review{{User: "Minos", CommitID: "head", State: "COMMENT"}},
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
			review, found := currentReview(snapshot, "Minos")
			_, terminal := terminalState(review)
			if got := found && terminal; got != test.want {
				t.Fatalf("current terminal review = %t, want %t", got, test.want)
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
				Provider: forge.ForgejoProvider, Context: "Minos",
				Creator: "Minos", Description: product.Incomplete().Description(),
			}},
			want: 0,
		},
		{
			name: "working current head",
			statuses: []forge.Status{{
				Provider: forge.ForgejoProvider, Context: "Minos",
				Creator: "Minos", Description: product.Working().Description(),
			}},
			want: 0,
		},
		{
			name: "continued current head",
			statuses: []forge.Status{{
				Provider: forge.ForgejoProvider, Context: "Minos",
				Creator: "Minos", Description: product.Continuation().Description(),
			}},
			want: 0,
		},
		{
			name: "other account does not claim continuation",
			statuses: []forge.Status{{
				Provider: forge.ForgejoProvider, Context: "Minos",
				Creator: "SomeBot", Description: product.Incomplete().Description(),
			}},
			want: 1,
		},
		{
			name: "clean terminal status is not unfinished",
			statuses: []forge.Status{{
				Provider: forge.ForgejoProvider, Context: "Minos",
				Creator: "Minos", Description: product.Clean().Description(),
			}},
			want: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forge.Snapshot{Statuses: test.statuses}
			if got := continuationPriority(snapshot, "Minos", "Minos"); got != test.want {
				t.Fatalf("continuationPriority() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestAdmissionCompletionStatuses(t *testing.T) {
	ownedStatus := func(id int64, state forge.StatusState, description string) forge.Status {
		return forge.Status{
			ID: id, Provider: forge.ForgejoProvider, Context: "Minos",
			Creator: "Minos", State: state, Description: description,
		}
	}
	tests := []struct {
		name     string
		statuses []forge.Status
		want     bool
	}{
		{name: "clean status completes the run", statuses: []forge.Status{ownedStatus(1, forge.StatusSuccess, product.Clean().Description())}, want: true},
		{name: "attention status completes the run", statuses: []forge.Status{ownedStatus(1, forge.StatusFailure, product.Attention().Description())}, want: true},
		{name: "clean description with failure state leaves the head eligible", statuses: []forge.Status{ownedStatus(1, forge.StatusFailure, product.Clean().Description())}},
		{name: "attention description with success state leaves the head eligible", statuses: []forge.Status{ownedStatus(1, forge.StatusSuccess, product.Attention().Description())}},
		{name: "incomplete status leaves the head eligible", statuses: []forge.Status{ownedStatus(1, forge.StatusError, product.Incomplete().Description())}},
		{name: "working status leaves the head eligible", statuses: []forge.Status{ownedStatus(1, forge.StatusPending, product.Working().Description())}},
		{name: "continuation status leaves the head eligible", statuses: []forge.Status{ownedStatus(1, forge.StatusPending, product.Continuation().Description())}},
		{
			name: "later working status supersedes an older clean marker",
			statuses: []forge.Status{
				ownedStatus(11, forge.StatusSuccess, product.Clean().Description()),
				ownedStatus(12, forge.StatusPending, product.Working().Description()),
			},
		},
		{
			name: "another account's clean status is not a marker",
			statuses: []forge.Status{{
				Provider: forge.ForgejoProvider, Context: "Minos",
				Creator: "SomeBot", State: forge.StatusSuccess, Description: product.Clean().Description(),
			}},
		},
		{name: "no statuses"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := forge.Snapshot{Statuses: test.statuses, DependenciesAvailable: true}
			cfg := ServiceConfig{}
			cfg.Service.BotLogin = "Minos"
			cfg.Service.StatusContext = "Minos"
			got := assessPullRequestAdmission(cfg, RepoConfig{}, snapshot)
			if got.completedRun != test.want {
				t.Fatalf("admission completedRun = %t, want %t", got.completedRun, test.want)
			}
		})
	}
}
