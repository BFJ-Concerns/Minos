package shell

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/BFJ-Concerns/Minos/internal/product"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

// The GitHub adaptation is proved against a fixture GitHub the way the
// Forgejo one is proved against its fixture: the real scripts, a local HTTP
// server speaking the forge's API, and assertions on the recorded writes.
// What is GitHub's own — the App JWT, installation-token minting, the
// sha256= signature prefix, line-and-side anchoring, the review-state
// vocabulary — is what these cases pin.

const (
	githubFixtureAppID          = "4242"
	githubFixtureInstallationID = "9001"
	githubFixtureAppSlug        = "minos-review"
	githubFixtureBotLogin       = githubFixtureAppSlug + "[bot]"
)

type githubFixtureWrite struct {
	method, path string
	payload      map[string]any
}

type githubFixtureState struct {
	t      *testing.T
	mu     sync.Mutex
	server *httptest.Server
	key    *rsa.PrivateKey

	adaptationPath string
	credentialPath string
	credential     string

	mintedTokens   []string
	pullRequest    map[string]any
	baseTip        string
	statuses       []map[string]any
	reviews        []map[string]any
	reviewComments map[int64][]map[string]any
	// cloneURL is acme/widgets' clone URL as the repository route reports it.
	cloneURL string
	// diffLines is the set of lines GitHub would accept an anchor on, per
	// path; a comment outside it is refused with 422 as GitHub refuses it.
	diffLines    map[string][]int64
	reviewWrites []map[string]any
	// refuseReviewPosts, when non-zero, is the HTTP status the fixture
	// answers every review submission with, recording nothing.
	refuseReviewPosts int
	// refuseReviewReads, when non-zero, is the HTTP status the fixture
	// answers every review listing with, so a script's read-back sees an
	// unreadable forge rather than an empty one.
	refuseReviewReads int
	reactions         []map[string]any
	definedLabels     []map[string]any
	issues            []map[string]any
	pullRequests      []map[string]any
	writes            []githubFixtureWrite
	unreadablePath    string
	refuseMutation    bool
}

func newGitHubFixtureState(t *testing.T) *githubFixtureState {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	adaptationPath, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "github"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	keyPath := filepath.Join(root, "github-app.pem")
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	credential := fmt.Sprintf(`{"app-id": %q, "installation-id": %q, "private-key-file": %q}`, githubFixtureAppID, githubFixtureInstallationID, keyPath)
	credentialPath := filepath.Join(root, "github-app.json")
	if err := os.WriteFile(credentialPath, []byte(credential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Each test's tokens live in its own cache so one case's minted token
	// never answers for another.
	t.Setenv("MINOS_GITHUB_TOKEN_CACHE", filepath.Join(root, "token-cache"))

	state := &githubFixtureState{
		t: t, key: key, adaptationPath: adaptationPath,
		credentialPath: credentialPath, credential: credential,
		baseTip: "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0",
		pullRequest: map[string]any{
			"number": 7, "state": "open", "merged": false, "draft": true,
			"title": "Widget change", "body": "Out of scope: the gears.",
			"user":   map[string]any{"login": "author"},
			"head":   map[string]any{"sha": "feedfacefeedfacefeedfacefeedfacefeedface", "ref": "topic", "repo": map[string]any{"full_name": "acme/widgets"}},
			"base":   map[string]any{"ref": "main", "sha": "0ld0ld0ld0ld0ld0ld0ld0ld0ld0ld0ld0ld0ld0", "repo": map[string]any{"full_name": "acme/widgets"}},
			"labels": []map[string]any{{"name": "needs-review"}},
		},
		statuses: manyStatuses(101),
		reviews: []map[string]any{
			{"id": 501, "state": "CHANGES_REQUESTED", "commit_id": "feedfacefeedfacefeedfacefeedfacefeedface", "body": "Earlier review", "user": map[string]any{"login": "colleague"}},
		},
		reviewComments: map[int64][]map[string]any{},
		cloneURL:       "https://github.example/acme/widgets.git",
		diffLines:      map[string][]int64{"src/code.txt": {3, 4, 5, 6, 7, 8}},
	}
	state.server = httptest.NewServer(http.HandlerFunc(state.handle))
	t.Cleanup(state.server.Close)
	return state
}

// manyStatuses is a commit's status history of the given length, newest
// first as GitHub lists them, ids 31 upwards, the oldest written by ci-bot.
func manyStatuses(count int) []map[string]any {
	statuses := make([]map[string]any, 0, count)
	for index := count - 1; index >= 0; index-- {
		id := 31 + index
		creator := "status-writer"
		if index == 0 {
			creator = "ci-bot"
		}
		statuses = append(statuses, map[string]any{
			"id": id, "state": "success", "context": fmt.Sprintf("ci/check-%d", index), "description": "built",
			"target_url": fmt.Sprintf("https://ci.example/%d", id), "creator": map[string]any{"login": creator},
		})
	}
	return statuses
}

func (s *githubFixtureState) runner() forge.ScriptRunner {
	return forge.ScriptRunner{Directory: s.adaptationPath, APIBase: s.server.URL, Credential: s.credential}
}

func (s *githubFixtureState) adapter(t *testing.T) *forge.Adapter {
	t.Helper()
	adapter, err := forge.NewAdapter(s.runner(), githubFixtureBotLogin, "Minos")
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func (s *githubFixtureState) guard() forge.Guard {
	s.mu.Lock()
	defer s.mu.Unlock()
	return forge.Guard{
		Repository:  forge.Repository{Owner: "acme", Name: "widgets"},
		PullRequest: 7,
		HeadSHA:     s.pullRequest["head"].(map[string]any)["sha"].(string),
		TargetSHA:   s.baseTip,
	}
}

func (s *githubFixtureState) moveHead(sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pullRequest["head"].(map[string]any)["sha"] = sha
}

func (s *githubFixtureState) mints() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.mintedTokens)
}

func (s *githubFixtureState) reviewWriteFacts() (int, map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reviewWrites) == 0 {
		return 0, nil
	}
	return len(s.reviewWrites), s.reviewWrites[len(s.reviewWrites)-1]
}

// verifyAppJWT accepts only a JWT the fixture's own key signed, issued by
// the fixture App and valid for at most GitHub's ten minutes.
func (s *githubFixtureState) verifyAppJWT(authorization string) bool {
	token := strings.TrimPrefix(authorization, "Bearer ")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	hashed := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&s.key.PublicKey, crypto.SHA256, hashed[:], signature); err != nil {
		return false
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return false
	}
	now := time.Now().Unix()
	return claims.Iss == githubFixtureAppID && claims.Exp > now && claims.Iat <= now && claims.Exp-claims.Iat <= 600
}

func (s *githubFixtureState) installationAuthorised(authorization string) bool {
	token := strings.TrimPrefix(authorization, "Bearer ")
	for _, minted := range s.mintedTokens {
		if minted == token {
			return true
		}
	}
	return false
}

