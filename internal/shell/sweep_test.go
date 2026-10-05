package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
)

func TestSweepDecisionMessage(t *testing.T) {
	facts := Facts{Owner: "owner", Repo: "repository", PR: "12"}
	tests := []struct {
		name   string
		result ReconcileResult
		want   string
	}{
		{name: "started", result: ReconcileResult{Decision: SpawnStarted}, want: "started owner/repository#12"},
		{name: "deferred", result: ReconcileResult{Decision: "deferred: open dependencies: owner/prerequisite#7"}, want: "owner/repository#12: deferred: open dependencies: owner/prerequisite#7"},
		{name: "work in progress", result: ReconcileResult{Decision: `deferred: work-in-progress branch "structural/rework"`}, want: `owner/repository#12: deferred: work-in-progress branch "structural/rework"`},
		{name: "suppressed duplicate", result: ReconcileResult{Decision: SpawnSuppressed, BlockingUnit: "minos-run-owner-repository-pr12.service"}, want: "owner/repository#12: suppressed by active unit minos-run-owner-repository-pr12.service"},
		{name: "suppressed concurrency cap", result: ReconcileResult{Decision: SpawnSuppressed, Detail: "all 2/2 run slots are occupied by active units minos-run-owner-other-pr9.service, minos-run-owner-third-pr4.service"}, want: "owner/repository#12: suppressed because all 2/2 run slots are occupied by active units minos-run-owner-other-pr9.service, minos-run-owner-third-pr4.service"},
		{name: "continued", result: ReconcileResult{Decision: SpawnContinued}, want: "owner/repository#12: continued previous run"},
		{name: "nothing", result: ReconcileResult{Decision: ReconcileNothing}, want: "owner/repository#12: nothing to do"},
		{name: "attention", result: ReconcileResult{Decision: SpawnAttention, Detail: `successor made no progress beyond stage "review" and published no new head or review`}, want: `owner/repository#12: attention: stopped stalled continuation because its successor made no progress beyond stage "review" and published no new head or review`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sweepDecisionMessage(facts, test.result); got != test.want {
				t.Fatalf("message = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPublishSweepDeferralsReplacesThePreviousDocumentAtomically(t *testing.T) {
	cfg := scratchTestConfig(t)
	stale := sweepDeferralDocument{Kind: sweepDeferralDocumentKind, CompletedAt: "2001-02-03T04:05:06Z", Deferrals: []sweepDeferral{{PR: "old", Reason: "stale"}}}
	staleJSON, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, sweepDeferralsPath(cfg), string(staleJSON))
	want := []sweepDeferral{{Forge: "forgejo", Owner: "owner", Repo: "repository", PR: "12", Reason: "deferred"}}
	originalRename := renameSweepDeferrals
	t.Cleanup(func() { renameSweepDeferrals = originalRename })
	var renamed bool
	renameSweepDeferrals = func(temporary, destination string) error {
		content, err := os.ReadFile(temporary)
		if err != nil {
			return err
		}
		var beforeRename sweepDeferralDocument
		if err := json.Unmarshal(content, &beforeRename); err != nil {
			return err
		}
		if len(beforeRename.Deferrals) != len(want) {
			t.Fatalf("temporary document deferrals = %#v, want %#v", beforeRename.Deferrals, want)
		}
		renamed = true
		return originalRename(temporary, destination)
	}

	if err := publishSweepDocument(cfg, sweepDeferralDocument{Deferrals: want}); err != nil {
		t.Fatal(err)
	}
	if !renamed {
		t.Fatal("sweep document was not published through rename")
	}
	content, err := os.ReadFile(sweepDeferralsPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	var got sweepDeferralDocument
	if err := json.Unmarshal(content, &got); err != nil {
		t.Fatal(err)
	}
	if got.Kind != sweepDeferralDocumentKind || got.CompletedAt == stale.CompletedAt {
		t.Fatalf("replacement = %#v, want a new completed sweep document", got)
	}
	if len(got.Deferrals) != len(want) {
		t.Fatalf("replacement deferrals = %#v, want %#v", got.Deferrals, want)
	}
	for index, deferral := range want {
		if !reflect.DeepEqual(got.Deferrals[index], deferral) {
			t.Fatalf("replacement deferral %d = %#v, want %#v", index, got.Deferrals[index], deferral)
		}
	}
}

func TestSweepRecordsEachAdmissionDeferralFromACompletedPass(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*forgejoFixtureState, ServiceConfig, Facts)
		wantReason string
	}{
		{
			name: "work in progress branch",
			configure: func(state *forgejoFixtureState, cfg ServiceConfig, _ Facts) {
				setSweepFixtureWorkInProgressPrefix(t, cfg, "structural/")
				state.changePullRequest(func(pullRequest map[string]any) {
					pullRequest["head"].(map[string]any)["ref"] = "structural/rework"
				})
			},
			wantReason: `work-in-progress branch "structural/rework"`,
		},
		{
			name: "open dependency",
			configure: func(state *forgejoFixtureState, _ ServiceConfig, _ Facts) {
				state.setDependencies([]map[string]any{{
					"number": 7, "state": "open",
					"repository": map[string]any{"full_name": "minos-e2e-owner/prerequisite"},
				}})
			},
			wantReason: "open dependencies: minos-e2e-owner/prerequisite#7",
		},
		{
			name: "terminal Minos review with its status",
			configure: func(state *forgejoFixtureState, _ ServiceConfig, _ Facts) {
				state.setReviews([]map[string]any{{
					"id": 41, "state": "APPROVED", "commit_id": state.headSHA(),
					"body": "completed review", "user": map[string]any{"login": "Minos"},
				}})
				state.setStatuses([]map[string]any{{
					"id": 7, "context": "Minos", "status": "success", "description": product.Clean().Description(),
					"creator": map[string]any{"login": "Minos"},
				}})
			},
			wantReason: "completed-marker",
		},
		{
			name: "terminal Minos review without a status",
			configure: func(state *forgejoFixtureState, _ ServiceConfig, _ Facts) {
				state.setReviews([]map[string]any{{
					"id": 41, "state": "APPROVED", "commit_id": state.headSHA(),
					"body": "completed review", "user": map[string]any{"login": "Minos"},
				}})
			},
			wantReason: "completed-marker",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			cfg := writeSweepFixtureConfig(t, state)
			_, _, facts := state.service(t)
			test.configure(state, cfg, facts)

			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name == "systemd-run" {
					t.Fatalf("deferred pull request started a run")
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
			if len(document.Deferrals) != 1 || document.Deferrals[0].Reason != test.wantReason {
				t.Fatalf("recorded deferrals = %#v, want one with reason %q", document.Deferrals, test.wantReason)
			}
		})
	}
}

func TestSweepContinuesWhenDeferralProjectionCannotBePublished(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg := writeSweepFixtureConfig(t, state)
	setSweepFixtureWorkInProgressPrefix(t, cfg, "structural/")
	state.changePullRequest(func(pullRequest map[string]any) {
		pullRequest["head"].(map[string]any)["ref"] = "structural/rework"
	})
	originalRename := renameSweepDeferrals
	t.Cleanup(func() { renameSweepDeferrals = originalRename })
	renameSweepDeferrals = func(_, _ string) error { return os.ErrPermission }
	originalCommand := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = originalCommand })
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "systemd-run" {
			t.Fatalf("deferred pull request started a run")
		}
		return nil, nil
	}

	if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
		t.Fatalf("projection write failed the completed sweep: %v", err)
	}
}

