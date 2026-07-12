package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type conformanceOwnership struct{}

func (conformanceOwnership) Check(context.Context, Ownership) error { return nil }

type conformanceHarness struct {
	t           *testing.T
	apiBase     string
	token       string
	readerToken string
	forkerToken string
	owner       string
	repo        string
	adaptation  string
	clone       string
	sequence    int
}

func TestForgejoConformance(t *testing.T) {
	if os.Getenv("MINOS_FORGE_CONFORMANCE") != "1" {
		t.Skip("set MINOS_FORGE_CONFORMANCE=1 or run make forgejo-conformance")
	}

	h := newConformanceHarness(t)
	adapter := h.adapter(h.token, "Minos")

	t.Run("authoritative snapshot and owned writes", func(t *testing.T) {
		pr := h.createPullRequest("snapshot")
		snapshot := h.snapshot(adapter, pr)
		if snapshot.AuthenticatedUser != "Minos" || snapshot.Repository != h.owner+"/"+h.repo || snapshot.State != "open" {
			t.Fatalf("snapshot identity/state = %#v", snapshot)
		}
		if snapshot.HeadSHA == "" || snapshot.TargetSHA == "" || snapshot.HeadBranch == "" || snapshot.TargetBranch != "main" {
			t.Fatalf("snapshot omitted current revisions: %#v", snapshot)
		}

		guard := h.guard(pr, snapshot)
		if result := adapter.SetStatus(t.Context(), guard, StatusPending, "Reviewing changes"); result.Outcome != WriteApplied {
			t.Fatalf("SetStatus() = %#v, want applied", result)
		}
		if result := adapter.PostReview(t.Context(), guard, ReviewVerdictComment, "Conformance review", nil); result.Outcome != WriteApplied {
			t.Fatalf("PostReview() = %#v, want applied", result)
		}
		observed := h.snapshot(adapter, pr)
		if !hasOwnedStatus(observed.Statuses, StatusPending, "Reviewing changes") {
			t.Fatalf("snapshot did not discover owned status: %#v", observed.Statuses)
		}
		if !hasOwnedReview(observed.Reviews, snapshot.HeadSHA, "Conformance review") {
			t.Fatalf("snapshot did not discover owned review: %#v", observed.Reviews)
		}
	})

	t.Run("required check reduction and self exclusion", func(t *testing.T) {
		h.createMainProtection([]string{OwnedStatusContext, "build"}, true)
		pr := h.createPullRequest("required-checks")
		snapshot := h.snapshot(adapter, pr)
		h.postStatus(snapshot.HeadSHA, "build", "failure", "first attempt")
		h.postStatus(snapshot.HeadSHA, "build", "success", "newest attempt")
		if result := adapter.SetStatus(t.Context(), h.guard(pr, snapshot), StatusFailure, "Owned context is excluded"); result.Outcome != WriteApplied {
			t.Fatalf("SetStatus() = %#v, want applied", result)
		}
		observed := h.snapshot(adapter, pr)
		if observed.CheckDecision != ChecksPass {
			t.Fatalf("required check decision = %q, want pass; statuses=%#v requirements=%#v", observed.CheckDecision, observed.Statuses, observed.RequiredChecks)
		}
	})

	t.Run("uncertain write discovery", func(t *testing.T) {
		pr := h.createPullRequest("uncertain-write")
		snapshot := h.snapshot(adapter, pr)
		guard := h.guard(pr, snapshot)

		h.withFaultingCurl(t, "after-write", func() {
			if result := adapter.SetStatus(t.Context(), guard, StatusPending, "Response deliberately lost"); result.Outcome != WriteApplied {
				t.Fatalf("discover completed status = %#v, want applied", result)
			}
		})
		h.withFaultingCurl(t, "before-write", func() {
			if result := adapter.SetStatus(t.Context(), guard, StatusPending, "Write deliberately dropped"); result.Outcome != WriteUncertain {
				t.Fatalf("undiscoverable status = %#v, want uncertain", result)
			}
		})
	})

	t.Run("guarded push and stale head rejection", func(t *testing.T) {
		pr := h.createPullRequest("guarded-push")
		snapshot := h.snapshot(adapter, pr)
		h.checkout(snapshot.HeadBranch)
		changed := filepath.Join(h.clone, "guarded-push.txt")
		if err := os.WriteFile(changed, []byte("guarded push\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		message := filepath.Join(t.TempDir(), "message")
		if err := os.WriteFile(message, []byte("test: exercise guarded push\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		result := adapter.Push(t.Context(), h.guard(pr, snapshot), PushRequest{
			Branch: snapshot.HeadBranch, AuthorName: "Minos", AuthorEmail: "minos@example.invalid",
			Model: "conformance", MessageFile: message, Workspace: h.clone,
		})
		if result.Outcome != WriteApplied || result.SHA == "" || result.SHA == snapshot.HeadSHA {
			t.Fatalf("Push() = %#v, want a new applied SHA", result)
		}
		if stale := adapter.SetStatus(t.Context(), h.guard(pr, snapshot), StatusPending, "stale"); stale.Outcome != WriteRejected || !strings.Contains(stale.Reason, "head moved") {
			t.Fatalf("stale SetStatus() = %#v, want head-moved rejection", stale)
		}
	})

	t.Run("target movement rejection", func(t *testing.T) {
		pr := h.createPullRequest("target-movement")
		snapshot := h.snapshot(adapter, pr)
		h.advanceMain("target moved")
		if result := adapter.Merge(t.Context(), h.guard(pr, snapshot), MergeMethodSquash); result.Outcome != WriteRejected || !strings.Contains(result.Reason, "target moved") {
			t.Fatalf("Merge() after target movement = %#v, want target rejection", result)
		}
	})

	t.Run("configured merge and guarded branch deletion", func(t *testing.T) {
		h.configureSquashOnly()
		pr := h.createPullRequest("merge-delete")
		snapshot := h.waitForMergeable(adapter, pr)
		if len(snapshot.AllowedMergeMethods) != 1 || snapshot.AllowedMergeMethods[0] != MergeMethodSquash {
			t.Fatalf("allowed merge methods = %#v, want squash only", snapshot.AllowedMergeMethods)
		}
		snapshot = h.satisfyChecks(adapter, pr, snapshot)
		if result := adapter.Merge(t.Context(), h.guard(pr, snapshot), MergeMethodSquash); result.Outcome != WriteApplied {
			t.Fatalf("Merge() = %#v, want applied", result)
		}
		merged := h.snapshot(adapter, pr)
		if !merged.Merged || merged.State != "merged" {
			t.Fatalf("merged snapshot = %#v", merged)
		}
		if result := adapter.DeleteMergedBranch(t.Context(), h.guard(pr, snapshot)); result.Outcome != WriteApplied {
			t.Fatalf("DeleteMergedBranch() = %#v, want applied", result)
		}
		if h.remoteBranchSHA(snapshot.HeadBranch) != "" {
			t.Fatalf("source branch %q still exists", snapshot.HeadBranch)
		}
		if result := adapter.DeleteMergedBranch(t.Context(), h.guard(pr, snapshot)); result.Outcome != WriteApplied {
			t.Fatalf("idempotent DeleteMergedBranch() = %#v, want confirmed-absent applied", result)
		}
	})

	t.Run("advanced and protected branches are preserved", func(t *testing.T) {
		advancedPR := h.createPullRequest("advanced-branch")
		advanced := h.waitForMergeable(adapter, advancedPR)
		advanced = h.satisfyChecks(adapter, advancedPR, advanced)
		if result := adapter.Merge(t.Context(), h.guard(advancedPR, advanced), MergeMethodSquash); result.Outcome != WriteApplied {
			t.Fatalf("advanced branch Merge() = %#v", result)
		}
		h.advanceBranch(advanced.HeadBranch)
		if result := adapter.DeleteMergedBranch(t.Context(), h.guard(advancedPR, advanced)); result.Outcome != WriteRejected || !strings.Contains(result.Reason, "advanced") {
			t.Fatalf("advanced DeleteMergedBranch() = %#v", result)
		}
		if h.remoteBranchSHA(advanced.HeadBranch) == "" {
			t.Fatal("advanced branch was deleted")
		}

		protectedPR := h.createPullRequest("protected-branch")
		protected := h.waitForMergeable(adapter, protectedPR)
		protected = h.satisfyChecks(adapter, protectedPR, protected)
		if result := adapter.Merge(t.Context(), h.guard(protectedPR, protected), MergeMethodSquash); result.Outcome != WriteApplied {
			t.Fatalf("protected branch Merge() = %#v", result)
		}
		h.createProtection(protected.HeadBranch, nil, false)
		if result := adapter.DeleteMergedBranch(t.Context(), h.guard(protectedPR, protected)); result.Outcome != WriteRejected || !strings.Contains(result.Reason, "protected") {
			t.Fatalf("protected DeleteMergedBranch() = %#v", result)
		}

		defaultPR := h.createDefaultSourcePullRequest()
		defaultSource := h.waitForClearance(adapter, defaultPR)
		if result := adapter.Merge(t.Context(), h.guard(defaultPR, defaultSource), MergeMethodSquash); result.Outcome != WriteApplied {
			t.Fatalf("default-source Merge() = %#v", result)
		}
		if result := adapter.DeleteMergedBranch(t.Context(), h.guard(defaultPR, defaultSource)); result.Outcome != WriteRejected || !strings.Contains(result.Reason, "default") {
			t.Fatalf("default-source DeleteMergedBranch() = %#v", result)
		}
		if h.remoteBranchSHA("main") == "" {
			t.Fatal("default branch was deleted")
		}

		forkPR := h.createForkPullRequest()
		fork := h.waitForMergeable(adapter, forkPR)
		fork = h.satisfyChecks(adapter, forkPR, fork)
		if result := adapter.Merge(t.Context(), h.guard(forkPR, fork), MergeMethodSquash); result.Outcome != WriteApplied {
			t.Fatalf("fork Merge() = %#v", result)
		}
		if result := adapter.DeleteMergedBranch(t.Context(), h.guard(forkPR, fork)); result.Outcome != WriteRejected || !strings.Contains(result.Reason, "fork") {
			t.Fatalf("fork DeleteMergedBranch() = %#v", result)
		}
	})

	t.Run("permissions", func(t *testing.T) {
		pr := h.createPullRequest("permission")
		reader := h.adapter(h.readerToken, "reader")
		snapshot := h.snapshot(reader, pr)
		if snapshot.CanMerge {
			t.Fatal("read-only identity unexpectedly reports merge permission")
		}
		if result := reader.Merge(t.Context(), h.guard(pr, snapshot), MergeMethodSquash); result.Outcome != WriteRejected || !strings.Contains(result.Reason, "cannot merge") {
			t.Fatalf("read-only Merge() = %#v, want permission rejection", result)
		}
	})
}

func newConformanceHarness(t *testing.T) *conformanceHarness {
	t.Helper()
	h := &conformanceHarness{
		t: t, apiBase: mustEnv(t, "MINOS_CONFORMANCE_API_BASE"), token: mustEnv(t, "MINOS_CONFORMANCE_TOKEN"),
		readerToken: mustEnv(t, "MINOS_CONFORMANCE_READER_TOKEN"), owner: mustEnv(t, "MINOS_CONFORMANCE_OWNER"),
		forkerToken: mustEnv(t, "MINOS_CONFORMANCE_FORKER_TOKEN"),
		repo:        mustEnv(t, "MINOS_CONFORMANCE_REPO"), adaptation: mustEnv(t, "MINOS_CONFORMANCE_ADAPTATION"),
		clone: filepath.Join(t.TempDir(), "subject"),
	}
	cloneBase, err := url.Parse(h.apiBase)
	if err != nil {
		t.Fatal(err)
	}
	cloneBase.User = url.UserPassword("x-access-token", h.token)
	cloneBase.Path = "/" + h.owner + "/" + h.repo + ".git"
	h.git("clone", cloneBase.String(), h.clone)
	h.gitInClone("config", "user.name", "Minos Conformance")
	h.gitInClone("config", "user.email", "minos@example.invalid")
	return h
}

func (h *conformanceHarness) adapter(token, login string) *Adapter {
	h.t.Helper()
	adapter, err := NewAdapter(ScriptRunner{Directory: h.adaptation, APIBase: h.apiBase, Credential: token}, conformanceOwnership{}, login)
	if err != nil {
		h.t.Fatal(err)
	}
	return adapter
}

func (h *conformanceHarness) createPullRequest(prefix string) int64 {
	h.t.Helper()
	h.sequence++
	branch := fmt.Sprintf("%s-%d", prefix, h.sequence)
	h.gitInClone("checkout", "main")
	h.gitInClone("pull", "--ff-only", "origin", "main")
	h.gitInClone("checkout", "-b", branch)
	path := filepath.Join(h.clone, branch+".txt")
	if err := os.WriteFile(path, []byte(branch+"\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	h.gitInClone("add", filepath.Base(path))
	h.gitInClone("commit", "-m", "add "+branch)
	h.gitInClone("push", "origin", branch)
	var response struct {
		Number int64 `json:"number"`
	}
	h.api(h.token, http.MethodPost, fmt.Sprintf("/api/v1/repos/%s/%s/pulls", h.owner, h.repo), map[string]any{"head": branch, "base": "main", "title": branch}, &response)
	return response.Number
}

func (h *conformanceHarness) createDefaultSourcePullRequest() int64 {
	h.t.Helper()
	h.sequence++
	target := fmt.Sprintf("default-target-%d", h.sequence)
	h.gitInClone("checkout", "main")
	h.gitInClone("pull", "--ff-only", "origin", "main")
	h.gitInClone("checkout", "-b", target)
	h.gitInClone("push", "origin", target)
	h.gitInClone("checkout", "main")
	path := filepath.Join(h.clone, fmt.Sprintf("default-source-%d.txt", h.sequence))
	if err := os.WriteFile(path, []byte("default source\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	h.gitInClone("add", filepath.Base(path))
	h.gitInClone("commit", "-m", "advance default source")
	h.gitInClone("push", "origin", "main")
	var response struct {
		Number int64 `json:"number"`
	}
	h.api(h.token, http.MethodPost, fmt.Sprintf("/api/v1/repos/%s/%s/pulls", h.owner, h.repo), map[string]any{"head": "main", "base": target, "title": target}, &response)
	return response.Number
}

func (h *conformanceHarness) createForkPullRequest() int64 {
	h.t.Helper()
	h.sequence++
	branch := fmt.Sprintf("fork-source-%d", h.sequence)
	var fork struct {
		FullName string `json:"full_name"`
	}
	h.api(h.forkerToken, http.MethodPost, fmt.Sprintf("/api/v1/repos/%s/%s/forks", h.owner, h.repo), map[string]any{}, &fork)
	if fork.FullName != "forker/"+h.repo {
		h.t.Fatalf("fork full name = %q", fork.FullName)
	}
	forkClone := filepath.Join(h.t.TempDir(), "fork")
	forkURL, err := url.Parse(h.apiBase)
	if err != nil {
		h.t.Fatal(err)
	}
	forkURL.User = url.UserPassword("x-access-token", h.forkerToken)
	forkURL.Path = "/forker/" + h.repo + ".git"
	h.git("clone", forkURL.String(), forkClone)
	h.git("-C", forkClone, "config", "user.name", "Fork Conformance")
	h.git("-C", forkClone, "config", "user.email", "forker@example.invalid")
	h.git("-C", forkClone, "checkout", "-b", branch)
	path := filepath.Join(forkClone, branch+".txt")
	if err := os.WriteFile(path, []byte("fork source\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	h.git("-C", forkClone, "add", filepath.Base(path))
	h.git("-C", forkClone, "commit", "-m", "add fork source")
	h.git("-C", forkClone, "push", "origin", branch)
	var response struct {
		Number int64 `json:"number"`
	}
	h.api(h.forkerToken, http.MethodPost, fmt.Sprintf("/api/v1/repos/%s/%s/pulls", h.owner, h.repo), map[string]any{"head": "forker:" + branch, "base": "main", "title": branch}, &response)
	return response.Number
}

func (h *conformanceHarness) snapshot(adapter *Adapter, pr int64) Snapshot {
	h.t.Helper()
	snapshot, err := adapter.Snapshot(h.t.Context(), Repository{Owner: h.owner, Name: h.repo}, pr)
	if err != nil {
		h.t.Fatalf("Snapshot(%d) error = %v", pr, err)
	}
	return snapshot
}

func (h *conformanceHarness) waitForMergeable(adapter *Adapter, pr int64) Snapshot {
	h.t.Helper()
	for range 60 {
		snapshot := h.snapshot(adapter, pr)
		if snapshot.Mergeable {
			return snapshot
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("pull request %d did not become mergeable", pr)
	return Snapshot{}
}

func (h *conformanceHarness) guard(pr int64, snapshot Snapshot) Guard {
	return Guard{Ownership: Ownership{Attempt: "conformance"}, Repository: Repository{Owner: h.owner, Name: h.repo}, PullRequest: pr, HeadSHA: snapshot.HeadSHA, TargetSHA: snapshot.TargetSHA}
}

func (h *conformanceHarness) postStatus(sha, context, state, description string) {
	h.t.Helper()
	h.api(h.token, http.MethodPost, fmt.Sprintf("/api/v1/repos/%s/%s/statuses/%s", h.owner, h.repo, sha), map[string]any{"context": context, "state": state, "description": description}, nil)
}

func (h *conformanceHarness) createMainProtection(contexts []string, blockOutdated bool) {
	h.t.Helper()
	h.createProtection("main", contexts, blockOutdated)
}

func (h *conformanceHarness) createProtection(branch string, contexts []string, blockOutdated bool) {
	h.t.Helper()
	payload := map[string]any{"rule_name": branch, "enable_push": true, "enable_status_check": len(contexts) > 0, "status_check_contexts": contexts, "block_on_outdated_branch": blockOutdated, "apply_to_admins": true}
	h.api(h.token, http.MethodPost, fmt.Sprintf("/api/v1/repos/%s/%s/branch_protections", h.owner, h.repo), payload, nil)
}

func (h *conformanceHarness) configureSquashOnly() {
	h.t.Helper()
	h.api(h.token, http.MethodPatch, fmt.Sprintf("/api/v1/repos/%s/%s", h.owner, h.repo), map[string]any{
		"allow_merge_commits": false, "allow_rebase": false, "allow_rebase_explicit": false,
		"allow_squash_merge": true, "allow_fast_forward_only_merge": false, "default_merge_style": "squash",
	}, nil)
}

func (h *conformanceHarness) satisfyChecks(adapter *Adapter, pr int64, snapshot Snapshot) Snapshot {
	h.t.Helper()
	h.postStatus(snapshot.HeadSHA, "build", "success", "Conformance build")
	if result := adapter.SetStatus(h.t.Context(), h.guard(pr, snapshot), StatusSuccess, "Changes approved"); result.Outcome != WriteApplied {
		h.t.Fatalf("satisfy Minos check = %#v", result)
	}
	// Final clearance is a fresh forge decision after the status writes, not the
	// pre-status mergeability snapshot. Require three stable reads of the same
	// head/target pair to model the agent-paced lifecycle and let Forgejo finish
	// its asynchronous mergeability recomputation.
	return h.waitForClearance(adapter, pr)
}

func (h *conformanceHarness) waitForClearance(adapter *Adapter, pr int64) Snapshot {
	h.t.Helper()
	var previous Snapshot
	stable := 0
	for range 120 {
		snapshot := h.snapshot(adapter, pr)
		if snapshot.Mergeable && snapshot.CheckDecision == ChecksPass &&
			snapshot.HeadSHA == previous.HeadSHA && snapshot.TargetSHA == previous.TargetSHA {
			stable++
		} else if snapshot.Mergeable && snapshot.CheckDecision == ChecksPass {
			stable = 1
		} else {
			stable = 0
		}
		if stable >= 3 {
			return snapshot
		}
		previous = snapshot
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("pull request %d did not reach stable final clearance", pr)
	return Snapshot{}
}

func (h *conformanceHarness) advanceMain(message string) {
	h.t.Helper()
	h.gitInClone("checkout", "main")
	h.gitInClone("pull", "--ff-only", "origin", "main")
	path := filepath.Join(h.clone, fmt.Sprintf("main-%d.txt", h.sequence))
	if err := os.WriteFile(path, []byte(message+"\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	h.gitInClone("add", filepath.Base(path))
	h.gitInClone("commit", "-m", message)
	h.gitInClone("push", "origin", "main")
}

func (h *conformanceHarness) advanceBranch(branch string) {
	h.t.Helper()
	h.gitInClone("checkout", branch)
	path := filepath.Join(h.clone, branch+"-advanced.txt")
	if err := os.WriteFile(path, []byte("advanced\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	h.gitInClone("add", filepath.Base(path))
	h.gitInClone("commit", "-m", "advance "+branch)
	h.gitInClone("push", "origin", branch)
}

func (h *conformanceHarness) checkout(branch string) {
	h.t.Helper()
	h.gitInClone("checkout", branch)
}

func (h *conformanceHarness) remoteBranchSHA(branch string) string {
	h.t.Helper()
	out := h.gitInClone("ls-remote", "origin", "refs/heads/"+branch)
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (h *conformanceHarness) withFaultingCurl(t *testing.T, mode string, fn func()) {
	t.Helper()
	directory := t.TempDir()
	realCurl, err := exec.LookPath("curl")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(directory, "curl")
	content := `#!/usr/bin/env sh
set -eu
case " $* " in
  *" -X POST "*"/statuses/"*)
    if [ "$MINOS_CONFORMANCE_CURL_FAULT" = "after-write" ]; then
      "$REAL_CURL" "$@" >/dev/null
    fi
    exit 56
    ;;
esac
exec "$REAL_CURL" "$@"
`
	if err := os.WriteFile(wrapper, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	oldRealCurl, hadRealCurl := os.LookupEnv("REAL_CURL")
	oldFault, hadFault := os.LookupEnv("MINOS_CONFORMANCE_CURL_FAULT")
	defer func() {
		_ = os.Setenv("PATH", oldPath)
		restoreEnvironment("REAL_CURL", oldRealCurl, hadRealCurl)
		restoreEnvironment("MINOS_CONFORMANCE_CURL_FAULT", oldFault, hadFault)
	}()
	if err := os.Setenv("REAL_CURL", realCurl); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("MINOS_CONFORMANCE_CURL_FAULT", mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("PATH", directory+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	fn()
}

func restoreEnvironment(name, value string, existed bool) {
	if existed {
		_ = os.Setenv(name, value)
		return
	}
	_ = os.Unsetenv(name)
}

func (h *conformanceHarness) api(token, method, path string, payload any, output any) {
	h.t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			h.t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(h.t.Context(), method, h.apiBase+path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Authorization", "token "+token)
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		h.t.Fatalf("%s %s returned %s: %s", method, path, response.Status, data)
	}
	if output != nil && len(data) > 0 {
		if err := json.Unmarshal(data, output); err != nil {
			h.t.Fatalf("decode %s %s: %v: %s", method, path, err, data)
		}
	}
}

func (h *conformanceHarness) git(arguments ...string) string {
	h.t.Helper()
	cmd := exec.CommandContext(h.t.Context(), "git", arguments...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (h *conformanceHarness) gitInClone(arguments ...string) string {
	h.t.Helper()
	return h.git(append([]string{"-C", h.clone}, arguments...)...)
}

func hasOwnedStatus(statuses []Status, state StatusState, description string) bool {
	for _, status := range statuses {
		if status.Context == OwnedStatusContext && status.Creator == "Minos" && status.State == state && status.Description == description {
			return true
		}
	}
	return false
}

func hasOwnedReview(reviews []Review, head, body string) bool {
	for _, review := range reviews {
		if review.User == "Minos" && review.CommitID == head && review.Body == body {
			return true
		}
	}
	return false
}

func mustEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}