func (s *githubFixtureState) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/api/v3")
	authorization := r.Header.Get("Authorization")
	if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") == "" {
		http.Error(w, `{"message":"missing GitHub API headers"}`, http.StatusBadRequest)
		return
	}
	if s.installationAuthorised(authorization) && s.handleOperations(w, r, path) {
		return
	}

	switch {
	case r.Method == http.MethodGet && path == "/app":
		if !s.verifyAppJWT(authorization) {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		writeFixtureJSON(s.t, w, map[string]any{"id": 4242, "slug": githubFixtureAppSlug})
	case r.Method == http.MethodPost && path == "/app/installations/"+githubFixtureInstallationID+"/access_tokens":
		if !s.verifyAppJWT(authorization) {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		token := fmt.Sprintf("ghs_fixture_%d", len(s.mintedTokens)+1)
		s.mintedTokens = append(s.mintedTokens, token)
		w.WriteHeader(http.StatusCreated)
		writeFixtureJSON(s.t, w, map[string]any{"token": token, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	case !s.installationAuthorised(authorization):
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	case r.Method == http.MethodGet && path == "/repos/acme/widgets":
		writeFixtureJSON(s.t, w, map[string]any{"full_name": "acme/widgets", "clone_url": s.cloneURL, "default_branch": "trunk"})
	case r.Method == http.MethodGet && path == "/repos/acme/widgets/pulls/7":
		writeFixtureJSON(s.t, w, s.pullRequest)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/acme/widgets/branches/"):
		writeFixtureJSON(s.t, w, map[string]any{"name": "main", "commit": map[string]any{"sha": s.baseTip}})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/acme/widgets/commits/") && strings.HasSuffix(path, "/statuses"):
		writeFixtureArray(s.t, w, s.pagedCollection(r, s.statuses))
	case r.Method == http.MethodGet && path == "/repos/acme/widgets/pulls/7/reviews":
		if s.refuseReviewReads != 0 {
			http.Error(w, `{"message":"unavailable"}`, s.refuseReviewReads)
			return
		}
		writeFixtureArray(s.t, w, s.pagedCollection(r, s.reviews))
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/acme/widgets/pulls/7/reviews/") && strings.HasSuffix(path, "/comments"):
		var id int64
		fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(path, "/repos/acme/widgets/pulls/7/reviews/"), "/comments"), "%d", &id)
		writeFixtureArray(s.t, w, s.pagedCollection(r, s.reviewComments[id]))
	case r.Method == http.MethodPost && path == "/repos/acme/widgets/pulls/7/reviews":
		if s.refuseReviewPosts != 0 {
			http.Error(w, `{"message":"API rate limit exceeded"}`, s.refuseReviewPosts)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.reviewWrites = append(s.reviewWrites, payload)
		comments, _ := payload["comments"].([]any)
		for _, raw := range comments {
			comment := raw.(map[string]any)
			line := int64(comment["line"].(float64))
			if !slices.Contains(s.diffLines[comment["path"].(string)], line) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				writeFixtureJSON(s.t, w, map[string]any{
					"message": "Validation Failed",
					"errors":  []any{map[string]any{"resource": "PullRequestReviewComment", "field": "line", "message": fmt.Sprintf("line %d is not part of the diff", line)}},
				})
				return
			}
		}
		state := map[string]string{"APPROVE": "APPROVED", "REQUEST_CHANGES": "CHANGES_REQUESTED", "COMMENT": "COMMENTED"}[payload["event"].(string)]
		id := int64(600 + len(s.reviews))
		// GitHub reports the review's page anchor on its own web host — a
		// reader of html_url must take the fragment and nothing else.
		s.reviews = append(s.reviews, map[string]any{
			"id": id, "state": state, "commit_id": payload["commit_id"], "body": payload["body"],
			"user":     map[string]any{"login": githubFixtureBotLogin},
			"html_url": fmt.Sprintf("https://github.example/acme/widgets/pull/7#pullrequestreview-%d", id),
		})
		for _, raw := range comments {
			comment := raw.(map[string]any)
			s.reviewComments[id] = append(s.reviewComments[id], map[string]any{"path": comment["path"], "body": comment["body"], "line": comment["line"], "side": comment["side"]})
		}
		w.WriteHeader(http.StatusOK)
		writeFixtureJSON(s.t, w, map[string]any{"id": id, "state": state})
	default:
		http.Error(w, fmt.Sprintf(`{"message":"unexpected %s %s"}`, r.Method, path), http.StatusNotFound)
	}
}

// pagedCollection slices a collection by per_page and page as GitHub does,
// answering [] past the last page, so a sibling case holding more records
// than one page proves the adaptation's paging rather than its first read.
func (s *githubFixtureState) pagedCollection(r *http.Request, values []map[string]any) []map[string]any {
	perPage, page := 30, 1
	if n, err := strconv.Atoi(r.URL.Query().Get("per_page")); err == nil && n > 0 {
		perPage = min(n, 100)
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 0 {
		page = n
	}
	start := (page - 1) * perPage
	if start >= len(values) {
		return nil
	}
	return values[start:min(start+perPage, len(values))]
}

func githubWebhook(event string, payload map[string]any) (string, []byte) {
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return event, body
}

func runGitHubNormaliseEvent(t *testing.T, event string, body []byte) Facts {
	t.Helper()
	script := filepath.Join("..", "..", "scripts", "adaptations", "github", "normalise-event")
	cmd := exec.Command(script)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = append(os.Environ(), "MINOS_HEADER_X_GITHUB_EVENT="+event)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("normalise-event failed: %v\n%s", err, out)
	}
	facts, err := ParseFactsAllowUnmapped(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("ParseFacts failed: %v\n%s", err, out)
	}
	return facts
}

func TestGitHubWebhookEventsNormaliseToTheOccasionVocabulary(t *testing.T) {
	repository := map[string]any{"name": "widgets", "owner": map[string]any{"login": "acme"}}
	pullRequest := map[string]any{"number": 7, "head": map[string]any{"sha": "feedface"}, "base": map[string]any{"ref": "main"}}
	cases := []struct {
		name, event string
		payload     map[string]any
		occasion    string
	}{
		{"opened", "pull_request", map[string]any{"action": "opened", "repository": repository, "pull_request": pullRequest}, "pr-opened"},
		{"synchronize", "pull_request", map[string]any{"action": "synchronize", "repository": repository, "pull_request": pullRequest}, "pr-synchronized"},
		{"ready for review", "pull_request", map[string]any{"action": "ready_for_review", "repository": repository, "pull_request": pullRequest}, "pr-edited"},
		{"converted to draft", "pull_request", map[string]any{"action": "converted_to_draft", "repository": repository, "pull_request": pullRequest}, "pr-edited"},
		{"closed", "pull_request", map[string]any{"action": "closed", "repository": repository, "pull_request": pullRequest}, "pr-closed"},
		{"labeled", "pull_request", map[string]any{"action": "labeled", "repository": repository, "pull_request": pullRequest}, "label-updated"},
		{"review approved", "pull_request_review", map[string]any{"action": "submitted", "repository": repository, "pull_request": pullRequest, "review": map[string]any{"state": "approved"}}, "review-approved"},
		{"review changes requested", "pull_request_review", map[string]any{"action": "submitted", "repository": repository, "pull_request": pullRequest, "review": map[string]any{"state": "changes_requested"}}, "review-rejected"},
		{"review commented", "pull_request_review", map[string]any{"action": "submitted", "repository": repository, "pull_request": pullRequest, "review": map[string]any{"state": "commented"}}, ""},
		{"comment on a pull request", "issue_comment", map[string]any{"action": "created", "repository": repository, "issue": map[string]any{"number": 7, "pull_request": map[string]any{"url": "https://api.github.invalid/pulls/7"}}}, "comment-created"},
		{"comment on a plain issue", "issue_comment", map[string]any{"action": "created", "repository": repository, "issue": map[string]any{"number": 12}}, ""},
		{"app installed", "installation", map[string]any{"action": "created", "repositories": []any{}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event, body := githubWebhook(tc.event, tc.payload)
			facts := runGitHubNormaliseEvent(t, event, body)
			if facts.Occasion != tc.occasion {
				t.Fatalf("occasion = %q, want %q (%#v)", facts.Occasion, tc.occasion, facts)
			}
			if tc.occasion == "" {
				return
			}
			if facts.Owner != "acme" || facts.Repo != "widgets" || facts.PR != "7" {
				t.Fatalf("repository facts not normalised: %#v", facts)
			}
			if tc.event == "pull_request" && (facts.HeadSHA != "feedface" || facts.BaseRef != "main") {
				t.Fatalf("pull request facts not normalised: %#v", facts)
			}
		})
	}
}

// GitHub signs deliveries as sha256=<hex> in X-Hub-Signature-256 and names
// its event in X-GitHub-Event; the receiver verifies the one and forwards
// the other without knowing either name.
func TestReceiverAcceptsGitHubSignedDeliveriesThroughTheGitHubAdaptation(t *testing.T) {
	cfg := receiverTestConfig(t)
	adaptation, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "github"))
	if err != nil {
		t.Fatal(err)
	}
	local := cfg.Forges["local"]
	local.Adaptation = adaptation
	local.SignatureHeader = "X-Hub-Signature-256"
	cfg.Forges["local"] = local

	_, body := githubWebhook("pull_request", map[string]any{
		"action":       "opened",
		"repository":   map[string]any{"name": "widgets", "owner": map[string]any{"login": "acme"}},
		"pull_request": map[string]any{"number": 7, "head": map[string]any{"sha": "feedface"}, "base": map[string]any{"ref": "main"}},
	})
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	request := httptest.NewRequest(http.MethodPost, "/hooks/local", bytes.NewReader(body))
	request.Header.Set("X-GitHub-Event", "pull_request")
	request.Header.Set("X-GitHub-Delivery", "72d3162e-cc78-11e3-81ab-4c9367dc0958")
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	if err := handleHook(context.Background(), cfg, response, request); err != nil {
		t.Fatalf("handleHook() error = %v", err)
	}
	// The delivery was verified and normalised to a mapped occasion; the
	// repository is simply not configured, which is the next decision.
	if response.Code != http.StatusAccepted || response.Body.String() != "not opted in\n" {
		t.Fatalf("status = %d body = %q, want 202 not opted in", response.Code, response.Body.String())
	}

	tampered := httptest.NewRequest(http.MethodPost, "/hooks/local", bytes.NewReader(append(body, '\n')))
	tampered.Header.Set("X-GitHub-Event", "pull_request")
	tampered.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	response = httptest.NewRecorder()
	if err := handleHook(context.Background(), cfg, response, tampered); err != nil {
		t.Fatalf("handleHook() error = %v", err)
	}
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("tampered delivery status = %d, want 401", response.Code)
	}
}

