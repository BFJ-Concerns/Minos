package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestForgejoAdmissionUsesFreshPullRequestSnapshot(t *testing.T) {
	t.Run("draft pull request is not started", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.changePullRequest(func(pullRequest map[string]any) { pullRequest["draft"] = true })
		cfg, repo, facts := state.service(t)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Fatalf("draft pull request reached %s", name)
			return nil, nil
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result != "nothing" {
			t.Fatalf("result = %q, want nothing", result)
		}
	})

	t.Run("Minos review with a missing terminal status is repaired without a new run", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.setReviews([]map[string]any{{
			"id": 41, "state": "APPROVED", "commit_id": state.headSHA(),
			"body": "ordinary review", "user": map[string]any{"login": "Minos"},
		}})
		cfg, repo, facts := state.service(t)
		state.setStatuses([]map[string]any{{
			"id": 7, "context": "Minos", "status": "success", "description": "Changes approved",
			"target_url": state.server.URL + "/minos-e2e-owner/subject/pulls/2#minos-target-elsewhere",
			"creator":    map[string]any{"login": "Minos"},
		}})

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Fatalf("reviewed pull request reached %s", name)
			return nil, nil
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result != "recovered" {
			t.Fatalf("result = %q, want recovered", result)
		}
		result, err = reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result != "nothing" {
			t.Fatalf("result after repair = %q, want nothing", result)
		}
		statusWrites, repairedTarget := state.statusWriteFacts()
		if statusWrites != 1 {
			t.Fatalf("status writes = %d, want one", statusWrites)
		}
		facts.BaseSHA = state.targetSHA()
		if repairedTarget != statusTargetURL(cfg.Forges["forgejo"].APIBase, facts) {
			t.Fatalf("repaired status target = %q, want this pull request and target", repairedTarget)
		}
	})

	t.Run("another pull request status on the same head does not suppress this pull request", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, facts := state.service(t)
		state.setStatuses([]map[string]any{{
			"id": 7, "context": "Minos", "status": "success", "description": "Changes approved",
			"target_url": state.server.URL + "/minos-e2e-owner/subject/pulls/2#minos-target-elsewhere",
			"creator":    map[string]any{"login": "Minos"},
		}})

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		var started bool
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			switch name {
			case "systemctl":
				return nil, nil
			case "systemd-run":
				started = true
				return nil, nil
			default:
				t.Fatalf("unexpected command %q", name)
				return nil, nil
			}
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result != "started" || !started {
			t.Fatalf("result = %q, started = %t; want an actual start", result, started)
		}
	})

	t.Run("virtual pull ref starts branchless", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		state.changePullRequest(func(pullRequest map[string]any) {
			pullRequest["head"].(map[string]any)["ref"] = "refs/pull/1/head"
		})
		cfg, repo, facts := state.service(t)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		var systemdArgs []string
		commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "systemctl" {
				return nil, nil
			}
			if name == "systemd-run" {
				systemdArgs = append([]string(nil), args...)
				return nil, nil
			}
			t.Fatalf("unexpected command %q", name)
			return nil, nil
		}

		snapshot, err := currentSnapshot(t.Context(), cfg, facts)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.HeadBranch != "" {
			t.Fatalf("head branch = %q, want virtual ref to be branchless", snapshot.HeadBranch)
		}
		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result != "started" {
			t.Fatalf("result = %q, want started", result)
		}
		assertArgument(t, systemdArgs, "MINOS_HEAD_BRANCH=")
		if state.virtualBranchReads() != 0 {
			t.Fatalf("virtual pull ref caused %d branch reads", state.virtualBranchReads())
		}
	})

	t.Run("active pull request unit is suppressed", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, facts := state.service(t)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		var commands []string
		commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
			commands = append(commands, name)
			return []byte("minos-other-repo-pr9.service loaded active running Minos lead\n"), nil
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
		if err != nil {
			t.Fatal(err)
		}
		if result != "suppressed" {
			t.Fatalf("result = %q, want suppressed", result)
		}
		if !slices.Equal(commands, []string{"systemctl"}) {
			t.Fatalf("commands = %v, want only active-unit check", commands)
		}
	})

	t.Run("a second pull request waits for the active lead to exit", func(t *testing.T) {
		state := newForgejoFixtureState(t)
		cfg, repo, firstFacts := state.service(t)

		original := commandCombinedOutput
		t.Cleanup(func() { commandCombinedOutput = original })
		var activeUnit string
		var startedUnits []string
		commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "systemctl":
				if activeUnit == "" {
					return nil, nil
				}
				return []byte(activeUnit + " loaded active running Minos lead\n"), nil
			case "systemd-run":
				unitFlag := slices.Index(args, "--unit")
				if unitFlag < 0 || unitFlag+1 >= len(args) {
					t.Fatalf("systemd-run arguments omit unit: %v", args)
				}
				activeUnit = args[unitFlag+1]
				startedUnits = append(startedUnits, activeUnit)
				return nil, nil
			default:
				t.Fatalf("unexpected command %q", name)
				return nil, nil
			}
		}

		result, err := reconcilePullRequest(t.Context(), cfg, repo, firstFacts)
		if err != nil {
			t.Fatal(err)
		}
		if result != "started" {
			t.Fatalf("first result = %q, want started", result)
		}

		state.changePullRequest(func(pullRequest map[string]any) { pullRequest["number"] = float64(2) })
		_, _, secondFacts := state.service(t)
		secondFacts.HeadSHA = ""
		result, err = reconcilePullRequest(t.Context(), cfg, repo, secondFacts)
		if err != nil {
			t.Fatal(err)
		}
		if result != "suppressed" {
			t.Fatalf("second result while first active = %q, want suppressed", result)
		}
		if len(startedUnits) != 1 {
			t.Fatalf("started units while first active = %v, want one", startedUnits)
		}

		activeUnit = ""
		result, err = reconcilePullRequest(t.Context(), cfg, repo, secondFacts)
		if err != nil {
			t.Fatal(err)
		}
		if result != "started" {
			t.Fatalf("second result after first exit = %q, want started", result)
		}
		if len(startedUnits) != 2 || !strings.Contains(startedUnits[1], "pr2") {
			t.Fatalf("started units = %v, want second pull request unit", startedUnits)
		}
	})
}

