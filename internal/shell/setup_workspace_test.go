package shell

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestSetupWorkspaceClonesHeadCapturesGroundingAndInstallsProtection(t *testing.T) {
	repository, head := createGitRepository(t, "code.txt", "reviewed code\n")
	secondary := createGitRepositoryAtHead(t, "README.md", "# Commission\n")
	server := newSetupForge(t, head, repository, secondary)

	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	orientation := filepath.Join(runDir, "orientation.json")
	runSetupWorkspaceWithGuidance(t, server.URL, runDir, workspace, orientation, head, secondaryGuidance)

	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != head {
		t.Fatalf("workspace head = %q, want %q", got, head)
	}
	commonDir := gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
	protectedRef, err := os.ReadFile(filepath.Join(commonDir, "minos-protected-ref"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(protectedRef)); got != "refs/heads/feature" {
		t.Fatalf("protected ref = %q, want pull-request branch", got)
	}
	clonePath := filepath.Join(runDir, "guidance", "owner", "repository-plans")
	assertContainsFile(t, filepath.Join(clonePath, "README.md"), "# Commission")
	// The secondary clone is read-only: no push from a run can reach it.
	if output, err := exec.Command("git", "-C", clonePath, "push", "origin", "HEAD:refs/heads/from-a-run").CombinedOutput(); err == nil {
		t.Fatalf("guidance clone accepted a push: %s", output)
	}

	state := readOrientation(t, orientation)
	want := []orientationGuidance{{
		Source:   guidanceSourceRecord{Repository: "owner/repository-plans", Path: "README.md"},
		Location: filepath.Join(clonePath, "README.md"), Origin: "configured",
	}}
	if !reflect.DeepEqual(state.Guidance, want) || len(state.Misconfigurations) != 0 {
		t.Fatalf("orientation guidance = %+v, misconfigurations = %+v; want the configured secondary source alone", state.Guidance, state.Misconfigurations)
	}
	pullRequestRecord := filepath.Join(runDir, "pull-request.json")
	if state.PullRequest != pullRequestRecord {
		t.Fatalf("orientation pull-request record = %q, want %q", state.PullRequest, pullRequestRecord)
	}
	if state.Source.Owner != "owner" || state.Source.Repo != "repository" || state.Source.PR != "17" {
		t.Fatalf("orientation source = %+v, want owner/repository#17", state.Source)
	}
	if matched, err := regexp.MatchString(`^\d{4}-\d{2}-\d{2}$`, state.Source.Date); err != nil || !matched {
		t.Fatalf("orientation source date = %q, want ISO date", state.Source.Date)
	}
	assertContainsFile(t, pullRequestRecord, `"title": "Fixture change"`)
	assertContainsFile(t, pullRequestRecord, `"body": "Not in scope: a concrete transport."`)

}

func TestSetupWorkspaceWarmResumeReestablishesSafetyState(t *testing.T) {
	repository, head := createGitRepository(t, "code.txt", "reviewed code\n")
	secondary := createGitRepositoryAtHead(t, "README.md", "# Commission\n")
	server := newSetupForge(t, head, repository, secondary)
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	orientation := filepath.Join(runDir, "orientation.json")
	runSetupWorkspaceWithGuidance(t, server.URL, runDir, workspace, orientation, head, secondaryGuidance)
	clonePath := filepath.Join(runDir, "guidance", "owner", "repository-plans")
	if err := os.WriteFile(filepath.Join(clonePath, "resumed-marker"), []byte("kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(workspace, "code.txt"), []byte("unfinished tracked change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commonDir := gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err := os.WriteFile(orientation, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "minos-protected-ref"), []byte("refs/heads/stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := setupWorkspaceCommand(t, server.URL, runDir, workspace, orientation, head)
	cmd.Env = append(cmd.Env, "MINOS_RESUME=true", "MINOS_GUIDANCE_SOURCES="+secondaryGuidance)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("warm setup-workspace failed: %v\n%s", err, output)
	}

	assertContainsFile(t, filepath.Join(workspace, "code.txt"), "reviewed code")
	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != head {
		t.Fatalf("resumed workspace head = %q, want %q", got, head)
	}
	state := readOrientation(t, orientation)
	if state.Head != head || len(state.Guidance) != 1 || state.Guidance[0].Location != filepath.Join(clonePath, "README.md") {
		t.Fatalf("rewritten orientation = %+v", state)
	}
	assertContainsFile(t, filepath.Join(clonePath, "resumed-marker"), "kept")
	protectedRefs, err := os.ReadFile(filepath.Join(commonDir, "minos-protected-ref"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(protectedRefs), "refs/heads/feature\n"; got != want {
		t.Fatalf("protected refs after resume = %q, want %q", got, want)
	}
	assertContainsFile(t, filepath.Join(commonDir, "hooks", "pre-push"), "minos-protected-ref")
}

func TestSetupWorkspaceResumeReclonesInvalidGitWorkspaceAndReestablishesSafetyState(t *testing.T) {
	repository, head := createGitRepository(t, "AGENTS.md", "reviewed code guidance\n")
	server := newSetupForge(t, head, repository, "")
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	orientation := filepath.Join(runDir, "orientation.json")
	runSetupWorkspace(t, server.URL, runDir, workspace, orientation, head)
	if err := os.RemoveAll(filepath.Join(workspace, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "abandoned.txt"), []byte("not a repository\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := setupWorkspaceCommand(t, server.URL, runDir, workspace, orientation, head)
	cmd.Env = append(cmd.Env, "MINOS_RESUME=true")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("resume with invalid workspace failed: %v\n%s", err, output)
	}

	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != head {
		t.Fatalf("re-cloned workspace head = %q, want %q", got, head)
	}
	if _, err := os.Stat(filepath.Join(workspace, "abandoned.txt")); !os.IsNotExist(err) {
		t.Fatalf("invalid workspace residue remains or stat failed: %v", err)
	}
	commonDir := gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
	protectedRef, err := os.ReadFile(filepath.Join(commonDir, "minos-protected-ref"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(protectedRef)); got != "refs/heads/feature" {
		t.Fatalf("protected ref = %q, want pull-request branch", got)
	}
	assertContainsFile(t, filepath.Join(commonDir, "hooks", "pre-push"), "minos-protected-ref")
	if state := readOrientation(t, orientation); state.Head != head || len(state.Guidance) != 1 || state.Guidance[0].Origin != "checked-in" {
		t.Fatalf("orientation after fresh clone = %+v", state)
	}
}

func TestSetupWorkspaceRefusesMovedHead(t *testing.T) {
	repository, admitted := createGitRepository(t, "code.txt", "admitted\n")
	if err := os.WriteFile(filepath.Join(repository, "code.txt"), []byte("moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "code.txt")
	runGit(t, repository, "commit", "-m", "move head")
	observed := gitOutput(t, repository, "rev-parse", "HEAD")
	server := newSetupForge(t, observed, repository, "")
	runDir := t.TempDir()
	cmd := setupWorkspaceCommand(t, server.URL, runDir, filepath.Join(runDir, "workspace"), filepath.Join(runDir, "orientation.json"), admitted)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "pull-request head moved from") {
		t.Fatalf("error = %v, output = %q", err, output)
	}
}

func TestSetupWorkspaceGroundsOnCheckedInGuidanceWhenNoSourceIsConfigured(t *testing.T) {
	repository, head := createGitRepository(t, "AGENTS.md", "MINOS_REPOSITORY_GUIDANCE_OCHRE_719\n")
	server := newSetupForge(t, head, repository, "")

	runDir := t.TempDir()
	orientation := filepath.Join(runDir, "orientation.json")
	runSetupWorkspace(t, server.URL, runDir, filepath.Join(runDir, "workspace"), orientation, head)

	state := readOrientation(t, orientation)
	want := []orientationGuidance{{
		Source: guidanceSourceRecord{Path: "AGENTS.md"}, Location: filepath.Join(runDir, "workspace", "AGENTS.md"), Origin: "checked-in",
	}}
	if !reflect.DeepEqual(state.Guidance, want) {
		t.Fatalf("orientation guidance = %+v, want the repository's own AGENTS.md as checked-in guidance", state.Guidance)
	}
	assertContainsFile(t, state.Guidance[0].Location, "MINOS_REPOSITORY_GUIDANCE_OCHRE_719")
	if _, err := os.Stat(filepath.Join(runDir, "guidance")); !os.IsNotExist(err) {
		t.Fatalf("guidance clone directory exists or stat failed unexpectedly: %v", err)
	}
}

func TestSetupWorkspaceReadsConfiguredSourcesInOrderFromBothRepositories(t *testing.T) {
	repository, head := createGitRepository(t, "docs/intent.md", "MINOS_REPOSITORY_INTENT_UMBER_719\n")
	secondary := createGitRepositoryAtHead(t, "README.md", "MINOS_SECONDARY_COMMISSION_UMBER_719\n")
	server := newSetupForge(t, head, repository, secondary)

	runDir := t.TempDir()
	orientation := filepath.Join(runDir, "orientation.json")
	runSetupWorkspaceWithGuidance(t, server.URL, runDir, filepath.Join(runDir, "workspace"), orientation, head,
		`[{"path":"docs/intent.md"},{"repository":"owner/repository-plans","path":"README.md"}]`)

	state := readOrientation(t, orientation)
	if len(state.Guidance) != 2 ||
		state.Guidance[0].Source != (guidanceSourceRecord{Path: "docs/intent.md"}) ||
		state.Guidance[1].Source != (guidanceSourceRecord{Repository: "owner/repository-plans", Path: "README.md"}) {
		t.Fatalf("orientation guidance = %+v, want the two configured sources in configured order", state.Guidance)
	}
	assertContainsFile(t, state.Guidance[0].Location, "MINOS_REPOSITORY_INTENT_UMBER_719")
	assertContainsFile(t, state.Guidance[1].Location, "MINOS_SECONDARY_COMMISSION_UMBER_719")
}

func TestSetupWorkspaceRecordsAnUnreadableConfiguredSourceAsMisconfiguration(t *testing.T) {
	for _, test := range []struct {
		name      string
		secondary func(t *testing.T) string
		reason    string
	}{
		{name: "secondary repository not on the forge", secondary: func(*testing.T) string { return "" }, reason: "repository lookup returned HTTP 404"},
		{name: "named file missing", secondary: func(t *testing.T) string { return createGitRepositoryAtHead(t, "NOTES.md", "# Notes\n") }, reason: "is missing or empty"},
		{name: "named file blank", secondary: func(t *testing.T) string { return createGitRepositoryAtHead(t, "README.md", " \n\t") }, reason: "is missing or empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, head := createGitRepository(t, "AGENTS.md", "MINOS_REPOSITORY_GUIDANCE_SLATE_719\n")
			server := newSetupForge(t, head, repository, test.secondary(t))
			runDir := t.TempDir()
			orientation := filepath.Join(runDir, "orientation.json")
			runSetupWorkspaceWithGuidance(t, server.URL, runDir, filepath.Join(runDir, "workspace"), orientation, head,
				`[{"repository":"owner/repository-plans","path":"README.md"},{"path":"AGENTS.md"}]`)

			state := readOrientation(t, orientation)
			if len(state.Guidance) != 1 || state.Guidance[0].Source != (guidanceSourceRecord{Path: "AGENTS.md"}) {
				t.Fatalf("orientation guidance = %+v, want the readable source alone", state.Guidance)
			}
			want := []orientationMisconfiguration{{
				Kind: "guidance-source", Source: guidanceSourceRecord{Repository: "owner/repository-plans", Path: "README.md"}, Reason: test.reason,
			}}
			if !reflect.DeepEqual(state.Misconfigurations, want) {
				t.Fatalf("orientation misconfigurations = %+v, want %+v", state.Misconfigurations, want)
			}
		})
	}
}

func TestSetupWorkspaceRefusesWhenNoConfiguredSourceIsReadable(t *testing.T) {
	repository, head := createGitRepository(t, "AGENTS.md", "unconfigured guidance\n")
	server := newSetupForge(t, head, repository, "")
	runDir := t.TempDir()
	cmd := setupWorkspaceCommand(t, server.URL, runDir, filepath.Join(runDir, "workspace"), filepath.Join(runDir, "orientation.json"), head)
	cmd.Env = append(cmd.Env, `MINOS_GUIDANCE_SOURCES=[{"path":"docs/absent.md"}]`)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "no configured guidance source is readable") {
		t.Fatalf("setup-workspace error = %v, output = %q", err, output)
	}
}

func TestSetupWorkspaceRejectsMalformedGuidanceSources(t *testing.T) {
	repository, head := createGitRepository(t, "AGENTS.md", "guidance\n")
	server := newSetupForge(t, head, repository, "")
	for _, sources := range []string{`not json`, `{"path":"x"}`, `[{"path":""}]`, `[{"path":"/etc/x"}]`, `[{"path":"../x"}]`, `[{"repository":"http://x/y","path":"a"}]`} {
		t.Run(sources, func(t *testing.T) {
			runDir := t.TempDir()
			cmd := setupWorkspaceCommand(t, server.URL, runDir, filepath.Join(runDir, "workspace"), filepath.Join(runDir, "orientation.json"), head)
			cmd.Env = append(cmd.Env, "MINOS_GUIDANCE_SOURCES="+sources)
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "MINOS_GUIDANCE_SOURCES must be a JSON array") {
				t.Fatalf("setup-workspace error = %v, output = %q", err, output)
			}
		})
	}
}

func TestSetupWorkspaceSkipsBlankRepositoryGuidanceFallback(t *testing.T) {
	repository, _ := createGitRepository(t, "AGENTS.md", " \n\t")
	if err := os.WriteFile(filepath.Join(repository, "CLAUDE.md"), []byte("MINOS_REPOSITORY_GUIDANCE_SIENNA_719\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "CLAUDE.md")
	runGit(t, repository, "commit", "-m", "fixture guidance")
	head := gitOutput(t, repository, "rev-parse", "HEAD")
	server := newSetupForge(t, head, repository, "")

	runDir := t.TempDir()
	orientation := filepath.Join(runDir, "orientation.json")
	runSetupWorkspace(t, server.URL, runDir, filepath.Join(runDir, "workspace"), orientation, head)

	state := readOrientation(t, orientation)
	if len(state.Guidance) != 1 || state.Guidance[0].Location != filepath.Join(runDir, "workspace", "CLAUDE.md") {
		t.Fatalf("orientation guidance = %+v, want non-empty repository CLAUDE.md", state.Guidance)
	}
	assertContainsFile(t, state.Guidance[0].Location, "MINOS_REPOSITORY_GUIDANCE_SIENNA_719")
}

func TestSetupWorkspaceRejectsEmptyRepositoryGuidanceFallback(t *testing.T) {
	repository, head := createGitRepository(t, "AGENTS.md", " \n\t")
	server := newSetupForge(t, head, repository, "")
	runDir := t.TempDir()
	cmd := setupWorkspaceCommand(t, server.URL, runDir, filepath.Join(runDir, "workspace"), filepath.Join(runDir, "orientation.json"), head)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "no guidance source is configured and the repository has no non-empty checked-in guidance") {
		t.Fatalf("setup-workspace error = %v, output = %q", err, output)
	}
}

func TestSetupWorkspaceLeavesOnlyCloneAndGuardState(t *testing.T) {
	repository, head := createGitRepository(t, "AGENTS.md", "MINOS_REPOSITORY_GUIDANCE_COBALT_719\n")
	server := newSetupForge(t, head, repository, "")
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	runSetupWorkspace(t, server.URL, runDir, workspace, filepath.Join(runDir, "orientation.json"), head)

	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != head {
		t.Fatalf("workspace head after clone-only setup = %q, want %q", got, head)
	}
	if output, err := exec.Command("git", "-C", workspace, "show-ref", "--verify", "--quiet", "refs/minos/target").CombinedOutput(); err == nil {
		t.Fatalf("setup fetched a target ref: %s", output)
	}
	for _, path := range []string{
		filepath.Join(runDir, "reconciliation.json"),
		filepath.Join(workspace, "reconciliation.json"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("setup left reconciliation state at %s: %v", path, err)
		}
	}
	for _, key := range []string{"user.name", "user.email", "http.extraHeader"} {
		if output, err := exec.Command("git", "-C", workspace, "config", "--local", "--get", key).CombinedOutput(); err == nil {
			t.Fatalf("setup retained %s plumbing: %q", key, output)
		}
	}
}

func TestSetupWorkspaceFetchesForkTargetObjectWithoutChangingWorkspaceState(t *testing.T) {
	targetRepository, target := createGitRepository(t, "AGENTS.md", "target guidance\n")
	headRepository, head := createGitRepository(t, "AGENTS.md", "head guidance\n")
	server := newSetupForgeForTarget(t, head, headRepository, target, targetRepository, "")

	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	runGit(t, filepath.Dir(workspace), "clone", "--no-checkout", headRepository, workspace)
	runGit(t, workspace, "checkout", "--detach", head)
	if output, err := exec.Command("git", "-C", workspace, "cat-file", "-e", target+"^{commit}").CombinedOutput(); err == nil {
		t.Fatalf("head clone unexpectedly contains target object %s", output)
	}
	expectedHead := gitOutput(t, workspace, "rev-parse", "HEAD")
	expectedRefs := gitOutput(t, workspace, "for-each-ref", "--format=%(refname) %(objectname)")
	expectedStatus := gitOutput(t, workspace, "status", "--porcelain")
	if err := os.RemoveAll(workspace); err != nil {
		t.Fatal(err)
	}

	runSetupWorkspaceWithTarget(t, server.URL, runDir, workspace, filepath.Join(runDir, "orientation.json"), head, target)

	if got := gitOutput(t, workspace, "cat-file", "-e", target+"^{commit}"); got != "" {
		t.Fatalf("target object lookup output = %q, want empty", got)
	}
	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != expectedHead {
		t.Fatalf("workspace head = %q, want unchanged %q", got, expectedHead)
	}
	if got := gitOutput(t, workspace, "for-each-ref", "--format=%(refname) %(objectname)"); got != expectedRefs {
		t.Fatalf("workspace refs = %q, want unchanged %q", got, expectedRefs)
	}
	if got := gitOutput(t, workspace, "status", "--porcelain"); got != expectedStatus {
		t.Fatalf("workspace status = %q, want unchanged %q", got, expectedStatus)
	}
	if output, err := exec.Command("git", "-C", workspace, "show-ref", "--verify", "--quiet", "refs/minos/target").CombinedOutput(); err == nil {
		t.Fatalf("setup created target ref: %s", output)
	}
	commonDir := gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if _, err := os.Stat(filepath.Join(commonDir, "FETCH_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("setup recorded target fetch state: %v", err)
	}
	if output := gitOutput(t, workspace, "fsck", "--unreachable", "--no-reflogs"); !strings.Contains(output, "unreachable commit "+target) {
		t.Fatalf("target object is not unreferenced: %q", output)
	}
}

type guidanceSourceRecord struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
}

type orientationGuidance struct {
	Source   guidanceSourceRecord `json:"source"`
	Location string               `json:"location"`
	Origin   string               `json:"origin"`
}

type orientationMisconfiguration struct {
	Kind   string               `json:"kind"`
	Source guidanceSourceRecord `json:"source"`
	Reason string               `json:"reason"`
}

type orientationState struct {
	Repository        string                        `json:"repository"`
	Head              string                        `json:"head"`
	Guidance          []orientationGuidance         `json:"guidance"`
	Misconfigurations []orientationMisconfiguration `json:"misconfigurations"`
	PullRequest       string                        `json:"pullRequest"`
	Source            struct {
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
		PR    string `json:"pr"`
		Date  string `json:"date"`
	} `json:"source"`
}

func readOrientation(t *testing.T, path string) orientationState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state orientationState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// newSetupForge serves the fixture pull request and, when secondary is a
// clone URL, the secondary repository owner/repository-plans that configured
// guidance sources may name.
func newSetupForge(t *testing.T, head, repository, secondary string) *httptest.Server {
	t.Helper()
	return newSetupForgeForTarget(t, head, repository, head, repository, secondary)
}

func newSetupForgeForTarget(t *testing.T, head, repository, target, targetRepository, secondary string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token forge-token" {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/repos/owner/repository/pulls/17":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"number":17,
				"title":"Fixture change",
				"body":"Not in scope: a concrete transport.",
				"head":{"sha":%q,"ref":"feature","repo":{"clone_url":%q,"full_name":"owner/repository"}},
				"base":{"sha":%q,"ref":"main","repo":{"clone_url":%q,"full_name":"owner/repository"}}
			}`, head, repository, target, targetRepository)
		case "/api/v1/repos/owner/repository-plans":
			if secondary == "" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"clone_url":%q}`, secondary)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func runSetupWorkspace(t *testing.T, apiBase, runDir, workspace, orientation, head string) {
	t.Helper()
	runSetupWorkspaceWithTarget(t, apiBase, runDir, workspace, orientation, head, head)
}

// secondaryGuidance is the configured source most tests use: README.md in
// the secondary repository the fixture forge serves.
const secondaryGuidance = `[{"repository":"owner/repository-plans","path":"README.md"}]`

func runSetupWorkspaceWithGuidance(t *testing.T, apiBase, runDir, workspace, orientation, head, sources string) {
	t.Helper()
	cmd := setupWorkspaceCommand(t, apiBase, runDir, workspace, orientation, head)
	cmd.Env = append(cmd.Env, "MINOS_GUIDANCE_SOURCES="+sources)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup-workspace failed: %v\n%s", err, output)
	}
}

func runSetupWorkspaceWithTarget(t *testing.T, apiBase, runDir, workspace, orientation, head, target string) {
	t.Helper()
	cmd := setupWorkspaceCommandWithTarget(t, apiBase, runDir, workspace, orientation, head, target)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup-workspace failed: %v\n%s", err, output)
	}
}

func setupWorkspaceCommand(t *testing.T, apiBase, runDir, workspace, orientation, head string) *exec.Cmd {
	t.Helper()
	return setupWorkspaceCommandWithTarget(t, apiBase, runDir, workspace, orientation, head, head)
}

func setupWorkspaceCommandWithTarget(t *testing.T, apiBase, runDir, workspace, orientation, head, target string) *exec.Cmd {
	t.Helper()
	credential := filepath.Join(runDir, "forge.token")
	if err := os.WriteFile(credential, []byte("forge-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("..", "..", "scripts", "run-body", "setup-workspace")
	cmd := exec.Command(script)
	cmd.Env = append(os.Environ(),
		"MINOS_API_BASE="+apiBase,
		"MINOS_CREDENTIAL_FILE="+credential,
		"MINOS_OWNER=owner",
		"MINOS_REPO_NAME=repository",
		"MINOS_PR=17",
		"MINOS_HEAD_SHA="+head,
		"MINOS_TARGET_SHA="+target,
		"MINOS_HEAD_BRANCH=feature",
		"MINOS_RUN_DIR="+runDir,
		"MINOS_WORKSPACE="+workspace,
		"MINOS_ORIENTATION="+orientation,
		"MINOS_GUIDANCE_SOURCES=[]",
	)
	return cmd
}

func createGitRepositoryAtHead(t *testing.T, name, contents string) string {
	t.Helper()
	repository, _ := createGitRepository(t, name, contents)
	return repository
}

func createGitRepository(t *testing.T, name, contents string) (string, string) {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "-b", "main")
	runGit(t, repository, "config", "user.name", "Fixture")
	runGit(t, repository, "config", "user.email", "fixture@example.invalid")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repository, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", name)
	runGit(t, repository, "commit", "-m", "fixture")
	return repository, gitOutput(t, repository, "rev-parse", "HEAD")
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func gitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