func TestGitHubSnapshotMintsOneInstallationTokenAndSpeaksTheProtocolVocabulary(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter := state.adapter(t)
	repository := forge.Repository{Owner: "acme", Name: "widgets"}

	snapshot, err := adapter.Snapshot(t.Context(), repository, 7)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.AuthenticatedUser != githubFixtureBotLogin {
		t.Fatalf("authenticated user = %q, want the App's bot login", snapshot.AuthenticatedUser)
	}
	if !snapshot.Draft || snapshot.State != "open" || snapshot.Author != "author" ||
		snapshot.Title != "Widget change" || snapshot.Body != "Out of scope: the gears." || snapshot.HeadRepository != "acme/widgets" {
		t.Fatalf("pull request facts = %#v", snapshot)
	}
	if snapshot.TargetSHA != state.baseTip {
		t.Fatalf("target sha = %q, want the base branch tip %q, not the pull request's opening base", snapshot.TargetSHA, state.baseTip)
	}
	if snapshot.HeadBranch != "topic" || snapshot.TargetBranch != "main" {
		t.Fatalf("branches = %q -> %q", snapshot.HeadBranch, snapshot.TargetBranch)
	}
	if len(snapshot.Statuses) != 101 || snapshot.Statuses[0].ID != 131 || snapshot.Statuses[100].ID != 31 {
		t.Fatalf("statuses = %d (first %d, last %d), want all 101 across two pages newest first", len(snapshot.Statuses), snapshot.Statuses[0].ID, snapshot.Statuses[len(snapshot.Statuses)-1].ID)
	}
	if len(snapshot.Reviews) != 1 || snapshot.Reviews[0].State != "REQUEST_CHANGES" || snapshot.Reviews[0].User != "colleague" {
		t.Fatalf("reviews = %#v, want CHANGES_REQUESTED mapped to the protocol's REQUEST_CHANGES", snapshot.Reviews)
	}
	if snapshot.Statuses[100].Provider != "github" || snapshot.Statuses[100].Creator != "ci-bot" || snapshot.Statuses[100].State != forge.StatusSuccess {
		t.Fatalf("status = %#v", snapshot.Statuses[100])
	}
	if !snapshot.DependenciesAvailable || snapshot.DependencyError != "" || len(snapshot.OpenDependencies) != 0 {
		t.Fatalf("dependency facts = %#v; GitHub has no dependency link, so none are ever pending", snapshot)
	}
	if snapshot.Labels[0] != "needs-review" {
		t.Fatalf("labels = %#v", snapshot.Labels)
	}

	// A fork's branch is still the head's name: the run contract requires it.
	state.mu.Lock()
	state.pullRequest["head"].(map[string]any)["repo"] = map[string]any{"full_name": "forker/widgets"}
	state.mu.Unlock()
	forked, err := adapter.Snapshot(t.Context(), repository, 7)
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if forked.HeadBranch != "topic" || forked.HeadRepository != "forker/widgets" {
		t.Fatalf("fork head = %q in %q, want topic in forker/widgets", forked.HeadBranch, forked.HeadRepository)
	}
	if mints := state.mints(); mints != 1 {
		t.Fatalf("installation tokens minted = %d, want one reused from the cache across invocations", mints)
	}
}

// A run clones through the protocol: the repository's clone URL and default
// branch as GitHub reports them on the installation route, and the HTTPS
// git credential an App installation authenticates with — x-access-token
// and the installation token the adaptation minted.
func TestGitHubRepositoryMetadataAndCloneCredentialServeTheRunsClones(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter := state.adapter(t)

	metadata, err := adapter.RepositoryMetadata(t.Context(), forge.Repository{Owner: "acme", Name: "widgets"})
	if err != nil {
		t.Fatalf("repository metadata: %v", err)
	}
	if metadata != (forge.RepositoryMetadata{CloneURL: "https://github.example/acme/widgets.git", DefaultBranch: "trunk"}) {
		t.Fatalf("repository metadata = %#v", metadata)
	}
	var lookup *forge.RepositoryLookupError
	if _, err := adapter.RepositoryMetadata(t.Context(), forge.Repository{Owner: "acme", Name: "absent"}); !errors.As(err, &lookup) || lookup.Status != http.StatusNotFound {
		t.Fatalf("repository metadata for an absent repository = %v, want the forge's 404 as the lookup's outcome", err)
	}

	credential, err := adapter.CloneCredential(t.Context())
	if err != nil {
		t.Fatalf("clone credential: %v", err)
	}
	state.mu.Lock()
	minted := slices.Clone(state.mintedTokens)
	state.mu.Unlock()
	if credential.Username != "x-access-token" || len(minted) != 1 || credential.Password != minted[0] {
		t.Fatalf("clone credential username = %q, password is the minted installation token = %t (tokens minted: %d)",
			credential.Username, len(minted) == 1 && credential.Password == minted[0], len(minted))
	}
}

// A run configured against the GitHub adaptation clones the reviewed
// repository as the App installation: setup-workspace asks the real minos
// binary, which runs the GitHub adaptation against the fixture GitHub, and
// the git server accepts only x-access-token with a token the fixture minted.
// The service still exports the forge's API base and credential file, as it
// does for every run.
func TestGitHubConfiguredSetupWorkspaceClonesAsTheAppInstallation(t *testing.T) {
	state := newGitHubFixtureState(t)
	repository, head := createGitRepository(t, "AGENTS.md", "widget guidance\n")
	git := newAuthenticatedGitServer(t, func(username, password string) bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return username == "x-access-token" && slices.Contains(state.mintedTokens, password)
	})
	git.serve(t, repository)
	state.mu.Lock()
	state.pullRequest["head"].(map[string]any)["sha"] = head
	state.baseTip = head
	state.cloneURL = git.URL + "/" + filepath.Base(filepath.Dir(repository)) + "/source"
	state.mu.Unlock()

	configRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configRoot, "service.toml"), []byte(fmt.Sprintf(`[service]
bot-login = %q
[listener]
bind = ":0"
[forges.github]
adaptation = %q
api-base = %q
webhook-secret-file = %q
credential-file = %q
[runs]
dir = %q
`, githubFixtureBotLogin, state.adaptationPath, state.server.URL, state.credentialPath, state.credentialPath, t.TempDir())), 0o600); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	cmd := setupWorkspaceCommand(t, buildMinos(t), runDir, workspace, filepath.Join(runDir, "orientation.json"), head)
	cmd.Env = append(cmd.Env,
		"MINOS_CONFIG="+configRoot, "MINOS_FORGE=github",
		"MINOS_OWNER=acme", "MINOS_REPO_NAME=widgets", "MINOS_PR=7", "MINOS_HEAD_BRANCH=topic",
		"MINOS_API_BASE="+state.server.URL, "MINOS_CREDENTIAL_FILE="+state.credentialPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup-workspace failed: %v\n%s", err, output)
	}
	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != head {
		t.Fatalf("workspace head = %q, want %q", got, head)
	}
	if git.authorised() == 0 {
		t.Fatalf("the git server authorised no request")
	}
	assertContainsFile(t, filepath.Join(runDir, "pull-request.json"), `"title": "Widget change"`)
}

