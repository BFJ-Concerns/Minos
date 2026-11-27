package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
)

func TestParseCommentCommandReadsTheFirstLineAddressedToTheService(t *testing.T) {
	for _, tc := range []struct {
		body  string
		login string
		want  commentCommand
		ok    bool
	}{
		{"@Minos review", "Minos", commandReview, true},
		{"\n  @minos   FULL  review \nthe rest is mine", "Minos", commandFullReview, true},
		{"@minos skip", "Minos", commandSkip, true},
		{"@minos-review skip", "minos-review[bot]", commandSkip, true},
		{"@minos-review[bot] review", "minos-review[bot]", commandReview, true},
		{"@minos review this please", "Minos", "", false},
		{"please @minos review", "Minos", "", false},
		{"thanks\n@minos review", "Minos", "", false},
		{"@minosx review", "Minos", "", false},
		{"@minos", "Minos", "", false},
		{"minos review", "Minos", "", false},
	} {
		got, ok := parseCommentCommand(tc.body, tc.login)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseCommentCommand(%q, %q) = %q, %t; want %q, %t", tc.body, tc.login, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCommandedOutcomesNeverLeaveNoReview(t *testing.T) {
	all := []string{"no-review", "increment", "full"}
	if got := commandedOutcomes(all, commandEffect{reviewRequested: true}); !slices.Equal(got, []string{"increment", "full"}) {
		t.Fatalf("review outcomes = %v", got)
	}
	if got := commandedOutcomes(all, commandEffect{reviewRequested: true, fullOnly: true}); !slices.Equal(got, []string{"full"}) {
		t.Fatalf("full review outcomes = %v", got)
	}
	if !slices.Equal(all, []string{"no-review", "increment", "full"}) {
		t.Fatalf("configured outcomes were changed in place: %v", all)
	}
}

// commandJourney is a GitHub pull request reconciled through the service's
// admission, with every systemd-run invocation recorded.
type commandJourney struct {
	state    *githubFixtureState
	adapter  *forge.Adapter
	cfg      ServiceConfig
	repo     RepoConfig
	facts    Facts
	launches [][]string
	// live reports the pull request's own run unit as active.
	live bool
	// onLaunch, when set, runs as a run is launched, before admission reads
	// anything back.
	onLaunch func()
}

func newCommandJourney(t *testing.T) *commandJourney {
	t.Helper()
	state := newGitHubFixtureState(t)
	state.pullRequest["draft"] = false
	state.permissions = map[string]string{"maintainer": "write", "reader": "read"}
	journey := &commandJourney{state: state}
	journey.cfg = ServiceConfig{Root: t.TempDir()}
	journey.cfg.Runs.Dir = t.TempDir()
	setTestRunCeilings(&journey.cfg)
	journey.cfg.Service.BotLogin = githubFixtureBotLogin
	journey.cfg.Service.StatusContext = "Minos"
	journey.cfg.Forges = map[string]ForgeConfig{"github": {Adaptation: state.adaptationPath, APIBase: state.server.URL, CredentialFile: state.credentialPath}}
	journey.repo = RepoConfig{Forge: "github", Owner: "acme", Repo: "widgets"}
	journey.repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"
	journey.repo.PushInspection.Outcomes = []string{"no-review", "increment", "full"}
	journey.facts = Facts{Forge: "github", Owner: "acme", Repo: "widgets", PR: "7"}
	adapter, err := newBehaviouralForge(journey.cfg, "github")
	if err != nil {
		t.Fatal(err)
	}
	journey.adapter = adapter
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "systemd-run":
			journey.launches = append(journey.launches, args)
			if journey.onLaunch != nil {
				journey.onLaunch()
			}
		case name == "systemctl" && slices.Contains(args, "list-units") && journey.live:
			return []byte(UnitName(journey.facts) + ".service loaded active running Minos run\n"), nil
		}
		return nil, nil
	}
	return journey
}

// comment writes a conversation comment at the fixture's next tick and
// returns its id.
func (j *commandJourney) comment(user, body string) int64 {
	j.state.mu.Lock()
	defer j.state.mu.Unlock()
	id := int64(5000 + len(j.state.issueComments))
	j.state.issueComments = append(j.state.issueComments, map[string]any{
		"id": id, "created_at": j.state.tick(), "user": map[string]any{"login": user}, "body": body,
	})
	return id
}

func (j *commandJourney) status(t *testing.T, state product.State) {
	t.Helper()
	githubApplied(t, j.adapter.SetProductStatus(t.Context(), j.state.guard(), state))
}

