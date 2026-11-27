package shell

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
)

func TestSweepRepairsSummaryOnlyCompletionOnBothForges(t *testing.T) {
	for _, provider := range []string{"forgejo", "github"} {
		t.Run(provider, func(t *testing.T) {
			var root string
			var guard forge.Guard
			var sawCleanMarker func() bool
			switch provider {
			case "forgejo":
				state := newForgejoFixtureState(t)
				cfg := writeSweepFixtureConfigWithKnobs(t, state, "[review]\ncommit-status = false\n")
				root = cfg.Root
				guard = forge.Guard{Repository: forge.Repository{Owner: "minos-e2e-owner", Name: "subject"},
					PullRequest: 1, HeadSHA: state.headSHA(), TargetSHA: state.targetSHA()}
				sawCleanMarker = func() bool {
					return slices.Contains(state.reactions, "+1") && len(state.statusReadFacts()) == 0 &&
						len(state.statusPostFacts()) == 0 && state.reviewWrites == 0
				}
			case "github":
				state := newGitHubFixtureState(t)
				state.pullRequest["draft"] = false
				state.pullRequests = []map[string]any{state.pullRequest}
				state.statuses, state.reviews = nil, nil
				root = t.TempDir()
				writeTestFile(t, filepath.Join(root, "service.toml"), fmt.Sprintf(`[service]
bot-login = %q
[listener]
bind = ":0"
[forges.github]
adaptation = %q
api-base = %q
credential-file = %q
webhook-secret-file = %q
[runs]
dir = %q
`, githubFixtureBotLogin, state.adaptationPath, state.server.URL, state.credentialPath, state.credentialPath, t.TempDir()))
				writeRepoConfig(t, root, "widgets.toml", `forge = "github"
owner = "acme"
repo = "widgets"
[adaptation]
run-body = "/opt/minos/run-body/run-body"
`)
				guard = state.guard()
				sawCleanMarker = func() bool {
					writes := state.operationWrites()
					return len(writes) == 2 && writes[1].path == "/repos/acme/widgets/issues/7/reactions" && writes[1].payload["content"] == "+1"
				}
			}
			t.Setenv("MINOS_CONFIG", root)
			t.Setenv("MINOS_FORGE", provider)
			t.Setenv("MINOS_OWNER", guard.Repository.Owner)
			t.Setenv("MINOS_REPO_NAME", guard.Repository.Name)
			t.Setenv("MINOS_PR", fmt.Sprint(guard.PullRequest))
			t.Setenv("MINOS_COMMIT_STATUS", "false")
			t.Setenv("MINOS_RESIDENT_LEAD", "true")
			run := t.TempDir()
			t.Setenv("MINOS_RUN_DIR", run)

			var stdout bytes.Buffer
			if err := ForgeCommand(t.Context(), []string{"status", guard.HeadSHA, guard.TargetSHA, "clean"}, &stdout); err != nil {
				t.Fatalf("close resident review: %v, %s", err, &stdout)
			}
			assertContainsFile(t, filepath.Join(run, appliedSummaryFile), "clean")
			if _, err := os.Stat(filepath.Join(run, appliedStatusFile)); !os.IsNotExist(err) {
				t.Fatalf("status recorded with commit statuses off: %v", err)
			}

			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name == "systemd-run" {
					t.Fatal("sweep started a run on the head recorded clean")
				}
				return nil, nil
			}
			if err := SweepCommand(t.Context(), []string{"-config", root}); err != nil {
				t.Fatal(err)
			}
			if !sawCleanMarker() {
				t.Fatal("sweep did not restore the clean marker from the summary alone")
			}
		})
	}
}

func TestDisabledStatusLeavesAnOldStatusOnlyHeadEligible(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, repo, facts := state.service(t)
	adapter, err := newBehaviouralForge(cfg, facts.Forge)
	if err != nil {
		t.Fatal(err)
	}
	guard := forge.Guard{Repository: forge.Repository{Owner: facts.Owner, Name: facts.Repo}, PullRequest: 1,
		HeadSHA: state.headSHA(), TargetSHA: state.targetSHA()}
	if result := adapter.SetProductStatus(t.Context(), guard, product.Clean()); result.Outcome != forge.WriteApplied {
		t.Fatalf("seed status: %#v", result)
	}
	repo.Review.CommitStatus = false
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(_ context.Context, _ string, _ ...string) ([]byte, error) { return nil, nil }
	result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
	if err != nil || result.Decision != SpawnStarted {
		t.Fatalf("disabled status left the head at %#v, %v", result, err)
	}
}

func TestForgeEarlierWithStatusesOffUsesReviewOrSummary(t *testing.T) {
	f := newEarlierHeadFixture(t)
	first := f.commit("feature.txt", "one\n", "feature")
	f.finish(first, "clean")
	second := f.commit("feature.txt", "two\n", "extend the feature")
	t.Setenv("MINOS_COMMIT_STATUS", "false")

	if got := f.earlier(second); got.Outcome != "none" {
		t.Fatalf("old status or its cache supplied a baseline: %#v", got)
	}

	f.state.setReviews([]map[string]any{{"id": 4, "state": "APPROVED", "commit_id": first, "user": map[string]any{"login": "Minos"}}})
	if got := f.earlier(second); got.Head != first || got.Outcome != "clean" {
		t.Fatalf("approving review supplied %#v", got)
	}
	f.state.setReviews(nil)
	f.summary(second, "changes-requested")
	third := f.commit("feature.txt", "three\n", "extend the feature again")
	if err := os.RemoveAll(filepath.Join(f.runsDir, ".reviewed")); err != nil {
		t.Fatal(err)
	}
	if got := f.earlier(third); got.Head != second || got.Outcome != "attention" {
		t.Fatalf("summary without its cache supplied %#v", got)
	}
}