// clone-credential hands its secret to jq through the environment: a jq
// stand-in on PATH records every argument vector either adaptation gives
// it, and neither forge's secret may appear in one.
func TestCloneCredentialKeepsTheSecretOutOfEveryArgv(t *testing.T) {
	realJQ, err := exec.LookPath("jq")
	if err != nil {
		t.Fatal(err)
	}
	stand := t.TempDir()
	argvLog := filepath.Join(stand, "argv.log")
	writeScript(t, filepath.Join(stand, "jq"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >>'"+argvLog+"'\nexec '"+realJQ+"' \"$@\"\n")
	path := stand + string(os.PathListSeparator) + os.Getenv("PATH")

	forgejoAdaptation, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	github := newGitHubFixtureState(t)
	for _, test := range []struct {
		name   string
		runner forge.ScriptRunner
		secret func() string
	}{
		{name: "forgejo", runner: forge.ScriptRunner{Directory: forgejoAdaptation, APIBase: "http://forge.invalid", Credential: "forgejo-secret-719"}, secret: func() string { return "forgejo-secret-719" }},
		{name: "github", runner: github.runner(), secret: func() string {
			github.mu.Lock()
			defer github.mu.Unlock()
			return github.mintedTokens[len(github.mintedTokens)-1]
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(argvLog, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := test.runner.Run(t.Context(), forge.RunRequest{Operation: "clone-credential", Env: map[string]string{"PATH": path}})
			if err != nil {
				t.Fatalf("clone-credential: %v", err)
			}
			secret := test.secret()
			if !strings.Contains(string(out), secret) {
				t.Fatalf("clone-credential output does not carry the forge's secret")
			}
			recorded, err := os.ReadFile(argvLog)
			if err != nil {
				t.Fatal(err)
			}
			if len(recorded) == 0 {
				t.Fatalf("the jq stand-in recorded no invocation")
			}
			if strings.Contains(string(recorded), secret) {
				t.Fatalf("a recorded jq argv carries the secret")
			}
		})
	}
}

func TestGitHubGuardedPostReviewAnchorsByLineAndSideAndReadsBack(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter := state.adapter(t)
	comments := []forge.ReviewComment{
		{Path: "src/code.txt", Body: "Single-line concern.", NewPosition: 5},
		{Path: "src/code.txt", Body: "Range concern.", NewPosition: 4, ExtraLinesCount: 2},
		{Path: "src/code.txt", Body: "Removed-line concern.", OldPosition: 3},
	}

	result := adapter.PostReview(t.Context(), state.guard(), forge.ReviewRequestChanges, "Review body.", comments)
	if result.Outcome != forge.WriteApplied || result.Reason != "" {
		t.Fatalf("post review = %#v", result)
	}
	writes, payload := state.reviewWriteFacts()
	if writes != 1 {
		t.Fatalf("review writes = %d, want one", writes)
	}
	if payload["event"] != "REQUEST_CHANGES" || payload["commit_id"] != state.guard().HeadSHA || payload["body"] != "Review body." {
		t.Fatalf("review payload = %#v", payload)
	}
	posted := payload["comments"].([]any)
	want := []map[string]any{
		{"path": "src/code.txt", "body": "Single-line concern.", "line": float64(5), "side": "RIGHT"},
		{"path": "src/code.txt", "body": "Range concern.", "line": float64(6), "side": "RIGHT", "start_line": float64(4), "start_side": "RIGHT"},
		{"path": "src/code.txt", "body": "Removed-line concern.", "line": float64(3), "side": "LEFT"},
	}
	if len(posted) != len(want) {
		t.Fatalf("inline comments = %#v", posted)
	}
	for index, expected := range want {
		got := posted[index].(map[string]any)
		for key, value := range expected {
			if got[key] != value {
				t.Fatalf("comment %d %s = %#v, want %#v (comment %#v)", index, key, got[key], value, got)
			}
		}
		for _, absent := range []string{"new_position", "old_position", "extra_lines_count", "position"} {
			if _, leaked := got[absent]; leaked {
				t.Fatalf("comment %d carries the protocol's %s into GitHub's payload: %#v", index, absent, got)
			}
		}
	}

	again := adapter.PostReview(t.Context(), state.guard(), forge.ReviewRequestChanges, "Review body.", comments)
	if again.Outcome != forge.WriteApplied || again.Reason != "review already present" {
		t.Fatalf("repeated post = %#v", again)
	}
	if writes, _ := state.reviewWriteFacts(); writes != 1 {
		t.Fatalf("review writes after repeat = %d, want the first alone", writes)
	}
}

func TestGitHubGuardedPostReviewReportsTheForgesRefusalOfAnAnchor(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter := state.adapter(t)

	result := adapter.PostReview(t.Context(), state.guard(), forge.ReviewVerdictComment, "Review body.", []forge.ReviewComment{
		{Path: "src/code.txt", Body: "Outside the diff.", NewPosition: 40},
	})
	if result.Outcome != forge.WriteRejected {
		t.Fatalf("post review = %#v, want the forge's refusal reported as rejected", result)
	}
	if !strings.Contains(result.Reason, "HTTP 422") || !strings.Contains(result.Reason, "line 40 is not part of the diff") {
		t.Fatalf("refusal reason = %q", result.Reason)
	}
}

// Only a validation refusal is the forge's verdict on the payload; a rate
// limit or an expired token is settled by the read-back like any other
// guarded write, so a transient never ends a run as a rejection.
func TestGitHubGuardedPostReviewTreatsANonValidationFailureAsUncertain(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter := state.adapter(t)
	state.mu.Lock()
	state.refuseReviewPosts = http.StatusForbidden
	state.mu.Unlock()

	result := adapter.PostReview(t.Context(), state.guard(), forge.ReviewVerdictComment, "Review body.", nil)
	if result.Outcome != forge.WriteUncertain || result.Reason != "review write could not be discovered" {
		t.Fatalf("post review = %#v, want uncertain after a 403", result)
	}
}

func TestGitHubGuardedPostReviewRejectsAMovedHead(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter := state.adapter(t)
	guard := state.guard()
	state.moveHead("0123456789012345678901234567890123456789")

	result := adapter.PostReview(t.Context(), guard, forge.ReviewApprove, "Review body.", nil)
	if result.Outcome != forge.WriteRejected || result.Reason != "head moved" {
		t.Fatalf("post review = %#v", result)
	}
	if writes, _ := state.reviewWriteFacts(); writes != 0 {
		t.Fatalf("review writes = %d, want none after the guard refused", writes)
	}
}

func TestGitHubGuardedPostReviewRejectsAnIdentityOtherThanTheApp(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter, err := forge.NewAdapter(state.runner(), "someone-else[bot]", "Minos")
	if err != nil {
		t.Fatal(err)
	}
	result := adapter.PostReview(t.Context(), state.guard(), forge.ReviewApprove, "Review body.", nil)
	if result.Outcome != forge.WriteRejected || !strings.Contains(result.Reason, "authenticated forge identity is "+githubFixtureBotLogin) {
		t.Fatalf("post review = %#v", result)
	}
}

func TestGitHubAdaptationRefusesAMalformedCredential(t *testing.T) {
	state := newGitHubFixtureState(t)
	runner := state.runner()
	runner.Credential = "ghp_a_plain_token_is_not_an_app_registration"
	adapter, err := forge.NewAdapter(runner, githubFixtureBotLogin, "Minos")
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Snapshot(t.Context(), forge.Repository{Owner: "acme", Name: "widgets"}, 7)
	if err == nil || !strings.Contains(err.Error(), `"app-id", "installation-id" and "private-key-file"`) {
		t.Fatalf("snapshot error = %v, want the credential contract named", err)
	}
	if mints := state.mints(); mints != 0 {
		t.Fatalf("tokens minted = %d, want none from a malformed credential", mints)
	}
}

// GitHub accepts a review comment only on a line the diff carries, so the
// adaptation declares no out-of-hunk anchoring and the review boundary folds
// such findings into the body rather than letting the forge refuse them.
func TestGitHubAdaptationDeclaresNoOutOfHunkAnchoring(t *testing.T) {
	adaptation, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "github"))
	if err != nil {
		t.Fatal(err)
	}
	capabilities := readAnchoringCapabilities(adaptation)
	if capabilities.ReviewComments.OutOfHunkAnchoring {
		t.Fatal("the GitHub adaptation declares out-of-hunk anchoring GitHub does not offer")
	}
}