// moveHead pushes a new head, the current head's statuses staying with
// their commit.
func (j *commandJourney) moveHead(head string) {
	j.state.mu.Lock()
	defer j.state.mu.Unlock()
	current := j.state.pullRequest["head"].(map[string]any)["sha"].(string)
	if j.state.statusesAt == nil {
		j.state.statusesAt = map[string][]map[string]any{}
	}
	j.state.statusesAt[current] = j.state.statuses
	j.state.statuses = j.state.statusesAt[head]
	delete(j.state.statusesAt, head)
	j.state.pullRequest["head"].(map[string]any)["sha"] = head
}

// finishClean writes what a clean run leaves: its working status, then its
// clean status and approving review.
func (j *commandJourney) finishClean(t *testing.T) {
	t.Helper()
	j.status(t, product.Working())
	j.status(t, product.Clean())
	githubApplied(t, j.adapter.PostReview(t.Context(), j.state.guard(), forge.ReviewApprove, "Reviewed.", nil))
}

func (j *commandJourney) reconcile(t *testing.T) ReconcileResult {
	t.Helper()
	result, err := reconcilePullRequest(t.Context(), j.cfg, j.repo, j.facts)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (j *commandJourney) acknowledged(id int64) bool {
	j.state.mu.Lock()
	defer j.state.mu.Unlock()
	return slices.ContainsFunc(j.state.commentReactions[id], func(reaction map[string]any) bool {
		return reaction["content"] == commandAcknowledgement && reaction["user"].(map[string]any)["login"] == githubFixtureBotLogin
	})
}

// launchedOutcomes is the push-inspection outcomes the last launched run
// was given.
func (j *commandJourney) launchedOutcomes(t *testing.T) string {
	t.Helper()
	if len(j.launches) == 0 {
		t.Fatal("no run was launched")
	}
	for _, arg := range j.launches[len(j.launches)-1] {
		if value, ok := strings.CutPrefix(arg, "MINOS_PUSH_INSPECTION_OUTCOMES="); ok {
			return value
		}
	}
	t.Fatal("the launched run carries no push-inspection outcomes")
	return ""
}

func TestReviewCommandSpendsTheCurrentHeadsCompletionMarker(t *testing.T) {
	journey := newCommandJourney(t)
	journey.finishClean(t)
	if result := journey.reconcile(t); result.DeferralReason != "completed-marker" {
		t.Fatalf("finished head reconciliation = %#v, want completed-marker", result)
	}
	id := journey.comment("maintainer", "@minos-review review")
	result := journey.reconcile(t)
	if result.Decision != SpawnStarted || !journey.acknowledged(id) {
		t.Fatalf("review command reconciliation = %#v, acknowledged %t; want a started run and the comment acknowledged", result, journey.acknowledged(id))
	}
	// The head already carries a review, so an increment over it is empty.
	if got := journey.launchedOutcomes(t); got != `["full"]` {
		t.Fatalf("re-review outcomes = %s, want full alone", got)
	}
	journey.finishClean(t)
	if result := journey.reconcile(t); result.DeferralReason != "completed-marker" || len(journey.launches) != 1 {
		t.Fatalf("answered command reconciliation = %#v after %d launches, want completed-marker and no second run", result, len(journey.launches))
	}
}

func TestReviewCommandIsNotAnsweredByARunAlreadyUnderWay(t *testing.T) {
	journey := newCommandJourney(t)
	journey.status(t, product.Working())
	journey.comment("maintainer", "@minos-review full review")
	journey.status(t, product.Clean())
	githubApplied(t, journey.adapter.PostReview(t.Context(), journey.state.guard(), forge.ReviewApprove, "Reviewed.", nil))
	if result := journey.reconcile(t); result.Decision != SpawnStarted {
		t.Fatalf("reconciliation after a run begun before the command = %#v, want a started run", result)
	}
	if got := journey.launchedOutcomes(t); got != `["full"]` {
		t.Fatalf("full review outcomes = %s", got)
	}
}

func TestReviewCommandOnANewHeadClosesNoReviewAlone(t *testing.T) {
	journey := newCommandJourney(t)
	journey.comment("maintainer", "@minos-review review")
	if result := journey.reconcile(t); result.Decision != SpawnStarted {
		t.Fatalf("reconciliation = %#v, want a started run", result)
	}
	if got := journey.launchedOutcomes(t); got != `["increment","full"]` {
		t.Fatalf("review outcomes = %s, want no-review closed and the rest as configured", got)
	}
}

func TestSkipCommandHoldsThePullRequestUntilAReviewCommand(t *testing.T) {
	journey := newCommandJourney(t)
	skip := journey.comment("maintainer", "@minos-review skip")
	result := journey.reconcile(t)
	if result.DeferralReason != skippedByCommandReason || len(journey.launches) != 0 || !journey.acknowledged(skip) {
		t.Fatalf("skip reconciliation = %#v after %d launches, acknowledged %t", result, len(journey.launches), journey.acknowledged(skip))
	}
	review := journey.comment("maintainer", "@minos-review review")
	if result := journey.reconcile(t); result.Decision != SpawnStarted || !journey.acknowledged(review) {
		t.Fatalf("review after skip = %#v, acknowledged %t", result, journey.acknowledged(review))
	}
}

func TestCommandFromAUserWithoutWriteAccessIsIgnored(t *testing.T) {
	journey := newCommandJourney(t)
	journey.finishClean(t)
	for _, user := range []string{"reader", "stranger"} {
		id := journey.comment(user, "@minos-review review")
		if result := journey.reconcile(t); result.DeferralReason != "completed-marker" || len(journey.launches) != 0 || journey.acknowledged(id) {
			t.Fatalf("%s's command reconciliation = %#v, acknowledged %t; want it ignored", user, result, journey.acknowledged(id))
		}
	}
}

func TestCommandOnAPullRequestOutsideTheRulesIsIgnored(t *testing.T) {
	journey := newCommandJourney(t)
	journey.state.pullRequest["draft"] = true
	id := journey.comment("maintainer", "@minos-review review")
	if result := journey.reconcile(t); result.DeferralReason != "draft" || len(journey.launches) != 0 || journey.acknowledged(id) {
		t.Fatalf("draft command reconciliation = %#v, acknowledged %t; want it ignored", result, journey.acknowledged(id))
	}
}

func TestForgejoCommenterMayCommandReadsWriteAccess(t *testing.T) {
	repository := forge.Repository{Owner: "owner", Name: "repo"}
	for _, tc := range []struct {
		name       string
		user       string
		permission int
		body       string
		want       bool
	}{
		{name: "writer", user: "writer", permission: 200, body: `{"permission":"write"}`, want: true},
		{name: "administrator", user: "admin", permission: 200, body: `{"permission":"admin"}`, want: true},
		{name: "reader", user: "reader", permission: 200, body: `{"permission":"read"}`, want: false},
		{name: "unknown user", user: "nobody", permission: 404, want: false},
		{name: "owner past a refused query", user: "Owner", permission: 403, want: true},
		// Refused the query, the script cannot tell a writer from a reader.
		{name: "collaborator past a refused query", user: "colleague", permission: 403, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := forgejoCommentsAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/repos/owner/repo/collaborators/" + tc.user + "/permission":
					w.WriteHeader(tc.permission)
					_, _ = w.Write([]byte(tc.body))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
					http.NotFound(w, r)
				}
			})
			var logged bytes.Buffer
			log.SetOutput(&logged)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })
			may, err := adapter.MayCommand(t.Context(), repository, tc.user)
			if err != nil || may != tc.want {
				t.Fatalf("MayCommand(%s) = %t, %v; want %t", tc.user, may, err, tc.want)
			}
			// The operator learns from the log why a collaborator's
			// command was ignored.
			if refusedCollaborator := tc.permission == 403 && !tc.want; refusedCollaborator != strings.Contains(logged.String(), "repository administrator") {
				t.Fatalf("logged %q", logged.String())
			}
		})
	}
	adapter := forgejoCommentsAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	if may, err := adapter.MayCommand(t.Context(), repository, "writer"); err == nil {
		t.Fatalf("an unavailable forge answered %t", may)
	}
}

