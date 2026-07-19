package forge

import "testing"

func TestReduceRequiredChecks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		required []CheckIdentity
		statuses []Status
		want     CheckDecision
	}{
		{
			name: "all newest attempts pass",
			required: []CheckIdentity{
				{Provider: "forgejo", Context: "build"},
				{Provider: "forgejo", Context: "test"},
			},
			statuses: []Status{
				{ID: 1, Provider: "forgejo", Context: "build", State: StatusFailure},
				{ID: 3, Provider: "forgejo", Context: "build", State: StatusSuccess},
				{ID: 2, Provider: "forgejo", Context: "test", State: StatusSuccess},
			},
			want: ChecksPass,
		},
		{
			name:     "missing requirement is pending",
			required: []CheckIdentity{{Provider: "forgejo", Context: "build"}},
			want:     ChecksPending,
		},
		{
			name:     "queued attempt is pending",
			required: []CheckIdentity{{Provider: "forgejo", Context: "build"}},
			statuses: []Status{{ID: 1, Provider: "forgejo", Context: "build", State: StatusQueued}},
			want:     ChecksPending,
		},
		{
			name:     "one failure fails the set",
			required: []CheckIdentity{{Provider: "forgejo", Context: "build"}, {Provider: "forgejo", Context: "test"}},
			statuses: []Status{{ID: 1, Provider: "forgejo", Context: "build", State: StatusPending}, {ID: 2, Provider: "forgejo", Context: "test", State: StatusTimeout}},
			want:     ChecksFail,
		},
		{
			name:     "owned Minos context is excluded exactly",
			required: []CheckIdentity{{Provider: "forgejo", Context: OwnedStatusContext}, {Provider: "forgejo", Context: "minos"}, {Provider: "other", Context: OwnedStatusContext}},
			statuses: []Status{{ID: 1, Provider: "forgejo", Context: OwnedStatusContext, State: StatusFailure}, {ID: 2, Provider: "forgejo", Context: "minos", State: StatusSuccess}, {ID: 3, Provider: "other", Context: OwnedStatusContext, State: StatusSuccess}},
			want:     ChecksPass,
		},
		{
			name:     "neutral passes only with forge protection satisfaction",
			required: []CheckIdentity{{Provider: "forgejo", Context: "optional"}},
			statuses: []Status{{ID: 1, Provider: "forgejo", Context: "optional", State: StatusNeutral, ProtectionSatisfied: true}},
			want:     ChecksPass,
		},
		{
			name:     "ambiguous newest attempt cannot pass",
			required: []CheckIdentity{{Provider: "forgejo", Context: "build"}},
			statuses: []Status{{ID: 8, Provider: "forgejo", Context: "build", State: StatusSuccess}, {ID: 8, Provider: "forgejo", Context: "build", State: StatusFailure}},
			want:     ChecksFail,
		},
		{
			name:     "identical duplicate newest attempt cannot pass",
			required: []CheckIdentity{{Provider: "forgejo", Context: "build"}},
			statuses: []Status{{ID: 8, Provider: "forgejo", Context: "build", State: StatusSuccess}, {ID: 8, Provider: "forgejo", Context: "build", State: StatusSuccess}},
			want:     ChecksFail,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ReduceRequiredChecks(test.required, test.statuses); got != test.want {
				t.Fatalf("ReduceRequiredChecks() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFailedRequiredChecksIncludesOnlyExplicitRedState(t *testing.T) {
	required := []CheckIdentity{
		{Provider: ForgejoProvider, Context: "failed"},
		{Provider: ForgejoProvider, Context: "pending"},
		{Provider: ForgejoProvider, Context: "ambiguous"},
		{Provider: ForgejoProvider, Context: "unknown"},
	}
	statuses := []Status{
		{ID: 4, Provider: ForgejoProvider, Context: "failed", State: StatusFailure},
		{ID: 3, Provider: ForgejoProvider, Context: "pending", State: StatusPending},
		{ID: 2, Provider: ForgejoProvider, Context: "ambiguous", State: StatusFailure},
		{ID: 2, Provider: ForgejoProvider, Context: "ambiguous", State: StatusFailure},
		{ID: 1, Provider: ForgejoProvider, Context: "unknown", State: StatusState("mystery")},
	}
	failed := FailedRequiredChecks(required, statuses)
	if len(failed) != 1 || failed[0].Context != "failed" {
		t.Fatalf("failed checks = %#v", failed)
	}
}