// These routes follow GitHub's REST documentation for pulls, commit statuses,
// reactions and issues/labels. Writes alone change the state read back by scripts.
func (s *githubFixtureState) handleOperations(w http.ResponseWriter, r *http.Request, path string) bool {
	if path == "/repos/acme/widgets/pulls/7/reviews" {
		return false
	}
	if r.Method == http.MethodGet && s.unreadablePath != "" && path == s.unreadablePath {
		http.Error(w, `{"message":"unavailable"}`, http.StatusServiceUnavailable)
		return true
	}
	if r.Method != http.MethodGet {
		var payload map[string]any
		if r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				s.t.Error(err)
				http.Error(w, "bad JSON", 400)
				return true
			}
		}
		// Review writes are handled by the original fixture route.
		if path != "/repos/acme/widgets/pulls/7/reviews" {
			s.writes = append(s.writes, githubFixtureWrite{r.Method, path, payload})
			if s.refuseMutation {
				http.Error(w, `{"message":"write unavailable"}`, 503)
				return true
			}
		}
		switch {
		case r.Method == http.MethodPost && path == "/repos/acme/widgets/statuses/"+s.pullRequest["head"].(map[string]any)["sha"].(string):
			state, _ := payload["state"].(string)
			if !slices.Contains([]string{"pending", "success", "failure", "error"}, state) {
				http.Error(w, "invalid state", 422)
				return true
			}
			record := map[string]any{"id": 1000 + len(s.writes), "creator": map[string]any{"login": githubFixtureBotLogin}}
			for key, value := range payload {
				record[key] = value
			}
			s.statuses = append(s.statuses, record)
			w.WriteHeader(201)
			writeFixtureJSON(s.t, w, record)
			return true
		case r.Method == http.MethodPost && path == "/repos/acme/widgets/issues/7/reactions":
			content, _ := payload["content"].(string)
			if !slices.Contains([]string{"+1", "-1", "laugh", "confused", "heart", "hooray", "rocket", "eyes"}, content) {
				http.Error(w, "invalid reaction", 422)
				return true
			}
			record := map[string]any{"id": 700 + len(s.writes), "content": content, "user": map[string]any{"login": githubFixtureBotLogin}}
			s.reactions = append(s.reactions, record)
			w.WriteHeader(201)
			writeFixtureJSON(s.t, w, record)
			return true
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/repos/acme/widgets/issues/7/reactions/"):
			id, err := strconv.Atoi(strings.TrimPrefix(path, "/repos/acme/widgets/issues/7/reactions/"))
			if err != nil {
				http.Error(w, "bad reaction id", 422)
				return true
			}
			s.reactions = slices.DeleteFunc(s.reactions, func(record map[string]any) bool {
				return record["id"] == id && record["user"].(map[string]any)["login"] == githubFixtureBotLogin
			})
			w.WriteHeader(204)
			return true
		case r.Method == http.MethodPost && path == "/repos/acme/widgets/issues/7/labels":
			names, ok := payload["labels"].([]any)
			if !ok {
				http.Error(w, "bad labels", 422)
				return true
			}
			for _, raw := range names {
				name, ok := raw.(string)
				if !ok {
					http.Error(w, "label must be a name", 422)
					return true
				}
				found := false
				for _, label := range s.definedLabels {
					if label["name"] == name {
						s.pullRequest["labels"] = append(s.pullRequest["labels"].([]map[string]any), label)
						found = true
						break
					}
				}
				if !found {
					http.Error(w, "label undefined", 422)
					return true
				}
			}
			writeFixtureArray(s.t, w, s.pullRequest["labels"].([]map[string]any))
			return true
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/repos/acme/widgets/issues/7/labels/"):
			name := strings.TrimPrefix(path, "/repos/acme/widgets/issues/7/labels/")
			s.pullRequest["labels"] = slices.DeleteFunc(s.pullRequest["labels"].([]map[string]any), func(label map[string]any) bool { return label["name"] == name })
			writeFixtureArray(s.t, w, s.pullRequest["labels"].([]map[string]any))
			return true
		case r.Method == http.MethodPost && path == "/repos/acme/widgets/issues":
			record := map[string]any{"number": 100 + len(s.writes), "state": "open", "user": map[string]any{"login": githubFixtureBotLogin}}
			for key, value := range payload {
				record[key] = value
			}
			s.issues = append(s.issues, record)
			w.WriteHeader(201)
			writeFixtureJSON(s.t, w, record)
			return true
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/repos/acme/widgets/issues/") && strings.HasSuffix(path, "/comments"):
			w.WriteHeader(201)
			writeFixtureJSON(s.t, w, map[string]any{"id": 800 + len(s.writes), "body": payload["body"]})
			return true
		}
	}
	if r.Method != http.MethodGet {
		return false
	}
	var records []map[string]any
	switch path {
	case "/repos/acme/widgets/pulls":
		if r.URL.Query().Get("state") != "open" {
			s.t.Error("pull listing must select open")
		}
		records = s.pullRequests
	case "/repos/acme/widgets/issues/7/reactions":
		records = s.reactions
	case "/repos/acme/widgets/labels":
		records = s.definedLabels
	case "/repos/acme/widgets/issues/7/labels":
		records = s.pullRequest["labels"].([]map[string]any)
	case "/repos/acme/widgets/issues":
		state := r.URL.Query().Get("state")
		if state != "all" && state != "open" {
			s.t.Error("issue listing needs explicit state")
		}
		for _, issue := range s.issues {
			if state == "all" || issue["state"] == "open" {
				records = append(records, issue)
			}
		}
	default:
		return false
	}
	writeFixtureArray(s.t, w, s.pagedCollection(r, records))
	return true
}

func (s *githubFixtureState) operationWrites() []githubFixtureWrite {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.writes)
}

func githubApplied(t *testing.T, result forge.WriteResult) {
	t.Helper()
	if result.Outcome != forge.WriteApplied {
		t.Fatalf("operation = %#v, want applied", result)
	}
}

func TestGitHubClaimWritesOnlyTheConfiguredMarker(t *testing.T) {
	for _, marker := range []forge.Marker{{Reaction: "eyes"}, {Label: "review active"}} {
		name := marker.Reaction + marker.Label
		t.Run(name, func(t *testing.T) {
			state := newGitHubFixtureState(t)
			state.mu.Lock()
			state.pullRequest["draft"] = false
			state.mu.Unlock()
			state.mu.Lock()
			state.definedLabels = []map[string]any{{"id": 42, "name": "review active"}}
			state.mu.Unlock()
			data, _ := json.Marshal(map[string]forge.Marker{"in-flight": marker})
			t.Setenv("MINOS_MARKERS", string(data))
			adapter := state.adapter(t)
			githubApplied(t, adapter.Claim(t.Context(), state.guard().Repository, 7))
			githubApplied(t, adapter.Claim(t.Context(), state.guard().Repository, 7))
			writes := state.operationWrites()
			wantPath := "/repos/acme/widgets/issues/7/reactions"
			wantPayload := map[string]any{"content": "eyes"}
			if marker.Label != "" {
				wantPath = "/repos/acme/widgets/issues/7/labels"
				wantPayload = map[string]any{"labels": []any{"review active"}}
			}
			if len(writes) != 1 || writes[0].path != wantPath || !reflect.DeepEqual(writes[0].payload, wantPayload) {
				t.Fatalf("claim writes = %#v", writes)
			}
		})
	}
}

func TestGitHubClaimRefusesMissingLabelAndWrongIdentity(t *testing.T) {
	for _, name := range []string{"missing label", "wrong identity"} {
		t.Run(name, func(t *testing.T) {
			state := newGitHubFixtureState(t)
			t.Setenv("MINOS_MARKERS", `{"in-flight":{"label":"missing"}}`)
			adapter := state.adapter(t)
			if name == "wrong identity" {
				adapter, _ = forge.NewAdapter(state.runner(), "wrong[bot]", "Minos")
			}
			result := adapter.Claim(t.Context(), state.guard().Repository, 7)
			reason := "not defined"
			if name == "wrong identity" {
				reason = "authenticated forge identity"
			}
			if result.Outcome != forge.WriteRejected || !strings.Contains(result.Reason, reason) {
				t.Fatalf("claim = %#v", result)
			}
			if writes := state.operationWrites(); len(writes) != 0 {
				t.Fatalf("refused claim wrote %#v", writes)
			}
		})
	}
}

