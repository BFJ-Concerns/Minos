package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
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

func TestContinuationSuccessorSetupDeathKeepsSweepPriority(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, facts := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)
	t.Setenv("MINOS_PR", facts.PR)
	facts.HeadSHA, facts.BaseSHA = state.headSHA(), state.targetSHA()
	var stdout strings.Builder
	if err := ForgeCommand(t.Context(), []string{"status", facts.HeadSHA, facts.BaseSHA, "continuation"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if priority, err := currentContinuationPriority(t.Context(), cfg, facts); err != nil || priority != 0 {
		t.Fatalf("predecessor continuation priority = %d, err=%v", priority, err)
	}

	// Drive the real setup failure. The stand-in transports the command's
	// arguments; only ForgeCommand and the real adaptation write forge state.
	fixture := newRunBodyFixture(t)
	calls := filepath.Join(fixture.root, "setup-status-command")
	writer := filepath.Join(fixture.root, "record-forge-command")
	writeScript(t, writer, fmt.Sprintf("#!/usr/bin/env sh\nfor argument do printf '%%s\\n' \"$argument\"; done >%s\n", strconv.Quote(calls)))
	setup := filepath.Join(fixture.root, "failing-setup")
	writeScript(t, setup, "#!/usr/bin/env sh\nexit 19\n")
	fixture.appendConfig(t, map[string]string{"MINOS_BIN": writer, "MINOS_SETUP_WORKSPACE": setup})
	copyFixtureFile(t, filepath.Join(cfg.Root, "service.toml"), filepath.Join(fixture.configRoot, "service.toml"), 0o600)
	output, err := fixture.execute(map[string]string{
		"MINOS_FORGE": facts.Forge, "MINOS_OWNER": facts.Owner,
		"MINOS_REPO_NAME": facts.Repo, "MINOS_PR": facts.PR,
		"MINOS_HEAD_SHA": facts.HeadSHA, "MINOS_TARGET_SHA": facts.BaseSHA,
	})
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 19 {
		t.Fatalf("setup successor exit = %v, want 19\n%s", err, output)
	}
	recorded, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")
	if len(args) < 2 || args[0] != "forge" {
		t.Fatalf("setup death forge command = %q", recorded)
	}
	if err := ForgeCommand(t.Context(), args[1:], &stdout); err != nil {
		t.Fatalf("publish setup successor death: %v\n%s", err, stdout.String())
	}
	if posts := state.statusPostFacts(); len(posts) != 2 {
		t.Fatalf("status posts = %#v, want continuation then setup death", posts)
	}
	snapshot, err := currentSnapshot(t.Context(), cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	latest, found := latestOwnedStatus(snapshot, cfg.Service.BotLogin, cfg.Service.StatusContext)
	if !found || latest.State != forge.StatusError {
		t.Fatalf("setup death status = %#v, found=%t", latest, found)
	}
	if latest.Description != "Review incomplete: setup failed at workspace-setup" {
		t.Errorf("setup death description = %q, want stage-bearing incomplete status", latest.Description)
	}
	classified, recognised := product.StateForDescription(latest.Description)
	if !recognised || classified != product.Incomplete() {
		t.Errorf("setup death classification = %q, recognised=%t, want incomplete", classified.Name(), recognised)
	}
	priority, err := currentContinuationPriority(t.Context(), cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	if priority != 0 {
		t.Errorf("setup-dead continuation successor priority = %d, want 0", priority)
	}
	fresh := Facts{Owner: facts.Owner, Repo: "fresh", PR: facts.PR}
	ordered := orderSweepCandidates([]sweepCandidate{{facts: fresh, priority: 1}, {facts: facts, priority: priority}}, nil)
	if ordered[0].facts != facts {
		t.Errorf("front of sweep queue = %s, want setup-dead successor %s", ordered[0].facts.RepoSlug(), facts.RepoSlug())
	}
}
