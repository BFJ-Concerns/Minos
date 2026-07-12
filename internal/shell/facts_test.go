package shell

import (
	"strings"
	"testing"
)

func TestParseFactsAllowUnmappedRequirementsFollowOccasion(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:  "unmapped event needs no pull request facts",
			input: "OCCASION=\nOWNER=owner\nREPO=subject\nPR=\n",
		},
		{
			name:    "mapped event retains pull request requirements",
			input:   "OCCASION=pr-opened\nOWNER=owner\nREPO=subject\nPR=\n",
			wantErr: true,
		},
		{
			name:    "malformed normaliser output remains invalid",
			input:   "this is not a key-value line\n",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseFactsAllowUnmapped(strings.NewReader(test.input))
			if (err != nil) != test.wantErr {
				t.Fatalf("ParseFactsAllowUnmapped() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}