func TestForgejoAcknowledgeCommentReactsOnceOnTheComment(t *testing.T) {
	var reactions []map[string]any
	posts := 0
	adapter := forgejoCommentsAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/user":
			_, _ = w.Write([]byte(`{"login":"Minos"}`))
		case r.URL.Path == "/api/v1/repos/owner/repo/pulls/17":
			_, _ = w.Write([]byte(`{"number":17,"state":"open","merged":false,"head":{"sha":"head"},"base":{"repo":{"full_name":"owner/repo"}}}`))
		case r.URL.Path == "/api/v1/repos/owner/repo/issues/comments/42/reactions" && r.Method == http.MethodGet:
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			encoded, _ := json.Marshal(reactions)
			_, _ = w.Write(encoded)
		case r.URL.Path == "/api/v1/repos/owner/repo/issues/comments/42/reactions" && r.Method == http.MethodPost:
			posts++
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			reactions = append(reactions, map[string]any{"content": payload["content"], "user": map[string]any{"login": "Minos"}})
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	})
	guard := forge.Guard{Repository: forge.Repository{Owner: "owner", Name: "repo"}, PullRequest: 17, HeadSHA: "head", TargetSHA: "target"}
	for range 2 {
		if result := adapter.AcknowledgeComment(t.Context(), guard, 42, commandAcknowledgement); result.Outcome != forge.WriteApplied {
			t.Fatalf("acknowledgement = %#v", result)
		}
	}
	if posts != 1 || len(reactions) != 1 || reactions[0]["content"] != commandAcknowledgement {
		t.Fatalf("acknowledgement wrote %d reactions: %v", posts, reactions)
	}
	guard.HeadSHA = "moved"
	if result := adapter.AcknowledgeComment(t.Context(), guard, 42, commandAcknowledgement); result.Outcome != forge.WriteRejected {
		t.Fatalf("acknowledgement on a moved head = %#v, want rejected", result)
	}
}