func setSweepFixtureWorkInProgressPrefix(t *testing.T, cfg ServiceConfig, prefix string) {
	t.Helper()
	path := filepath.Join(cfg.Root, "repos", "subject.toml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := bytes.Replace(content, []byte("repo = \"subject\"\n"), []byte("repo = \"subject\"\nwork-in-progress-branch-prefixes = [\""+prefix+"\"]\n"), 1)
	if bytes.Equal(updated, content) {
		t.Fatal("sweep fixture repo configuration has no repository field")
	}
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSweepDecisionMessageCoversReachableVocabulary(t *testing.T) {
	facts := Facts{Owner: "owner", Repo: "repository", PR: "12"}
	results := []ReconcileResult{{Decision: ReconcileDecision(deferredDecisionPrefix + "reason")}}
	for _, decision := range declaredReconcileDecisions(t) {
		result := ReconcileResult{Decision: decision}
		switch decision {
		case SpawnSuppressed:
			result.BlockingUnit = "minos-run-other-repo-pr9.service"
		case SpawnAttention:
			result.Detail = `successor made no progress beyond stage "review" and published no new head or review`
		}
		results = append(results, result)
	}
	for _, result := range results {
		if got := sweepDecisionMessage(facts, result); got == "" {
			t.Errorf("decision %q returned an empty message", result.Decision)
		}
	}
}

