package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
		if name == "systemd-run" {
			journey.launches = append(journey.launches, args)
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
		// collaborator is the answer to the collaborator check Forgejo
		// leaves a non-administrator; zero where it must not be asked.
		collaborator int
		want         bool
	}{
		{name: "writer", user: "writer", permission: 200, body: `{"permission":"write"}`, want: true},
		{name: "administrator", user: "admin", permission: 200, body: `{"permission":"admin"}`, want: true},
		{name: "reader", user: "reader", permission: 200, body: `{"permission":"read"}`, want: false},
		{name: "unknown user", user: "nobody", permission: 404, want: false},
		{name: "owner past a refused query", user: "Owner", permission: 403, want: true},
		{name: "collaborator past a refused query", user: "colleague", permission: 403, collaborator: 204, want: true},
		{name: "stranger past a refused query", user: "stranger", permission: 403, collaborator: 404, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := forgejoCommentsAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/repos/owner/repo/collaborators/" + tc.user + "/permission":
					w.WriteHeader(tc.permission)
					_, _ = w.Write([]byte(tc.body))
				case "/api/v1/repos/owner/repo/collaborators/" + tc.user:
					if tc.collaborator == 0 {
						t.Errorf("collaborator check asked for %s", tc.user)
					}
					w.WriteHeader(tc.collaborator)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
					http.NotFound(w, r)
				}
			})
			may, err := adapter.MayCommand(t.Context(), repository, tc.user)
			if err != nil || may != tc.want {
				t.Fatalf("MayCommand(%s) = %t, %v; want %t", tc.user, may, err, tc.want)
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
// answered after the author pushes, through the record of the head a run
// last finished on.
func TestReviewCommandAnsweredOnAnEarlierHeadStaysAnswered(t *testing.T) {
	for _, recorded := range []bool{true, false} {
		t.Run(fmt.Sprintf("recorded=%t", recorded), func(t *testing.T) {
			journey := newCommandJourney(t)
			journey.comment("maintainer", "@minos-review review")
			journey.status(t, product.Working())
			journey.status(t, product.Clean())
			if result := journey.reconcile(t); result.DeferralReason != "completed-marker" {
				t.Fatalf("answered command reconciliation = %#v", result)
			}
			earlier := journey.state.guard()
			if recorded {
				path := reviewedChangePath(journey.cfg, "github", earlier)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				record, _ := json.Marshal(reviewedChange{Head: earlier.HeadSHA, Target: earlier.TargetSHA, Outcome: "clean", Files: map[string]string{}})
				if err := os.WriteFile(path, record, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			journey.state.mu.Lock()
			journey.state.statusesAt = map[string][]map[string]any{earlier.HeadSHA: journey.state.statuses}
			journey.state.statuses = nil
			journey.state.pullRequest["head"].(map[string]any)["sha"] = "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff"
			journey.state.mu.Unlock()
			if result := journey.reconcile(t); result.Decision != SpawnStarted {
				t.Fatalf("new head reconciliation = %#v", result)
			}
			want := `["no-review","increment","full"]`
			if !recorded {
				// Without the record the command is taken as waiting, and
				// the new head's run is kept from no-review once.
				want = `["increment","full"]`
			}
			if got := journey.launchedOutcomes(t); got != want {
				t.Fatalf("new head outcomes = %s, want %s", got, want)
			}
		})
	}
}