func TestForgejoCommentOnAPlainIssueIsNoOccasion(t *testing.T) {
	fixture := readFixture(t, findFixture(t, "issue_comment-created"))
	if facts := runNormaliseEvent(t, fixture); facts.Occasion != "comment-created" {
		t.Fatalf("pull-request comment occasion = %q", facts.Occasion)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(fixture.Body), &payload); err != nil {
		t.Fatal(err)
	}
	payload["is_pull"] = false
	payload["issue"].(map[string]any)["pull_request"] = nil
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "normalise-event"))
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = append(os.Environ(), "MINOS_HEADER_X_FORGEJO_EVENT=issue_comment")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "OCCASION=\n") {
		t.Fatalf("plain-issue comment normalised to %q, %v; want no occasion", out, err)
	}
}

// A clean run that changes nothing it had already approved leaves a status
// alone, which is read only on its own head. A command answered there stays
// answered after the author pushes, whether a pass saw the answer before
// the push or first sees it after, reading the statuses of the head the
// command's run was started on.
func TestReviewCommandAnsweredOnAnEarlierHeadStaysAnswered(t *testing.T) {
	for _, seen := range []bool{true, false} {
		t.Run(fmt.Sprintf("seen=%t", seen), func(t *testing.T) {
			journey := newCommandJourney(t)
			journey.comment("maintainer", "@minos-review review")
			if result := journey.reconcile(t); result.Decision != SpawnStarted {
				t.Fatalf("command reconciliation = %#v", result)
			}
			journey.status(t, product.Working())
			journey.status(t, product.Clean())
			if seen {
				if result := journey.reconcile(t); result.DeferralReason != "completed-marker" {
					t.Fatalf("answered command reconciliation = %#v", result)
				}
			}
			journey.moveHead("c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff")
			if result := journey.reconcile(t); result.Decision != SpawnStarted {
				t.Fatalf("new head reconciliation = %#v", result)
			}
			if got := journey.launchedOutcomes(t); got != `["no-review","increment","full"]` {
				t.Fatalf("new head outcomes = %s, want the configured outcomes", got)
			}
		})
	}
}

func TestCommandSupersededBeforeAPassIsStillAcknowledged(t *testing.T) {
	journey := newCommandJourney(t)
	review := journey.comment("maintainer", "@minos-review review")
	skip := journey.comment("maintainer", "@minos-review skip")
	if result := journey.reconcile(t); result.DeferralReason != skippedByCommandReason || !journey.acknowledged(review) || !journey.acknowledged(skip) {
		t.Fatalf("reconciliation = %#v, review acknowledged %t, skip acknowledged %t; want both acknowledged and the skip in effect", result, journey.acknowledged(review), journey.acknowledged(skip))
	}
	journey2 := newCommandJourney(t)
	skip = journey2.comment("maintainer", "@minos-review skip")
	review = journey2.comment("maintainer", "@minos-review full review")
	if result := journey2.reconcile(t); result.Decision != SpawnStarted || !journey2.acknowledged(review) || !journey2.acknowledged(skip) {
		t.Fatalf("reconciliation = %#v, skip acknowledged %t, review acknowledged %t; want both acknowledged and a run", result, journey2.acknowledged(skip), journey2.acknowledged(review))
	}
	if got := journey2.launchedOutcomes(t); got != `["full"]` {
		t.Fatalf("outcomes = %s", got)
	}
}