func declaredReconcileDecisions(t *testing.T) []ReconcileDecision {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var decisions []ReconcileDecision
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		typeName, ok := spec.Type.(*ast.Ident)
		if !ok || typeName.Name != "ReconcileDecision" {
			return true
		}
		literal, ok := spec.Values[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			t.Fatalf("ReconcileDecision %s does not have a string literal value", spec.Names[0].Name)
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Fatalf("parse ReconcileDecision %s: %v", spec.Names[0].Name, err)
		}
		decisions = append(decisions, ReconcileDecision(value))
		return true
	})
	if len(decisions) == 0 {
		t.Fatal("service.go declares no ReconcileDecision constants")
	}
	return decisions
}

func TestTerminalPullRequestExpiresHandoffBeforeFollowingResidueSweep(t *testing.T) {
	originalCommand := commandCombinedOutput
	originalSnapshot := inspectHandoffSnapshot
	t.Cleanup(func() {
		commandCombinedOutput = originalCommand
		inspectHandoffSnapshot = originalSnapshot
	})
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	inspectHandoffSnapshot = func(context.Context, ServiceConfig, Facts) (forge.Snapshot, error) {
		return forge.Snapshot{State: "closed"}, nil
	}
	cfg := scratchTestConfig(t)
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repository", PR: "21", HeadSHA: "head"}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	writeTestFile(t, filepath.Join(runDir, "workspace", ".git", "HEAD"), "fixture\n")
	handoff := writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA)

	if err := sweepRunResidue(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if err := expireInactiveRunHandoffs(t.Context(), cfg, []RepoConfig{{Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handoff); !os.IsNotExist(err) {
		t.Fatalf("terminal pull request handoff still exists: %v", err)
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("terminal pull request residue was removed in the same pass: %v", err)
	}
	if err := sweepRunResidue(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("terminal pull request residue survived the following pass: %v", err)
	}
}

func TestOpenPullRequestMissingFromListingKeepsHandoff(t *testing.T) {
	originalSnapshot := inspectHandoffSnapshot
	t.Cleanup(func() { inspectHandoffSnapshot = originalSnapshot })
	inspectHandoffSnapshot = func(context.Context, ServiceConfig, Facts) (forge.Snapshot, error) {
		return forge.Snapshot{State: "open"}, nil
	}
	cfg := scratchTestConfig(t)
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "repository", PR: "22", HeadSHA: "head"}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	writeTestFile(t, filepath.Join(runDir, "workspace", ".git", "HEAD"), "fixture\n")
	handoff := writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA)

	if err := expireInactiveRunHandoffs(t.Context(), cfg, []RepoConfig{{Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handoff); err != nil {
		t.Fatalf("open pull request missing from listing lost its handoff: %v", err)
	}
}

func TestUnconfiguredRepositoryExpiresHandoff(t *testing.T) {
	cfg := scratchTestConfig(t)
	facts := Facts{Forge: "forgejo", Owner: "owner", Repo: "removed-repository", PR: "23", HeadSHA: "head"}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	writeTestFile(t, filepath.Join(runDir, "workspace", ".git", "HEAD"), "fixture\n")
	handoff := writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA)

	if err := expireInactiveRunHandoffs(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handoff); !os.IsNotExist(err) {
		t.Fatalf("unconfigured repository handoff still exists: %v", err)
	}
}

func TestOrderSweepCandidatesInterleavesReposWithinEachPriorityClass(t *testing.T) {
	candidate := func(repo, pr string, priority int) sweepCandidate {
		return sweepCandidate{facts: Facts{Owner: "owner", Repo: repo, PR: pr}, priority: priority}
	}
	ordered := orderSweepCandidates([]sweepCandidate{
		// The forge lists newest first; the sweep must serve each repo's
		// queue oldest first regardless.
		candidate("busy", "3", 1),
		candidate("busy", "2", 1),
		candidate("busy", "1", 1),
		candidate("starved", "8", 1),
		candidate("quiet", "4", 1),
		candidate("busy", "5", 0),
	}, nil)
	var got []string
	for _, entry := range ordered {
		got = append(got, entry.facts.Repo+"#"+entry.facts.PR)
	}
	want := []string{"busy#5", "busy#1", "quiet#4", "starved#8", "busy#2", "busy#3"}
	if !slices.Equal(got, want) {
		t.Fatalf("ordered candidates = %v, want %v", got, want)
	}
}

func TestOrderSweepCandidatesYieldsTheLaneToReposHoldingFewerLiveRuns(t *testing.T) {
	candidate := func(repo, pr string, priority int) sweepCandidate {
		return sweepCandidate{facts: Facts{Owner: "owner", Repo: repo, PR: pr}, priority: priority}
	}
	// The busy repo owns the lowest pull request number, so without live-run
	// counts it would lead every pass and claim each freed slot; its two
	// active units must push it behind the repos holding none.
	ordered := orderSweepCandidates([]sweepCandidate{
		candidate("busy", "1", 1),
		candidate("busy", "2", 1),
		candidate("starved", "8", 1),
		candidate("quiet", "4", 1),
	}, []string{
		"minos-run-owner-busy-pr90.service",
		"minos-run-owner-busy-pr104.service",
		"minos-run-other-owner-unrelated-pr7.service",
	})
	var got []string
	for _, entry := range ordered {
		got = append(got, entry.facts.Repo+"#"+entry.facts.PR)
	}
	want := []string{"quiet#4", "starved#8", "busy#1", "busy#2"}
	if !slices.Equal(got, want) {
		t.Fatalf("ordered candidates = %v, want %v", got, want)
	}
}

func TestSweepSkipsAnUnreadableRepoAndStillSweepsTheRest(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg := writeSweepFixtureConfig(t, state)
	vanished := `forge = "forgejo"
owner = "minos-e2e-owner"
repo = "vanished"
[adaptation]
run-body = "/opt/minos/run-body/run-body"
`
	// Sorts before subject.toml, so the unreadable repo is met first and a
	// pass-wide abort would leave the healthy repo unswept.
	if err := os.WriteFile(filepath.Join(cfg.Root, "repos", "aaa-vanished.toml"), []byte(vanished), 0o600); err != nil {
		t.Fatal(err)
	}

	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	var commands []string
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		commands = append(commands, name)
		return nil, nil
	}

	if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
		t.Fatalf("a single unreadable repo failed the pass: %v", err)
	}
	if got := state.pullRequestSnapshotReads("1"); got != 1 {
		t.Fatalf("healthy repo snapshot reads this pass = %d, want one", got)
	}
	if !slices.Contains(commands, "systemd-run") {
		t.Fatalf("healthy repo was not reconciled; commands = %v", commands)
	}
}