func TestReviewBodyIncludesAdviceFromTheConfiguredRequestChangesRound(t *testing.T) {
	for _, threshold := range []string{"", "2"} {
		t.Run("threshold="+threshold, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			configureForgeCommandFixture(t, state)
			t.Setenv("MINOS_REQUEST_CHANGES_ROUNDS", threshold)
			state.setReviews([]map[string]any{
				{"id": 1, "state": "REQUEST_CHANGES", "commit_id": "foreign", "user": map[string]any{"login": "human"}},
				{"id": 2, "state": "REQUEST_CHANGES", "commit_id": "first", "user": map[string]any{"login": "Minos"}},
				{"id": 3, "state": "REQUEST_CHANGES", "commit_id": "first", "user": map[string]any{"login": "Minos"}},
				{"id": 4, "state": "COMMENT", "commit_id": "advice", "user": map[string]any{"login": "Minos"}},
				{"id": 5, "state": "APPROVED", "commit_id": "approved", "user": map[string]any{"login": "Minos"}},
			})
			body := filepath.Join(t.TempDir(), "review.md")
			writeTestFile(t, body, "The check accepts invalid input.")
			for index, head := range []string{"second", "third"} {
				state.changePullRequest(func(pr map[string]any) { pr["head"].(map[string]any)["sha"] = head })
				before, _ := state.reviewWriteFacts()
				for range 2 {
					if err := ForgeCommand(t.Context(), []string{"review", head, state.targetSHA(), "request-changes", body}, &bytes.Buffer{}); err != nil {
						t.Fatal(err)
					}
				}
				writes, payload := state.reviewWriteFacts()
				text := payload["body"].(string)
				wantAdvice := index == 1 || threshold == "2"
				if writes != before+1 || !strings.Contains(text, "The check accepts invalid input.") ||
					strings.Contains(text, fmt.Sprintf("round %d of requested changes", index+2)) != wantAdvice ||
					strings.Contains(text, "reconsidering the approach") != wantAdvice {
					t.Fatalf("head %s got %d posts, body %q, want advice %t", head, writes-before, text, wantAdvice)
				}
			}
		})
	}
}

func TestCommitStatusPublishesOnlyEnabledOutcomes(t *testing.T) {
	state := newForgejoFixtureState(t)
	configureForgeCommandFixture(t, state)

	run := func(name string) {
		t.Helper()
		var stdout bytes.Buffer
		if err := ForgeCommand(t.Context(), []string{"status", state.headSHA(), state.targetSHA(), name}, &stdout); err != nil {
			t.Fatalf("write %s: %v (%s)", name, err, &stdout)
		}
	}
	for _, name := range []string{"working", "continuation", "clean", "attention", "incomplete"} {
		run(name)
	}
	posts := state.statusPostFacts()
	if len(posts) != 3 || posts[0].Payload["state"] != "success" || posts[1].Payload["state"] != "failure" || posts[2].Payload["state"] != "error" {
		t.Fatalf("enabled status posts = %#v, want clean, attention and incomplete", posts)
	}
	t.Setenv("MINOS_COMMIT_STATUS", "false")
	for _, name := range []string{"clean", "attention", "incomplete"} {
		run(name)
	}
	if posts = state.statusPostFacts(); len(posts) != 3 {
		t.Fatalf("disabled status added a write: %#v", posts)
	}
}

func TestStalledContinuationStaysStoppedWithoutAStatus(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, repo, facts := state.service(t)
	repo.Review.CommitStatus = false
	facts.HeadSHA, facts.BaseSHA = state.headSHA(), state.targetSHA()
	state.reactions = []string{"eyes"}
	progress := handoffProgress{Stage: "review", Head: facts.HeadSHA}
	_, path := writeProgressHandoff(t, cfg, facts, progress, &progress)

	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	var starts int
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemd-run" {
			starts++
		}
		return nil, nil
	}
	for range 2 {
		outcome, err := SpawnRun(t.Context(), cfg, repo, facts)
		if err != nil || outcome.Outcome != SpawnAttention {
			t.Fatalf("stalled continuation outcome = %#v, %v", outcome, err)
		}
	}
	held, err := readRunHandoff(path, facts)
	if err != nil || !held.Stalled || starts != 0 || len(state.statusPostFacts()) != 0 || slices.Contains(state.reactions, "eyes") {
		t.Fatalf("stalled handoff = %#v, %v, starts = %d, forge writes = %v", held, err, starts, state.writeSequence)
	}
	facts.reviewRequested = true
	outcome, err := SpawnRun(t.Context(), cfg, repo, facts)
	if err != nil || outcome.Outcome != SpawnStarted || starts != 1 {
		t.Fatalf("explicit review after stall = %#v, %v, starts = %d", outcome, err, starts)
	}
}
