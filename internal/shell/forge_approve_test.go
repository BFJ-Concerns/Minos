package shell

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

// A clean head approves only where Minos's own latest verdict requested
// changes, so the forge stops showing a block Minos no longer holds and a
// pull request Minos never blocked gets no approval.

func TestOwnChangesRequestedReadsOnlyTheServicesLatestVerdict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reviews []forge.Review
		want    bool
	}{
		{name: "no reviews"},
		{name: "own request for changes", reviews: []forge.Review{{ID: 1, User: "Minos", State: "REQUEST_CHANGES"}}, want: true},
		{name: "comment after own request for changes", reviews: []forge.Review{{ID: 1, User: "Minos", State: "REQUEST_CHANGES"}, {ID: 2, User: "Minos", State: "COMMENT"}}, want: true},
		{name: "approval after own request for changes", reviews: []forge.Review{{ID: 1, User: "Minos", State: "REQUEST_CHANGES"}, {ID: 2, User: "Minos", State: "APPROVED"}}},
		{name: "listed out of submission order", reviews: []forge.Review{{ID: 2, User: "Minos", State: "APPROVED"}, {ID: 1, User: "Minos", State: "REQUEST_CHANGES"}}},
		{name: "dismissed request for changes", reviews: []forge.Review{{ID: 1, User: "Minos", State: "REQUEST_CHANGES"}, {ID: 2, User: "Minos", State: "DISMISSED"}}},
		{name: "someone else's request for changes", reviews: []forge.Review{{ID: 1, User: "colleague", State: "REQUEST_CHANGES"}}},
		{name: "own approval then someone else's request", reviews: []forge.Review{{ID: 1, User: "Minos", State: "APPROVED"}, {ID: 2, User: "colleague", State: "REQUEST_CHANGES"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownChangesRequested(forge.Snapshot{AuthenticatedUser: "Minos", Reviews: tc.reviews}); got != tc.want {
				t.Fatalf("ownChangesRequested = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestForgeApprovePostsOneApprovalAfterTheServicesOwnBlock(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")
	t.Setenv("MINOS_LEAD_MODEL", "claude-opus-5-5")
	state.setReviews([]map[string]any{{
		"id": float64(1), "state": "REQUEST_CHANGES", "commit_id": "earlier-head",
		"body": "**Blocking**", "user": map[string]any{"login": "Minos"},
	}})
	var outputs []string
	for attempt := 0; attempt < 2; attempt++ {
		var out bytes.Buffer
		if err := ForgeCommand(t.Context(), []string{"approve", state.headSHA(), state.targetSHA()}, &out); err != nil {
			t.Fatalf("forge approve attempt %d: %v", attempt, err)
		}
		outputs = append(outputs, out.String())
	}
	writes, payload := state.reviewWriteFacts()
	if writes != 1 {
		t.Fatalf("review writes = %d, want one; outputs %q", writes, outputs)
	}
	body, _ := payload["body"].(string)
	// The record is how the sweep reads an approving review of the current
	// head as a clean marker, so the approval carries this head and target.
	want := cleanApprovalBody + "\n\nReviewed by: `claude-opus-5-5`.\n\n<!-- Minos: head=" + state.headSHA() + " target=" + state.targetSHA() + " -->"
	if body != want {
		t.Fatalf("approval body = %q, want %q", body, want)
	}
	var second map[string]string
	if err := json.Unmarshal([]byte(outputs[1]), &second); err != nil || second["outcome"] != "not-needed" {
		t.Fatalf("second approve printed %q", outputs[1])
	}
}

func TestForgeApproveWritesNothingWhereTheServiceNeverBlocked(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")
	state.setReviews([]map[string]any{{
		"id": float64(1), "state": "REQUEST_CHANGES", "commit_id": "earlier-head",
		"body": "Please split this.", "user": map[string]any{"login": "colleague"},
	}})
	var out bytes.Buffer
	if err := ForgeCommand(t.Context(), []string{"approve", state.headSHA(), state.targetSHA()}, &out); err != nil {
		t.Fatalf("forge approve: %v", err)
	}
	if writes, _ := state.reviewWriteFacts(); writes != 0 {
		t.Fatalf("review writes = %d, want none; printed %q", writes, out.String())
	}
}

// An approval whose write came back uncertain may still have reached the
// forge, and an approving review of the head is a clean marker to the
// sweep, so the command tries again before reporting failure; the guarded
// write skips a review already posted, so a retry never doubles it.
func TestForgeApproveTriesAgainWhileTheWriteIsUncertain(t *testing.T) {
	for _, tc := range []struct {
		name       string
		dropped    int
		wantWrites int
		wantErr    bool
	}{
		{name: "one lost write", dropped: 1, wantWrites: 1},
		{name: "every attempt lost", dropped: 3, wantWrites: 0, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts, delay := approvalAttempts, approvalRetryDelay
			approvalAttempts, approvalRetryDelay = 3, 0
			t.Cleanup(func() { approvalAttempts, approvalRetryDelay = attempts, delay })
			state := newForgejoFixtureState(t)
			cfg, _, _ := state.service(t)
			writeServiceConfig(t, cfg)
			t.Setenv("MINOS_CONFIG", cfg.Root)
			t.Setenv("MINOS_FORGE", "forgejo")
			t.Setenv("MINOS_OWNER", "minos-e2e-owner")
			t.Setenv("MINOS_REPO_NAME", "subject")
			t.Setenv("MINOS_PR", "1")
			state.setReviews([]map[string]any{{
				"id": float64(1), "state": "REQUEST_CHANGES", "commit_id": "earlier-head",
				"body": "**Blocking**", "user": map[string]any{"login": "Minos"},
			}})
			state.dropNextReviewWrites(tc.dropped)
			var out bytes.Buffer
			err := ForgeCommand(t.Context(), []string{"approve", state.headSHA(), state.targetSHA()}, &out)
			if (err != nil) != tc.wantErr {
				t.Fatalf("forge approve error = %v, want error %t; printed %q", err, tc.wantErr, out.String())
			}
			if writes, _ := state.reviewWriteFacts(); writes != tc.wantWrites {
				t.Fatalf("review writes = %d, want %d", writes, tc.wantWrites)
			}
			// The command prints one answer, the last attempt's.
			var printed forge.WriteResult
			decoder := json.NewDecoder(&out)
			if err := decoder.Decode(&printed); err != nil || decoder.More() {
				t.Fatalf("printed %q, want one result", out.String())
			}
			want := forge.WriteApplied
			if tc.wantErr {
				want = forge.WriteUncertain
			}
			if printed.Outcome != want {
				t.Fatalf("printed outcome %q, want %q", printed.Outcome, want)
			}
		})
	}
}