// A run fixes its review's scope when it is started, before it writes its
// working status, so a run started before the command never answers it,
// whether its working status lands after the command or is never written.
func TestReviewCommandIsNotAnsweredByARunStartedBeforeIt(t *testing.T) {
	for _, writesWorking := range []bool{true, false} {
		t.Run(fmt.Sprintf("working=%t", writesWorking), func(t *testing.T) {
			journey := newCommandJourney(t)
			if result := journey.reconcile(t); result.Decision != SpawnStarted || journey.launchedOutcomes(t) != `["no-review","increment","full"]` {
				t.Fatalf("first reconciliation = %#v", result)
			}
			id := journey.comment("maintainer", "@minos-review full review")
			if writesWorking {
				journey.status(t, product.Working())
			}
			journey.status(t, product.Clean())
			result := journey.reconcile(t)
			if result.Decision != SpawnStarted || len(journey.launches) != 2 || !journey.acknowledged(id) {
				t.Fatalf("reconciliation after the earlier run finished = %#v after %d launches, acknowledged %t; want a second run", result, len(journey.launches), journey.acknowledged(id))
			}
			if got := journey.launchedOutcomes(t); got != `["full"]` {
				t.Fatalf("second run outcomes = %s, want full alone", got)
			}
			journey.finishClean(t)
			if result := journey.reconcile(t); result.DeferralReason != "completed-marker" || len(journey.launches) != 2 {
				t.Fatalf("answered command reconciliation = %#v after %d launches", result, len(journey.launches))
			}
		})
	}
}

func TestCommandRecordGoesWithTheClosedPullRequest(t *testing.T) {
	journey := newCommandJourney(t)
	journey.comment("maintainer", "@minos-review review")
	if result := journey.reconcile(t); result.Decision != SpawnStarted {
		t.Fatalf("reconciliation = %#v", result)
	}
	path := commandRecordPath(journey.cfg, "github", journey.state.guard())
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no command record after a commanded run: %v", err)
	}
	journey.state.mu.Lock()
	journey.state.pullRequest["state"] = "closed"
	journey.state.mu.Unlock()
	journey.reconcile(t)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("command record outlived the closed pull request: %v", err)
	}
}

// The run started for a command can end incomplete after an earlier run,
// started before the command, finished clean; the earlier run's result
// answers nothing, so the retry is still the full review asked for.
func TestCommandOutlivesAnIncompleteRunStartedForIt(t *testing.T) {
	journey := newCommandJourney(t)
	if result := journey.reconcile(t); result.Decision != SpawnStarted {
		t.Fatalf("first reconciliation = %#v", result)
	}
	journey.comment("maintainer", "@minos-review full review")
	journey.status(t, product.Working())
	journey.status(t, product.Clean())
	if result := journey.reconcile(t); result.Decision != SpawnStarted || journey.launchedOutcomes(t) != `["full"]` {
		t.Fatalf("commanded reconciliation = %#v", result)
	}
	journey.status(t, product.Working())
	journey.status(t, product.Incomplete())
	if result := journey.reconcile(t); result.Decision != SpawnStarted || len(journey.launches) != 3 {
		t.Fatalf("retry reconciliation = %#v after %d launches", result, len(journey.launches))
	}
	if got := journey.launchedOutcomes(t); got != `["full"]` {
		t.Fatalf("retry outcomes = %s, want full alone", got)
	}
}

func TestCommandSupersededAfterAnUnrelatedRunIsAcknowledged(t *testing.T) {
	journey := newCommandJourney(t)
	if result := journey.reconcile(t); result.Decision != SpawnStarted {
		t.Fatalf("first reconciliation = %#v", result)
	}
	review := journey.comment("maintainer", "@minos-review review")
	journey.status(t, product.Working())
	journey.status(t, product.Clean())
	skip := journey.comment("maintainer", "@minos-review skip")
	if result := journey.reconcile(t); result.DeferralReason != skippedByCommandReason || !journey.acknowledged(review) || !journey.acknowledged(skip) {
		t.Fatalf("reconciliation = %#v, review acknowledged %t, skip acknowledged %t", result, journey.acknowledged(review), journey.acknowledged(skip))
	}
}