func TestGitHubListOpenPullRequestsPagesFacts(t *testing.T) {
	state := newGitHubFixtureState(t)
	for i := 1; i <= 101; i++ {
		state.mu.Lock()
		state.pullRequests = append(state.pullRequests, map[string]any{"number": i, "head": map[string]any{"sha": fmt.Sprintf("head-%d", i)}, "base": map[string]any{"ref": "parent"}})
		state.mu.Unlock()
	}
	// Exercise the service consumer as well as the protocol runner.
	adaptation := Adaptation{Dir: state.adaptationPath, APIBase: state.server.URL, Credential: state.credential}
	facts, err := adaptation.ListOpenPRs(t.Context(), "github", "acme", "widgets")
	if err != nil {
		t.Fatalf("list open PRs: %v", err)
	}
	if len(facts) != 101 || facts[100].PR != "101" || facts[100].HeadSHA != "head-101" || facts[100].BaseRef != "parent" || facts[100].Occasion != "reconcile" || facts[100].Forge != "github" {
		t.Fatalf("open PR facts = %#v", facts)
	}
}

func TestGitHubCommitStatusesMatchesSnapshotProjection(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter := state.adapter(t)
	statuses, err := adapter.CommitStatuses(t.Context(), state.guard().Repository, state.guard().HeadSHA)
	if err != nil {
		t.Fatalf("commit statuses: %v", err)
	}
	snapshot, err := adapter.Snapshot(t.Context(), state.guard().Repository, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 101 || statuses[0].ID != 131 || statuses[100].Creator != "ci-bot" || !reflect.DeepEqual(statuses, snapshot.Statuses) {
		t.Fatalf("status projection = %#v", statuses)
	}
}

func TestGitHubStatusWritesReadBackAndIgnoreTargetMovement(t *testing.T) {
	for _, stateValue := range []product.State{product.Working(), product.Incomplete(), product.Clean(), product.Attention()} {
		t.Run(stateValue.Name(), func(t *testing.T) {
			state := newGitHubFixtureState(t)
			adapter := state.adapter(t)
			guard := state.guard()
			state.mu.Lock()
			state.baseTip = "advanced-target"
			state.mu.Unlock()
			githubApplied(t, adapter.SetProductStatus(t.Context(), guard, stateValue))
			githubApplied(t, adapter.SetProductStatus(t.Context(), guard, stateValue))
			writes := state.operationWrites()
			wantURL := state.server.URL + "/acme/widgets/pull/7"
			if len(writes) != 1 || writes[0].path != "/repos/acme/widgets/statuses/"+guard.HeadSHA || writes[0].payload["target_url"] != wantURL || writes[0].payload["state"] != string(stateValue.ForgeState()) || writes[0].payload["context"] != "Minos" || writes[0].payload["description"] != stateValue.Description() {
				t.Fatalf("status writes = %#v", writes)
			}
			statuses, err := adapter.CommitStatuses(t.Context(), guard.Repository, guard.HeadSHA)
			if err != nil {
				t.Fatal(err)
			}
			if statuses[0].Creator != githubFixtureBotLogin || statuses[0].State != forge.StatusState(stateValue.ForgeState()) {
				t.Fatalf("status read-back = %#v", statuses[0])
			}
		})
	}
}

func TestGitHubMarkersWriteAndRemoveTheirOwnState(t *testing.T) {
	for _, marker := range []forge.Marker{{Reaction: "+1"}, {Label: "review done"}} {
		t.Run(marker.Reaction+marker.Label, func(t *testing.T) {
			state := newGitHubFixtureState(t)
			state.mu.Lock()
			state.definedLabels = []map[string]any{{"id": 43, "name": "review done"}}
			state.mu.Unlock()
			// A colleague's reaction must neither satisfy the bot's write nor be removed.
			state.mu.Lock()
			state.reactions = []map[string]any{{"id": 22, "content": "+1", "user": map[string]any{"login": "colleague"}}}
			state.mu.Unlock()
			adapter := state.adapter(t)
			guard := state.guard()
			githubApplied(t, adapter.AddMarker(t.Context(), guard, marker))
			githubApplied(t, adapter.AddMarker(t.Context(), guard, marker))
			githubApplied(t, adapter.RemoveMarker(t.Context(), guard, marker))
			githubApplied(t, adapter.RemoveMarker(t.Context(), guard, marker))
			writes := state.operationWrites()
			if len(writes) != 2 || writes[0].method != "POST" || writes[1].method != "DELETE" {
				t.Fatalf("marker writes = %#v", writes)
			}
			if marker.Reaction != "" {
				state.mu.Lock()
				reactions := slices.Clone(state.reactions)
				state.mu.Unlock()
				if len(reactions) != 1 || reactions[0]["id"] != 22 || writes[1].path != "/repos/acme/widgets/issues/7/reactions/701" {
					t.Fatalf("reaction cleanup = %#v, writes %#v", reactions, writes)
				}
			} else {
				snapshot, err := adapter.Snapshot(t.Context(), guard.Repository, 7)
				if err != nil {
					t.Fatal(err)
				}
				if slices.Contains(snapshot.Labels, marker.Label) || writes[1].path != "/repos/acme/widgets/issues/7/labels/review done" {
					t.Fatalf("label cleanup = %#v, writes %#v", snapshot.Labels, writes)
				}
			}
		})
	}
}

func TestGitHubHeadBoundWritesRefuseMovedHeads(t *testing.T) {
	for _, op := range []string{"status", "add reaction", "remove reaction", "add label", "remove label"} {
		t.Run(op, func(t *testing.T) {
			state := newGitHubFixtureState(t)
			adapter := state.adapter(t)
			guard := state.guard()
			marker := forge.Marker{Reaction: "eyes"}
			if strings.Contains(op, "label") {
				marker = forge.Marker{Label: "review active"}
				state.mu.Lock()
				state.definedLabels = []map[string]any{{"id": 42, "name": marker.Label}}
				state.mu.Unlock()
			}
			if strings.HasPrefix(op, "remove") {
				githubApplied(t, adapter.AddMarker(t.Context(), guard, marker))
			}
			before := len(state.operationWrites())
			state.moveHead("moved")
			var result forge.WriteResult
			switch {
			case op == "status":
				result = adapter.SetProductStatus(t.Context(), guard, product.Clean())
			case strings.HasPrefix(op, "remove"):
				result = adapter.RemoveMarker(t.Context(), guard, marker)
			default:
				result = adapter.AddMarker(t.Context(), guard, marker)
			}
			if result.Outcome != forge.WriteRejected {
				t.Fatalf("moved-head write = %#v, want rejected", result)
			}
			if len(state.operationWrites()) != before {
				t.Fatal("moved-head write mutated the forge")
			}
		})
	}
}

func TestGitHubMarkerCleanupAcceptsOnlyMatchingMergedHeads(t *testing.T) {
	for _, marker := range []forge.Marker{{Reaction: "eyes"}, {Label: "review active"}} {
		for _, moved := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s%s/moved=%t", marker.Reaction, marker.Label, moved), func(t *testing.T) {
				state := newGitHubFixtureState(t)
				state.mu.Lock()
				state.definedLabels = []map[string]any{{"id": 42, "name": "review active"}}
				state.mu.Unlock()
				adapter := state.adapter(t)
				guard := state.guard()
				githubApplied(t, adapter.AddMarker(t.Context(), guard, marker))
				state.mu.Lock()
				state.pullRequest["state"] = "closed"
				state.pullRequest["merged"] = true
				state.baseTip = "landed-parent"
				state.mu.Unlock()
				if moved {
					state.moveHead("moved")
				}
				result := adapter.RemoveMarker(t.Context(), guard, marker)
				if moved {
					if result.Outcome != forge.WriteRejected || len(state.operationWrites()) != 1 {
						t.Fatalf("merged moved-head cleanup = %#v", result)
					}
				} else {
					githubApplied(t, result)
					if len(state.operationWrites()) != 2 {
						t.Fatal("matching merged marker was not removed")
					}
				}
			})
		}
	}
}