func TestForgeClaimAssignsAndReactsIdempotently(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg, _, _ := state.service(t)
	writeServiceConfig(t, cfg)
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", "forgejo")
	t.Setenv("MINOS_OWNER", "minos-e2e-owner")
	t.Setenv("MINOS_REPO_NAME", "subject")
	t.Setenv("MINOS_PR", "1")

	for attempt := 1; attempt <= 2; attempt++ {
		var stdout bytes.Buffer
		if err := ForgeCommand(t.Context(), []string{"claim"}, &stdout); err != nil {
			t.Fatalf("claim attempt %d: %v", attempt, err)
		}
		if !strings.Contains(stdout.String(), `"outcome":"applied"`) {
			t.Fatalf("claim attempt %d output = %q", attempt, stdout.String())
		}
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if !slices.Contains(state.assignees, "Minos") {
		t.Fatalf("assignees = %v, want Minos", state.assignees)
	}
	if !slices.Contains(state.reactions, "eyes") {
		t.Fatalf("reactions = %v, want eyes", state.reactions)
	}
	if state.assignmentWrites != 1 || state.reactionWrites != 1 {
		t.Fatalf("writes = assignment:%d reaction:%d, want one each", state.assignmentWrites, state.reactionWrites)
	}
	if state.obsoleteAssignmentWrites != 0 {
		t.Fatalf("obsolete assignment route received %d writes, want none", state.obsoleteAssignmentWrites)
	}
}

type forgejoFixtureState struct {
	t              *testing.T
	pullRequest    map[string]any
	repository     map[string]any
	reviews        []map[string]any
	statuses       []map[string]any
	server         *httptest.Server
	tokenPath      string
	adaptationPath string

	mu                       sync.Mutex
	assignees                []string
	reactions                []string
	assignmentWrites         int
	obsoleteAssignmentWrites int
	reactionWrites           int
	statusWrites             int
	statusReadCommits        []string
	virtualRefLookups        int
	annexeCloneURL           string
}

func newForgejoFixtureState(t *testing.T) *forgejoFixtureState {
	t.Helper()
	fixture := readFixture(t, "001-pull_request-opened.json")
	var event struct {
		PullRequest map[string]any `json:"pull_request"`
		Repository  map[string]any `json:"repository"`
	}
	if err := json.Unmarshal([]byte(fixture.Body), &event); err != nil {
		t.Fatal(err)
	}
	adaptationPath, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	state := &forgejoFixtureState{
		t: t, pullRequest: event.PullRequest, repository: event.Repository,
		adaptationPath: adaptationPath,
	}
	state.server = httptest.NewServer(http.HandlerFunc(state.handle))
	t.Cleanup(state.server.Close)
	state.tokenPath = filepath.Join(t.TempDir(), "forge.token")
	if err := os.WriteFile(state.tokenPath, []byte("fixture-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return state
}

func (s *forgejoFixtureState) service(t *testing.T) (ServiceConfig, RepoConfig, Facts) {
	t.Helper()
	s.mu.Lock()
	pullRequestNumber := fmt.Sprint(s.pullRequest["number"])
	s.mu.Unlock()
	cfg := ServiceConfig{Root: t.TempDir()}
	cfg.Service.BotLogin = "Minos"
	cfg.Listener.Bind = ":0"
	cfg.Runs.Dir = t.TempDir()
	cfg.Forges = map[string]ForgeConfig{
		"forgejo": {
			Adaptation: s.adaptationPath, APIBase: s.server.URL,
			WebhookSecretFile: s.tokenPath, CredentialFile: s.tokenPath,
		},
	}
	repo := RepoConfig{Forge: "forgejo", Owner: "minos-e2e-owner", Repo: "subject"}
	repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"
	facts := Facts{
		Forge: "forgejo", Owner: repo.Owner, Repo: repo.Repo,
		PR: pullRequestNumber, Occasion: "pr-opened",
	}
	return cfg, repo, facts
}

func (s *forgejoFixtureState) headSHA() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pullRequest["head"].(map[string]any)["sha"].(string)
}

func (s *forgejoFixtureState) targetSHA() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pullRequest["base"].(map[string]any)["sha"].(string)
}

func (s *forgejoFixtureState) changePullRequest(change func(map[string]any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s.pullRequest)
}

func (s *forgejoFixtureState) setReviews(reviews []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reviews = reviews
}

func (s *forgejoFixtureState) setStatuses(statuses []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = statuses
}

func (s *forgejoFixtureState) setAnnexeCloneURL(cloneURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.annexeCloneURL = cloneURL
}

func (s *forgejoFixtureState) statusReadFacts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.statusReadCommits...)
}