// The predecessor live when a command is written was started before it and
// never answers it: admission waits for that run to end, then starts the
// review the command asked for, once.
func TestReviewCommandWaitsOutALivePredecessor(t *testing.T) {
	journey := newCommandJourney(t)
	journey.live = true
	id := journey.comment("maintainer", "@minos-review review")
	if result := journey.reconcile(t); result.Decision != SpawnSuppressed || len(journey.launches) != 0 || !journey.acknowledged(id) {
		t.Fatalf("reconciliation under a live predecessor = %#v after %d launches, acknowledged %t", result, len(journey.launches), journey.acknowledged(id))
	}
	journey.finishClean(t)
	journey.live = false
	if result := journey.reconcile(t); result.Decision != SpawnStarted || len(journey.launches) != 1 {
		t.Fatalf("reconciliation once the predecessor ended = %#v after %d launches, want one run", result, len(journey.launches))
	}
	if got := journey.launchedOutcomes(t); got != `["full"]` {
		t.Fatalf("commanded run outcomes = %s, want full alone", got)
	}
	journey.live = true
	if result := journey.reconcile(t); result.Decision != SpawnSuppressed || len(journey.launches) != 1 {
		t.Fatalf("reconciliation under the commanded run = %#v after %d launches", result, len(journey.launches))
	}
	journey.finishClean(t)
	journey.live = false
	if result := journey.reconcile(t); result.DeferralReason != "completed-marker" || len(journey.launches) != 1 {
		t.Fatalf("answered command reconciliation = %#v after %d launches", result, len(journey.launches))
	}
}

// A predecessor that publishes after admission read the forge and before
// the commanded run starts published before that run, and answers nothing.
func TestReviewCommandIsNotAnsweredByAResultPublishedAsItsRunStarts(t *testing.T) {
	journey := newCommandJourney(t)
	journey.comment("maintainer", "@minos-review review")
	journey.onLaunch = func() {
		journey.onLaunch = nil
		journey.finishClean(t)
	}
	if result := journey.reconcile(t); result.Decision != SpawnStarted {
		t.Fatalf("commanded reconciliation = %#v", result)
	}
	if result := journey.reconcile(t); result.Decision != SpawnStarted || len(journey.launches) != 2 {
		t.Fatalf("reconciliation after the predecessor's result = %#v after %d launches, want the command still waiting", result, len(journey.launches))
	}
	journey.finishClean(t)
	if result := journey.reconcile(t); result.DeferralReason != "completed-marker" || len(journey.launches) != 2 {
		t.Fatalf("answered command reconciliation = %#v after %d launches", result, len(journey.launches))
	}
}

// A head the author resets the branch to carries the result Minos left on
// it before the command, which answers nothing: the reset head's marker is
// spent and a run started there for the command.
func TestReviewCommandIsNotAnsweredByAnOlderHeadsResult(t *testing.T) {
	journey := newCommandJourney(t)
	older := journey.state.guard().HeadSHA
	journey.finishClean(t)
	journey.moveHead("c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff")
	journey.comment("maintainer", "@minos-review review")
	if result := journey.reconcile(t); result.Decision != SpawnStarted {
		t.Fatalf("commanded reconciliation = %#v", result)
	}
	journey.moveHead(older)
	if result := journey.reconcile(t); result.Decision != SpawnStarted || len(journey.launches) != 2 {
		t.Fatalf("reconciliation on the reset head = %#v after %d launches, want a run for the command", result, len(journey.launches))
	}
	if got := journey.launchedOutcomes(t); got != `["full"]` {
		t.Fatalf("reset head outcomes = %s, want full alone", got)
	}
	journey.finishClean(t)
	if result := journey.reconcile(t); result.DeferralReason != "completed-marker" || len(journey.launches) != 2 {
		t.Fatalf("answered command reconciliation = %#v after %d launches", result, len(journey.launches))
	}
}

// A run that writes a review and no status answers a command by the review
// alone; the older review the record knew from the start does not.
func TestReviewAloneAnswersACommand(t *testing.T) {
	for _, verdict := range []forge.ReviewVerdict{forge.ReviewApprove, forge.ReviewRequestChanges} {
		t.Run(string(verdict), func(t *testing.T) {
			journey := newCommandJourney(t)
			journey.finishClean(t)
			journey.comment("maintainer", "@minos-review review")
			if result := journey.reconcile(t); result.Decision != SpawnStarted {
				t.Fatalf("commanded reconciliation = %#v", result)
			}
			githubApplied(t, journey.adapter.PostReview(t.Context(), journey.state.guard(), verdict, "Reviewed again.", nil))
			if result := journey.reconcile(t); result.DeferralReason != "completed-marker" || len(journey.launches) != 1 {
				t.Fatalf("reconciliation after the review = %#v after %d launches, want the command answered", result, len(journey.launches))
			}
		})
	}
}