func TestGitHubFilingDeduplicatesOpenAndClosedIssuesButNotPullRequests(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter := state.adapter(t)
	repository := state.guard().Repository
	body := "Finding. <!-- minos:entry_1 -->"
	// A pull request with the same marker is not a filed issue.
	state.mu.Lock()
	state.issues = []map[string]any{{"number": 7, "state": "open", "title": "Finding", "body": body, "pull_request": map[string]any{"url": "pull"}}}
	state.mu.Unlock()
	result := adapter.FileIssue(t.Context(), repository, "Finding", body)
	githubApplied(t, result)
	if result.Written == nil || *result.Written != 1 {
		t.Fatalf("first filing = %#v", result)
	}
	for _, closed := range []bool{false, true} {
		if closed {
			state.mu.Lock()
			state.issues[1]["state"] = "closed"
			state.mu.Unlock()
			state.moveHead("changed-after-filing")
		}
		result = adapter.FileIssue(t.Context(), repository, "Changed title", body)
		githubApplied(t, result)
		if result.Written == nil || *result.Written != 0 || len(state.operationWrites()) != 1 {
			t.Fatalf("repeated filing = %#v, writes %#v", result, state.operationWrites())
		}
	}
	writes := state.operationWrites()
	if writes[0].path != "/repos/acme/widgets/issues" || writes[0].payload["body"] != body {
		t.Fatalf("filing writes = %#v", writes)
	}
}

func TestGitHubFilingRefusesWrongIdentity(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter, _ := forge.NewAdapter(state.runner(), "wrong[bot]", "Minos")
	result := adapter.FileIssue(t.Context(), state.guard().Repository, "Finding", "Body <!-- minos:entry_1 -->")
	if result.Outcome != forge.WriteRejected || !strings.Contains(result.Reason, "identity") || len(state.operationWrites()) != 0 {
		t.Fatalf("identity refusal = %#v", result)
	}
}

func TestGitHubAlertsUseIssuesAndRepeatAsComments(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter := state.adapter(t)
	repository := state.guard().Repository
	state.mu.Lock()
	state.issues = []map[string]any{{"number": 7, "state": "open", "title": "Sweep failed", "pull_request": map[string]any{"url": "pull"}}}
	state.mu.Unlock()
	githubApplied(t, adapter.Alert(t.Context(), repository, "Sweep failed", "First alert"))
	githubApplied(t, adapter.Alert(t.Context(), repository, "Sweep failed", "Repeat alert"))
	writes := state.operationWrites()
	if len(writes) != 2 || writes[0].path != "/repos/acme/widgets/issues" || writes[0].payload["body"] != "First alert" || writes[1].path != "/repos/acme/widgets/issues/101/comments" || writes[1].payload["body"] != "Repeat alert" {
		t.Fatalf("alert writes = %#v", writes)
	}
}

func TestGitHubStackedChildClaimSurvivesRetarget(t *testing.T) {
	state := newGitHubFixtureState(t)
	state.mu.Lock()
	state.pullRequest["draft"] = false
	state.mu.Unlock()
	state.mu.Lock()
	state.pullRequest["base"].(map[string]any)["ref"] = "parent"
	state.mu.Unlock()
	t.Setenv("MINOS_MARKERS", `{"in-flight":{"reaction":"eyes"}}`)
	adapter := state.adapter(t)
	guard := state.guard()
	repository := guard.Repository
	githubApplied(t, adapter.Claim(t.Context(), repository, 7))
	snapshot, err := adapter.Snapshot(t.Context(), repository, 7)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ServiceConfig{}
	cfg.Service.BotLogin = githubFixtureBotLogin
	cfg.Service.StatusContext = "Minos"
	eligibility := assessPullRequestAdmission(cfg, RepoConfig{}, snapshot)
	if snapshot.TargetBranch != "parent" || snapshot.TargetSHA != guard.TargetSHA || eligibility.dependencyDeferred || eligibility.completedRun {
		t.Fatalf("stacked child snapshot %#v, admission %#v", snapshot, eligibility)
	}
	// The lead's real writes establish completion; the fixture does not seed it.
	githubApplied(t, adapter.SetProductStatus(t.Context(), guard, product.Clean()))
	githubApplied(t, adapter.PostReview(t.Context(), guard, forge.ReviewApprove, "Child reviewed against parent.", nil))
	githubApplied(t, adapter.AddMarker(t.Context(), guard, forge.Marker{Reaction: "+1"}))
	before := len(state.operationWrites())
	// GitHub retargets the child when the parent lands; its head is unchanged.
	state.mu.Lock()
	state.pullRequest["base"].(map[string]any)["ref"] = "main"
	state.baseTip = "parent-landed"
	state.mu.Unlock()
	snapshot, err = adapter.Snapshot(t.Context(), repository, 7)
	if err != nil {
		t.Fatal(err)
	}
	eligibility = assessPullRequestAdmission(cfg, RepoConfig{}, snapshot)
	if !eligibility.completedRun {
		t.Fatalf("retarget lost completion: %#v", eligibility)
	}
	result, err := reconcilePullRequestSnapshot(t.Context(), cfg, RepoConfig{}, Facts{Owner: "acme", Repo: "widgets", PR: "7"}, adapter, snapshot)
	if err != nil || result.Decision != ReconcileNothing || result.DeferralReason != "completed-marker" || snapshot.HeadSHA != guard.HeadSHA || len(state.operationWrites()) != before {
		t.Fatalf("retarget reconciliation = %#v, error %v, head %s target %s", result, err, snapshot.HeadSHA, snapshot.TargetSHA)
	}
	state.mu.Lock()
	reactions := slices.Clone(state.reactions)
	state.mu.Unlock()
	if len(reactions) != 2 {
		t.Fatalf("retarget spent markers: %#v", reactions)
	}
}

func TestGitHubWritesPreserveUncertainty(t *testing.T) {
	for _, op := range []string{"status", "reaction", "label", "file", "alert"} {
		t.Run(op, func(t *testing.T) {
			state := newGitHubFixtureState(t)
			adapter := state.adapter(t)
			guard := state.guard()
			state.mu.Lock()
			state.refuseMutation = true
			state.mu.Unlock()
			state.mu.Lock()
			state.definedLabels = []map[string]any{{"id": 42, "name": "review active"}}
			state.mu.Unlock()
			var result forge.WriteResult
			switch op {
			case "status":
				result = adapter.SetProductStatus(t.Context(), guard, product.Clean())
			case "reaction":
				result = adapter.AddMarker(t.Context(), guard, forge.Marker{Reaction: "eyes"})
			case "label":
				result = adapter.AddMarker(t.Context(), guard, forge.Marker{Label: "review active"})
			case "file":
				result = adapter.FileIssue(t.Context(), guard.Repository, "Finding", "Body <!-- minos:entry_1 -->")
			default:
				result = adapter.Alert(t.Context(), guard.Repository, "Alert", "Body")
			}
			if result.Outcome != forge.WriteUncertain || len(state.operationWrites()) != 1 {
				t.Fatalf("failed write = %#v, writes %#v", result, state.operationWrites())
			}
		})
	}
}

func TestGitHubStatusDetailsUseEnterpriseWebBase(t *testing.T) {
	state := newGitHubFixtureState(t)
	runner := state.runner()
	runner.APIBase += "/api/v3"
	adapter, err := forge.NewAdapter(runner, githubFixtureBotLogin, "Minos")
	if err != nil {
		t.Fatal(err)
	}
	guard := state.guard()
	githubApplied(t, adapter.SetProductStatus(t.Context(), guard, product.Clean()))
	writes := state.operationWrites()
	if len(writes) != 1 || writes[0].payload["target_url"] != state.server.URL+"/acme/widgets/pull/7" {
		t.Fatalf("enterprise details URL = %#v", writes)
	}
}

