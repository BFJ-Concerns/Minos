package shell

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
)

func TestSweepRecordsDraftDeferrals(t *testing.T) {
	for _, scenario := range []string{"draft", "draft with dependency", "draft with every reason"} {
		t.Run(scenario, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			cfg := writeSweepFixtureConfigWithKnobs(t, state, labelMarkerKnobs)
			state.changePullRequest(func(pr map[string]any) { pr["draft"] = true })
			defineMarkerLabels(state)
			state.setPullRequestLabels(state.repositoryLabels[2:])
			state.setReviews([]map[string]any{{
				"id": 9, "state": "APPROVED", "commit_id": "previous-head",
				"user": map[string]any{"login": "Minos"},
			}})
			wantReasons := []string{"draft"}
			if scenario == "draft with every reason" {
				setSweepFixtureWorkInProgressPrefix(t, cfg, "structural/")
				state.changePullRequest(func(pr map[string]any) { pr["head"].(map[string]any)["ref"] = "structural/work" })
				state.setStatuses([]map[string]any{{"id": 7, "context": "Minos", "status": "success", "description": product.Clean().Description(), "creator": map[string]any{"login": "Minos"}}})
				wantReasons = append(wantReasons, `work-in-progress branch "structural/work"`, "completed-marker")
			}
			if scenario != "draft" {
				state.setDependencies([]map[string]any{{"number": 7, "state": "open", "repository": map[string]any{"full_name": "owner/prerequisite"}}})
				wantReasons = append(wantReasons, "open dependencies: owner/prerequisite#7")
			}
			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name == "systemd-run" {
					t.Fatal("draft pull request started a run")
				}
				return nil, nil
			}
			if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(sweepDeferralsPath(cfg))
			if err != nil {
				t.Fatal(err)
			}
			var document sweepDeferralDocument
			if err := json.Unmarshal(content, &document); err != nil {
				t.Fatal(err)
			}
			if len(document.Deferrals) != 1 || document.Deferrals[0].Reason != "draft" {
				t.Fatalf("recorded deferrals = %#v, want one with reason draft", document.Deferrals)
			}
			if got := document.Deferrals[0].Reasons; !reflect.DeepEqual(got, wantReasons) {
				t.Fatalf("draft reasons = %#v, want %#v", got, wantReasons)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			wantWrites := []string{"label-remove:minos/approved", "label-remove:minos/attention"}
			if scenario == "draft with every reason" {
				wantWrites = wantWrites[1:] // The clean marker still stands for the current head.
			}
			if !reflect.DeepEqual(state.writeSequence, wantWrites) {
				t.Fatalf("draft marker writes = %v, want %v", state.writeSequence, wantWrites)
			}
			if state.reviewRequestWrites != 0 || state.obsoleteAssignmentWrites != 0 || state.reactionWrites != 0 || state.reactionDeleteWrites != 0 || state.statusWrites != 0 || state.reviewWrites != 0 || len(state.issueCreateRequests) != 0 {
				t.Fatalf("draft caused forge writes: sequence=%v reactions=%d/%d labels=%d statuses=%d reviews=%d requests=%d assignments=%d issues=%d", state.writeSequence, state.reactionWrites, state.reactionDeleteWrites, state.labelWrites, state.statusWrites, state.reviewWrites, state.reviewRequestWrites, state.obsoleteAssignmentWrites, len(state.issueCreateRequests))
			}
		})
	}
}

func TestSweepRemovesStaleDefaultReactionAfterDraftHeadMoves(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg := writeSweepFixtureConfig(t, state)
	state.reactions = []string{"+1"}
	state.setReviews([]map[string]any{{
		"id": 9, "state": "APPROVED", "commit_id": state.headSHA(),
		"user": map[string]any{"login": "Minos"},
	}})
	state.changePullRequest(func(pr map[string]any) {
		pr["draft"] = true
		pr["head"].(map[string]any)["sha"] = "draft-head-after-approval"
	})
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemd-run" {
			t.Fatal("draft pull request started a run")
		}
		return nil, nil
	}

	if err := SweepCommand(t.Context(), []string{"--config", cfg.Root}); err != nil {
		t.Fatal(err)
	}
	document := projectionJSON(t, sweepDeferralsPath(cfg))
	if row := projectionRow(t, document, "deferrals"); row["reason"] != "draft" {
		t.Fatalf("sweep deferral = %#v, want draft", row)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.reactions) != 0 || !reflect.DeepEqual(state.writeSequence, []string{"reaction-remove:+1"}) {
		t.Fatalf("draft reactions = %v, forge writes = %v, want only stale approval removed", state.reactions, state.writeSequence)
	}
}

func TestSweepStartsDraftWhenMarkedReady(t *testing.T) {
	for _, test := range []struct {
		name                 string
		markDraft, markReady func(map[string]any)
	}{
		{
			name:      "native draft flag",
			markDraft: func(pr map[string]any) { pr["draft"] = true },
			markReady: func(pr map[string]any) { pr["draft"] = false },
		},
		{
			name:      "Forgejo WIP title with draft flag false",
			markDraft: func(pr map[string]any) { pr["title"], pr["draft"] = "WIP: change", false },
			markReady: func(pr map[string]any) { pr["title"] = "change" },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			cfg := writeSweepFixtureConfig(t, state)
			state.changePullRequest(test.markDraft)
			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			starts := 0
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name == "systemd-run" {
					starts++
				}
				return nil, nil
			}
			if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
				t.Fatal(err)
			}
			doc := projectionJSON(t, sweepDeferralsPath(cfg))
			if row := projectionRow(t, doc, "deferrals"); row["reason"] != "draft" || starts != 0 {
				t.Fatalf("draft pass: row=%#v starts=%d", row, starts)
			}
			state.changePullRequest(test.markReady)
			if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
				t.Fatal(err)
			}
			doc = projectionJSON(t, sweepDeferralsPath(cfg))
			if rows, ok := doc["deferrals"].([]any); !ok || len(rows) != 0 {
				t.Fatalf("ready pass retained deferrals: %#v", doc["deferrals"])
			}
			if row := projectionRow(t, doc, "readied"); row["decision"] != string(SpawnStarted) || row["cause"] != "current forge head is eligible" || starts != 1 {
				t.Fatalf("ready pass: row=%#v starts=%d", row, starts)
			}
		})
	}
}

func TestClosedOrMergedDraftHasNoDeferral(t *testing.T) {
	for _, snapshot := range []forge.Snapshot{{State: "closed", Draft: true}, {State: "open", Merged: true, Draft: true}} {
		var cfg ServiceConfig
		cfg.Runs.Dir = t.TempDir()
		result, err := reconcilePullRequestSnapshot(t.Context(), cfg, RepoConfig{}, Facts{}, nil, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision != ReconcileNothing || result.DeferralReason != "" || len(result.DeferralReasons) != 0 {
			t.Errorf("closed/merged draft result = %#v, want nothing without deferral", result)
		}
	}
}