// The summary comment is one comment edited in place: a later run's write of
// the same head and state as the one the record knew still answers.
func TestSummaryCommentAnswersACommand(t *testing.T) {
	journey := newCommandJourney(t)
	summary := func(state product.SummaryState) {
		t.Helper()
		result, err := writeSummary(t.Context(), journey.adapter, journey.state.guard(), state, "Summary.")
		if err != nil {
			t.Fatal(err)
		}
		githubApplied(t, result)
	}
	summary(product.SummaryClean)
	if result := journey.reconcile(t); result.DeferralReason != "completed-marker" || len(journey.launches) != 0 {
		t.Fatalf("summary-finished head reconciliation = %#v", result)
	}
	journey.comment("maintainer", "@minos-review review")
	if result := journey.reconcile(t); result.Decision != SpawnStarted || journey.launchedOutcomes(t) != `["full"]` {
		t.Fatalf("commanded reconciliation = %#v", result)
	}
	summary(product.SummaryInspecting)
	summary(product.SummaryClean)
	if result := journey.reconcile(t); result.DeferralReason != "completed-marker" || len(journey.launches) != 1 {
		t.Fatalf("reconciliation after the rewritten summary = %#v after %d launches", result, len(journey.launches))
	}
	// The answer stands once the summary moves on to a later head.
	journey.moveHead("c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff")
	summary(product.SummaryInspecting)
	if result := journey.reconcile(t); result.Decision != SpawnStarted || journey.launchedOutcomes(t) != `["no-review","increment","full"]` {
		t.Fatalf("later head reconciliation = %#v", result)
	}
}

func TestUnreadableCommandsDeferThePullRequest(t *testing.T) {
	for _, tc := range []struct {
		name, path, reason string
	}{
		{"comments", "/repos/acme/widgets/issues/7/comments", "pull request snapshot unavailable"},
		{"permission", "/repos/acme/widgets/collaborators/maintainer/permission", "comment commands unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journey := newCommandJourney(t)
			journey.comment("maintainer", "@minos-review review")
			journey.state.mu.Lock()
			journey.state.unreadablePath = tc.path
			journey.state.mu.Unlock()
			result, err := reconcilePullRequest(t.Context(), journey.cfg, journey.repo, journey.facts)
			if err == nil || result.DeferralReason != tc.reason || len(journey.launches) != 0 {
				t.Fatalf("reconciliation = %#v, %v after %d launches; want %q and no run", result, err, len(journey.launches), tc.reason)
			}
		})
	}
}

func TestSkipCommandHoldsAcrossAHeadMove(t *testing.T) {
	journey := newCommandJourney(t)
	journey.comment("maintainer", "@minos-review skip")
	if result := journey.reconcile(t); result.DeferralReason != skippedByCommandReason {
		t.Fatalf("skip reconciliation = %#v", result)
	}
	journey.moveHead("c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff")
	if result := journey.reconcile(t); result.DeferralReason != skippedByCommandReason || len(journey.launches) != 0 {
		t.Fatalf("reconciliation after the head moved = %#v after %d launches, want the skip still in effect", result, len(journey.launches))
	}
}

// A reaction the forge refused is written on the next pass, also on a
// command a newer one has by then superseded.
func TestRefusedAcknowledgementIsTriedAgain(t *testing.T) {
	for _, superseded := range []bool{false, true} {
		t.Run(fmt.Sprintf("superseded=%t", superseded), func(t *testing.T) {
			journey := newCommandJourney(t)
			review := journey.comment("maintainer", "@minos-review review")
			journey.state.mu.Lock()
			journey.state.refuseMutation = true
			journey.state.mu.Unlock()
			journey.reconcile(t)
			if journey.acknowledged(review) {
				t.Fatal("a refused reaction was recorded")
			}
			journey.state.mu.Lock()
			journey.state.refuseMutation = false
			journey.state.mu.Unlock()
			skip := int64(0)
			if superseded {
				skip = journey.comment("maintainer", "@minos-review skip")
			}
			result := journey.reconcile(t)
			if !journey.acknowledged(review) || (superseded && (!journey.acknowledged(skip) || result.DeferralReason != skippedByCommandReason)) {
				t.Fatalf("next pass = %#v, review acknowledged %t", result, journey.acknowledged(review))
			}
		})
	}
}