func (s *forgejoFixtureState) statusWriteFacts() (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var target any
	if len(s.statuses) > 0 {
		target = s.statuses[0]["target_url"]
	}
	return s.statusWrites, target
}

func (s *forgejoFixtureState) virtualBranchReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.virtualRefLookups
}

func (s *forgejoFixtureState) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	pullPath := fmt.Sprintf("/api/v1/repos/minos-e2e-owner/subject/pulls/%v", s.pullRequest["number"])
	issuePath := fmt.Sprintf("/api/v1/repos/minos-e2e-owner/subject/issues/%v", s.pullRequest["number"])
	switch {
	case r.Method == http.MethodGet && path == "/api/v1/user":
		writeFixtureJSON(s.t, w, map[string]any{"login": "Minos"})
	case r.Method == http.MethodGet && path == "/api/v1/repos/minos-e2e-owner/subject":
		writeFixtureJSON(s.t, w, s.repository)
	case r.Method == http.MethodGet && path == "/api/v1/repos/minos-e2e-owner/subject-Annexe":
		if s.annexeCloneURL == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeFixtureJSON(s.t, w, map[string]any{"clone_url": s.annexeCloneURL})
	case r.Method == http.MethodGet && path == pullPath:
		writeFixtureJSON(s.t, w, s.pullRequest)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/branches/main"):
		base := s.pullRequest["base"].(map[string]any)
		writeFixtureJSON(s.t, w, map[string]any{
			"commit": map[string]any{"id": base["sha"]}, "protected": false,
			"user_can_merge": true, "status_check_contexts": []string{},
		})
	case r.Method == http.MethodGet && strings.Contains(path, "/branches/"):
		if strings.Contains(path, "refs/pull/") {
			s.virtualRefLookups++
		}
		writeFixtureJSON(s.t, w, map[string]any{"protected": false})
	case r.Method == http.MethodGet && strings.Contains(path, "/commits/") && strings.HasSuffix(path, "/statuses"):
		commit := strings.TrimSuffix(strings.SplitN(path, "/commits/", 2)[1], "/statuses")
		s.statusReadCommits = append(s.statusReadCommits, commit)
		writeFixtureJSON(s.t, w, s.statuses)
	case r.Method == http.MethodPost && strings.Contains(path, "/statuses/"):
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
		}
		nextID := float64(1)
		for _, status := range s.statuses {
			var id float64
			switch value := status["id"].(type) {
			case int:
				id = float64(value)
			case float64:
				id = value
			}
			if id >= nextID {
				nextID = id + 1
			}
		}
		payload["id"] = nextID
		payload["creator"] = map[string]any{"login": "Minos"}
		s.statuses = append([]map[string]any{payload}, s.statuses...)
		s.statusWrites++
		writeFixtureJSON(s.t, w, payload)
	case r.Method == http.MethodGet && path == pullPath+"/reviews":
		writeFixtureJSON(s.t, w, s.reviews)
	case r.Method == http.MethodGet && path == issuePath:
		assignees := make([]map[string]any, 0, len(s.assignees))
		for _, login := range s.assignees {
			assignees = append(assignees, map[string]any{"login": login})
		}
		writeFixtureJSON(s.t, w, map[string]any{"assignees": assignees})
	case r.Method == http.MethodPost && path == issuePath+"/assignees":
		s.obsoleteAssignmentWrites++
		http.Error(w, "route not found", http.StatusNotFound)
	case r.Method == http.MethodPatch && path == issuePath:
		var payload struct {
			Assignees []string `json:"assignees"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
		}
		for _, login := range payload.Assignees {
			if !slices.Contains(s.assignees, login) {
				s.assignees = append(s.assignees, login)
			}
		}
		s.assignmentWrites++
		writeFixtureJSON(s.t, w, map[string]any{})
	case r.Method == http.MethodGet && path == issuePath+"/reactions":
		reactions := make([]map[string]any, 0, len(s.reactions))
		for _, content := range s.reactions {
			reactions = append(reactions, map[string]any{"content": content, "user": map[string]any{"login": "Minos"}})
		}
		writeFixtureJSON(s.t, w, reactions)
	case r.Method == http.MethodPost && path == issuePath+"/reactions":
		var payload struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
		}
		if !slices.Contains(s.reactions, payload.Content) {
			s.reactions = append(s.reactions, payload.Content)
		}
		s.reactionWrites++
		writeFixtureJSON(s.t, w, map[string]any{})
	default:
		http.Error(w, fmt.Sprintf("unexpected fixture request %s %s", r.Method, path), http.StatusNotFound)
	}
}

func writeFixtureJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func writeServiceConfig(t *testing.T, cfg ServiceConfig) {
	t.Helper()
	body := fmt.Sprintf(`[service]
bot-login = "Minos"
[listener]
bind = ":0"
[forges.forgejo]
adaptation = %q
api-base = %q
webhook-secret-file = %q
credential-file = %q
[runs]
dir = %q
`, cfg.Forges["forgejo"].Adaptation, cfg.Forges["forgejo"].APIBase,
		cfg.Forges["forgejo"].WebhookSecretFile, cfg.Forges["forgejo"].CredentialFile, cfg.Runs.Dir)
	if err := os.WriteFile(filepath.Join(cfg.Root, "service.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
