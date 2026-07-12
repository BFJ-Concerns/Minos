package preflight

import "testing"

func TestClassifyBackendFailure(t *testing.T) {
	tests := []struct {
		name  string
		input BackendFailure
		want  FailureKind
	}{
		{
			name:  "stale shared subscription oauth",
			input: BackendFailure{StatusCode: 403, Message: "organization has disabled Claude subscription access", Immediate: true, CostUSD: 0},
			want:  FailureStaleOAuth,
		},
		{
			name:  "quota",
			input: BackendFailure{StatusCode: 429, Message: "usage limit reached", Immediate: true, CostUSD: 0},
			want:  FailureQuota,
		},
		{
			name:  "genuinely disabled",
			input: BackendFailure{StatusCode: 403, Message: "account disabled by organization administrator", Immediate: true, CostUSD: 0},
			want:  FailureAccountDisabled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyBackendFailure(test.input)
			if got.Kind != test.want {
				t.Fatalf("got %q, want %q", got.Kind, test.want)
			}
			if got.Kind == FailureStaleOAuth && got.Recovery == "" {
				t.Fatal("stale OAuth classification omitted recovery route")
			}
		})
	}
}