// Two passes over one pull request that each change the command record
// keep both changes.
func TestCommandRecordUpdatesDoNotLoseEachOther(t *testing.T) {
	cfg := ServiceConfig{}
	cfg.Runs.Dir = t.TempDir()
	guard := forge.Guard{Repository: forge.Repository{Owner: "acme", Name: "widgets"}, PullRequest: 7}
	var wg sync.WaitGroup
	for id := range int64(16) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			updateCommandRecord(cfg, "github", guard, func(record *commandRecord) {
				record.Acknowledged = append(record.Acknowledged, id)
			})
		}()
	}
	wg.Wait()
	if got := readCommandRecord(cfg, "github", guard).Acknowledged; len(got) != 16 {
		t.Fatalf("acknowledged after concurrent updates = %v, want all 16", got)
	}
}

// A run started for a command that ends having written nothing answers it
// with nothing: the summary its predecessor finished still stands, and the
// record knew that summary when the run started.
func TestCommandOutlivesARunThatWroteNothingUnderAFinishedSummary(t *testing.T) {
	journey := newCommandJourney(t)
	result, err := writeSummary(t.Context(), journey.adapter, journey.state.guard(), product.SummaryClean, "Summary.")
	if err != nil {
		t.Fatal(err)
	}
	githubApplied(t, result)
	journey.comment("maintainer", "@minos-review review")
	if result := journey.reconcile(t); result.Decision != SpawnStarted || len(journey.launches) != 1 {
		t.Fatalf("commanded reconciliation = %#v after %d launches", result, len(journey.launches))
	}
	if result := journey.reconcile(t); result.Decision != SpawnStarted || len(journey.launches) != 2 || journey.launchedOutcomes(t) != `["full"]` {
		t.Fatalf("reconciliation after a run that wrote nothing = %#v after %d launches, want the command still waiting", result, len(journey.launches))
	}
}

// Two pull requests can share a head commit. A status Minos wrote for the
// other one is on the commit but answers no command given on this one.
func TestReviewCommandIsNotAnsweredByAnotherPullRequestsStatus(t *testing.T) {
	journey := newCommandJourney(t)
	journey.comment("maintainer", "@minos-review review")
	if result := journey.reconcile(t); result.Decision != SpawnStarted {
		t.Fatalf("commanded reconciliation = %#v", result)
	}
	commanded := journey.launchedOutcomes(t)
	journey.status(t, product.Clean())
	journey.state.mu.Lock()
	moved := false
	for _, status := range journey.state.statuses {
		if status["context"] == "Minos" && strings.HasSuffix(status["target_url"].(string), "/pull/7") {
			status["target_url"] = strings.TrimSuffix(status["target_url"].(string), "/pull/7") + "/pull/8"
			moved = true
		}
	}
	if !moved {
		t.Fatalf("no clean status of this pull request to move: %#v", journey.state.statuses)
	}
	journey.state.mu.Unlock()
	if result := journey.reconcile(t); result.Decision != SpawnStarted || len(journey.launches) != 2 || journey.launchedOutcomes(t) != commanded {
		t.Fatalf("reconciliation after the other pull request's status = %#v after %d launches, want the command still waiting", result, len(journey.launches))
	}
	journey.status(t, product.Clean())
	if result := journey.reconcile(t); result.DeferralReason != "completed-marker" || len(journey.launches) != 2 {
		t.Fatalf("reconciliation after this pull request's status = %#v after %d launches", result, len(journey.launches))
	}
}

// A head whose run left an unfinished summary is finished by a terminal
// status written after it, as admission writes when it stops a stalled
// continuation and a resident lead writes in place of a summary, and not
// by one written before it.
func TestTerminalStatusAfterAnUnfinishedSummaryFinishesTheHead(t *testing.T) {
	journey := newCommandJourney(t)
	journey.status(t, product.Clean())
	result, err := writeSummary(t.Context(), journey.adapter, journey.state.guard(), product.SummaryReviewing, "Summary.")
	if err != nil {
		t.Fatal(err)
	}
	githubApplied(t, result)
	if result := journey.reconcile(t); result.Decision != SpawnStarted || len(journey.launches) != 1 {
		t.Fatalf("reconciliation under a status older than the summary = %#v after %d launches, want a run", result, len(journey.launches))
	}
	journey.status(t, product.Attention())
	if result := journey.reconcile(t); result.DeferralReason != "completed-marker" || result.TerminalOutcome != product.Attention().Name() || len(journey.launches) != 1 {
		t.Fatalf("reconciliation after the later attention status = %#v after %d launches", result, len(journey.launches))
	}
}