// The Details link lands on the newest review the App left on the reviewed
// head, at the anchor GitHub reports in that review's html_url
// (`pullrequestreview-<id>`, on GitHub's own host — only the fragment is
// taken); the App's earlier review on the same head, the colleague's review
// the fixture seeds on the head, and the App's review on an earlier head
// are not it. The link is the status's read-back identity, so a repeat
// converges, and a forge that cannot list reviews leaves the write
// uncertain rather than posting a link that may be wrong.
func TestGitHubStatusDetailsLandOnTheHeadsOwnReview(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter := state.adapter(t)
	guard := state.guard()
	state.mu.Lock()
	state.reviews = append(state.reviews,
		map[string]any{
			"id": 502, "state": "CHANGES_REQUESTED", "commit_id": "earlier-head", "body": "Earlier round",
			"user":     map[string]any{"login": githubFixtureBotLogin},
			"html_url": "https://github.example/acme/widgets/pull/7#pullrequestreview-502",
		},
		map[string]any{
			"id": 503, "state": "COMMENTED", "commit_id": guard.HeadSHA, "body": "The App's earlier note on this head",
			"user":     map[string]any{"login": githubFixtureBotLogin},
			"html_url": "https://github.example/acme/widgets/pull/7#pullrequestreview-503",
		})
	state.mu.Unlock()
	githubApplied(t, adapter.PostReview(t.Context(), guard, forge.ReviewRequestChanges, "Blocking review", nil))
	state.mu.Lock()
	posted := state.reviews[len(state.reviews)-1]
	state.mu.Unlock()
	if posted["user"].(map[string]any)["login"] != githubFixtureBotLogin || posted["commit_id"] != guard.HeadSHA {
		t.Fatalf("posted review = %#v, want the App's review on the head", posted)
	}

	githubApplied(t, adapter.SetProductStatus(t.Context(), guard, product.Attention()))
	githubApplied(t, adapter.SetProductStatus(t.Context(), guard, product.Attention()))
	writes := state.operationWrites()
	want := fmt.Sprintf("%s/acme/widgets/pull/7#pullrequestreview-%d", state.server.URL, posted["id"])
	if len(writes) != 1 || writes[0].payload["target_url"] != want {
		t.Fatalf("status writes = %#v, want one write with Details %s", writes, want)
	}

	state.mu.Lock()
	state.refuseReviewReads = http.StatusServiceUnavailable
	state.mu.Unlock()
	result := adapter.SetProductStatus(t.Context(), guard, product.Clean())
	if result.Outcome != forge.WriteUncertain || !strings.Contains(result.Reason, "reviews could not be read") {
		t.Fatalf("status with unreadable reviews = %#v, want uncertain naming the review listing", result)
	}
	if writes := state.operationWrites(); len(writes) != 1 {
		t.Fatalf("status writes after unreadable reviews = %d, want the earlier 1 alone", len(writes))
	}
}

func TestGitHubWebBaseMapsPublicAndEnterpriseHosts(t *testing.T) {
	for _, apiBase := range []string{"https://api.github.com/", "https://forge.example/api/v3/"} {
		t.Run(apiBase, func(t *testing.T) {
			state := newGitHubFixtureState(t)
			cmd := exec.Command("sh", "-c", `. "$1"; MINOS_API_BASE="$2"; web_base`, "web-base", filepath.Join(state.adaptationPath, "common.sh"), apiBase)
			cmd.Env = append(os.Environ(), "MINOS_API_BASE="+state.server.URL, "MINOS_FORGE_CREDENTIAL="+state.credential)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("web base execution: %v: %s", err, out)
			}
			want := "https://github.com"
			if strings.Contains(apiBase, "forge.example") {
				want = "https://forge.example"
			}
			if string(out) != want {
				t.Fatalf("web base = %q, want %q", out, want)
			}
		})
	}
}

func TestGitHubMissingMarkerLabelsAreRefused(t *testing.T) {
	state := newGitHubFixtureState(t)
	adapter := state.adapter(t)
	result := adapter.AddMarker(t.Context(), state.guard(), forge.Marker{Label: "undefined"})
	if result.Outcome != forge.WriteRejected || !strings.Contains(result.Reason, "not defined on acme/widgets") || len(state.operationWrites()) != 0 {
		t.Fatalf("undefined label write = %#v", result)
	}
}

func TestGitHubUnreadableMarkerStateNeverBecomesAbsence(t *testing.T) {
	for _, op := range []string{"claim", "add reaction", "remove reaction", "add label", "remove label"} {
		t.Run(op, func(t *testing.T) {
			state := newGitHubFixtureState(t)
			adapter := state.adapter(t)
			guard := state.guard()
			marker := forge.Marker{Reaction: "eyes"}
			t.Setenv("MINOS_MARKERS", `{"in-flight":{"reaction":"eyes"}}`)
			state.mu.Lock()
			state.unreadablePath = "/repos/acme/widgets/issues/7/reactions"
			state.mu.Unlock()
			if strings.Contains(op, "label") {
				marker = forge.Marker{Label: "review active"}
				state.mu.Lock()
				state.unreadablePath = "/repos/acme/widgets/issues/7/labels"
				state.mu.Unlock()
			}
			var result forge.WriteResult
			switch {
			case op == "claim":
				result = adapter.Claim(t.Context(), guard.Repository, 7)
			case strings.HasPrefix(op, "remove"):
				result = adapter.RemoveMarker(t.Context(), guard, marker)
			default:
				result = adapter.AddMarker(t.Context(), guard, marker)
			}
			if result.Outcome != forge.WriteUncertain || !strings.Contains(result.Reason, "marker") && !strings.Contains(result.Reason, "read-back") || len(state.operationWrites()) != 0 {
				t.Fatalf("unreadable marker = %#v, writes %#v", result, state.operationWrites())
			}
		})
	}
}

func TestGitHubStatusOnlyCompletionSurvivesRetarget(t *testing.T) {
	for _, outcome := range []product.State{product.Clean(), product.Attention()} {
		t.Run(outcome.Name(), func(t *testing.T) {
			state := newGitHubFixtureState(t)
			state.pullRequest["draft"] = false
			state.pullRequest["base"].(map[string]any)["ref"] = "parent"
			adapter := state.adapter(t)
			guard := state.guard()
			githubApplied(t, adapter.SetProductStatus(t.Context(), guard, outcome))
			marker := forge.Marker{Reaction: "+1"}
			if outcome == product.Attention() {
				marker.Reaction = "-1"
			}
			githubApplied(t, adapter.AddMarker(t.Context(), guard, marker))
			cfg := ServiceConfig{Root: t.TempDir()}
			cfg.Runs.Dir = t.TempDir()
			setTestRunCeilings(&cfg)
			cfg.Service.BotLogin = githubFixtureBotLogin
			cfg.Service.StatusContext = "Minos"
			cfg.Forges = map[string]ForgeConfig{"github": {Adaptation: state.adaptationPath, APIBase: state.server.URL, CredentialFile: state.credentialPath}}
			repo := RepoConfig{Forge: "github", Owner: "acme", Repo: "widgets"}
			repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"
			if outcome == product.Clean() {
				repo.Markers.Clean = &marker
			} else {
				repo.Markers.Attention = &marker
			}
			facts := Facts{Forge: repo.Forge, Owner: repo.Owner, Repo: repo.Repo, PR: "7"}
			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			var commands []string
			commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				commands = append(commands, name)
				return nil, nil
			}
			before := len(state.operationWrites())
			for _, phase := range []string{"parent", "retargeted"} {
				if phase == "retargeted" {
					state.mu.Lock()
					state.pullRequest["base"].(map[string]any)["ref"] = "main"
					state.baseTip = "parent-landed"
					state.mu.Unlock()
				}
				result, err := reconcilePullRequest(t.Context(), cfg, repo, facts)
				if err != nil {
					t.Fatal(err)
				}
				if result.Decision != ReconcileNothing || result.DeferralReason != "completed-marker" || result.TerminalOutcome != outcome.Name() || len(commands) != 0 || len(state.operationWrites()) != before {
					t.Fatalf("%s status-only reconciliation = %#v, commands %v, writes %d -> %d; want completed-marker without claim or marker removal", phase, result, commands, before, len(state.operationWrites()))
				}
			}
		})
	}
}

func TestGitHubCommitStatusContinuationPriority(t *testing.T) {
	for _, outcome := range []product.State{product.Incomplete(), product.Working(), product.Continuation()} {
		t.Run(outcome.Name(), func(t *testing.T) {
			state := newGitHubFixtureState(t)
			adapter := state.adapter(t)
			guard := state.guard()
			githubApplied(t, adapter.SetProductStatus(t.Context(), guard, product.Clean()))
			githubApplied(t, adapter.SetProductStatus(t.Context(), guard, outcome))
			cfg := ServiceConfig{}
			cfg.Service.BotLogin = githubFixtureBotLogin
			cfg.Service.StatusContext = "Minos"
			cfg.Forges = map[string]ForgeConfig{"github": {Adaptation: state.adaptationPath, APIBase: state.server.URL, CredentialFile: state.credentialPath}}
			facts := Facts{Forge: "github", Owner: "acme", Repo: "widgets", HeadSHA: guard.HeadSHA}
			priority, err := currentContinuationPriority(t.Context(), cfg, facts)
			if err != nil {
				t.Fatal(err)
			}
			if priority != 0 {
				t.Fatalf("%s commit-status continuation priority = %d, want 0", outcome.Name(), priority)
			}
		})
	}
}
