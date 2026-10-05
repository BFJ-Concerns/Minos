package shell

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cgi"
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
	runSetupWorkspaceWithGuidance(t, server.Bin, runDir, workspace, orientation, head, secondaryGuidance)

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

// The forge's clone credential is the only thing that authorises the clones:
// a git server demanding HTTPS Basic authorisation serves the reviewed and
// guidance repositories, and the credential the forge reports — GitHub's
// x-access-token form here — is what setup must present.
func TestSetupWorkspaceClonesWithTheForgesCloneCredential(t *testing.T) {
	repository, head := createGitRepository(t, "AGENTS.md", "reviewed guidance\n")
	secondary := createGitRepositoryAtHead(t, "README.md", "# Commission\n")
	git := newAuthenticatedGitServer(t, func(username, password string) bool {
		return username == "x-access-token" && password == "installation-token"
	})
	server := newSetupForge(t, head, git.URL+"/"+filepath.Base(filepath.Dir(repository))+"/source", git.URL+"/"+filepath.Base(filepath.Dir(secondary))+"/source")
	git.serve(t, repository)
	git.serve(t, secondary)

	for _, test := range []struct {
		name               string
		username, password string
		succeeds           bool
	}{
		{name: "the forge's credential", username: "x-access-token", password: "installation-token", succeeds: true},
		{name: "another credential", username: "x-access-token", password: "stale-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server.credential(t, test.username, test.password)
			runDir := t.TempDir()
			workspace := filepath.Join(runDir, "workspace")
			cmd := setupWorkspaceCommand(t, server.Bin, runDir, workspace, filepath.Join(runDir, "orientation.json"), head)
			cmd.Env = append(cmd.Env, "MINOS_GUIDANCE_SOURCES="+secondaryGuidance)
			output, err := cmd.CombinedOutput()
			if !test.succeeds {
				// Git answers the server's 401 by asking for a credential it
				// may not prompt for, or by reporting the rejected one.
				refused := strings.Contains(string(output), "Authentication failed") || strings.Contains(string(output), "terminal prompts disabled")
				if err == nil || !strings.Contains(string(output), "Cloning into") || !refused {
					t.Fatalf("setup-workspace error = %v without the forge's credential; want the clone refused: %s", err, output)
				}
				return
			}
			if err != nil {
				t.Fatalf("setup-workspace failed: %v\n%s", err, output)
			}
			if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != head {
				t.Fatalf("workspace head = %q, want %q", got, head)
			}
			assertContainsFile(t, filepath.Join(runDir, "guidance", "owner", "repository-plans", "README.md"), "# Commission")
			// The secret is never output, and never persisted in a clone.
			if strings.Contains(string(output), test.password) {
				t.Fatalf("setup-workspace output carries the clone secret")
			}
			for _, clone := range []string{workspace, filepath.Join(runDir, "guidance", "owner", "repository-plans")} {
				if config, err := os.ReadFile(filepath.Join(clone, ".git", "config")); err != nil || strings.Contains(string(config), "Authorization") {
					t.Fatalf("%s config read error %v or carries an authorisation header", clone, err)
				}
			}
			if got := git.authorised(); got == 0 {
				t.Fatalf("git server saw no authorised request")
			}
		})
	}
}

// authenticatedGitServer serves bare copies of fixture repositories over
// Git's smart HTTP protocol, answering 401 to any request whose Basic
// authorisation accept refuses.
type authenticatedGitServer struct {
	*httptest.Server
	root  string
	count chan int
}

func newAuthenticatedGitServer(t *testing.T, accept func(username, password string) bool) *authenticatedGitServer {
	t.Helper()
	execPath := strings.TrimSpace(gitOutput(t, ".", "--exec-path"))
	server := &authenticatedGitServer{root: t.TempDir(), count: make(chan int, 1)}
	server.count <- 0
	backend := &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + server.root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || !accept(username, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		server.count <- <-server.count + 1
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

// serve publishes repository at <its parent directory's name>/source.
func (s *authenticatedGitServer) serve(t *testing.T, repository string) {
	t.Helper()
	target := filepath.Join(s.root, filepath.Base(filepath.Dir(repository)), "source")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, ".", "clone", "--quiet", "--bare", repository, target)
}

func (s *authenticatedGitServer) authorised() int {
	count := <-s.count
	s.count <- count
	return count
}

func TestSetupWorkspaceWarmResumeReestablishesSafetyState(t *testing.T) {
	repository, head := createGitRepository(t, "code.txt", "reviewed code\n")
	secondary := createGitRepositoryAtHead(t, "README.md", "# Commission\n")
	server := newSetupForge(t, head, repository, secondary)
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	orientation := filepath.Join(runDir, "orientation.json")
	runSetupWorkspaceWithGuidance(t, server.Bin, runDir, workspace, orientation, head, secondaryGuidance)
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

	cmd := setupWorkspaceCommand(t, server.Bin, runDir, workspace, orientation, head)
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
	runSetupWorkspace(t, server.Bin, runDir, workspace, orientation, head)
	if err := os.RemoveAll(filepath.Join(workspace, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "abandoned.txt"), []byte("not a repository\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := setupWorkspaceCommand(t, server.Bin, runDir, workspace, orientation, head)
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
	cmd := setupWorkspaceCommand(t, server.Bin, runDir, filepath.Join(runDir, "workspace"), filepath.Join(runDir, "orientation.json"), admitted)
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
	runSetupWorkspace(t, server.Bin, runDir, filepath.Join(runDir, "workspace"), orientation, head)

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
	runSetupWorkspaceWithGuidance(t, server.Bin, runDir, filepath.Join(runDir, "workspace"), orientation, head,
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
			runSetupWorkspaceWithGuidance(t, server.Bin, runDir, filepath.Join(runDir, "workspace"), orientation, head,
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
	cmd := setupWorkspaceCommand(t, server.Bin, runDir, filepath.Join(runDir, "workspace"), filepath.Join(runDir, "orientation.json"), head)
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
			cmd := setupWorkspaceCommand(t, server.Bin, runDir, filepath.Join(runDir, "workspace"), filepath.Join(runDir, "orientation.json"), head)
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
	runSetupWorkspace(t, server.Bin, runDir, filepath.Join(runDir, "workspace"), orientation, head)

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
	cmd := setupWorkspaceCommand(t, server.Bin, runDir, filepath.Join(runDir, "workspace"), filepath.Join(runDir, "orientation.json"), head)
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
	runSetupWorkspace(t, server.Bin, runDir, workspace, filepath.Join(runDir, "orientation.json"), head)

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

	runSetupWorkspaceWithTarget(t, server.Bin, runDir, workspace, filepath.Join(runDir, "orientation.json"), head, target)

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

func TestSetupWorkspaceExplainsRefusedTargetFetch(t *testing.T) {
	server, head, target, transportEnv := refusedTargetForge(t)
	runDir := t.TempDir()
	cmd := setupWorkspaceCommandWithTarget(t, server.Bin, runDir, filepath.Join(runDir, "workspace"), filepath.Join(runDir, "orientation.json"), head, target)
	for key, value := range transportEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("refused fetch exit = %v, want 1; output = %s", err, output)
	}
	for _, want := range []string{"unadvertised object " + target, "target fetch refused: the forge would not serve target sha " + target, "likely because the base branch moved", "the next sweep re-derives"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("refused target fetch output lacks %q: %s", want, output)
		}
	}
}

func TestSetupWorkspaceExplainsProtocolV2TargetRefusal(t *testing.T) {
	server, head, target, transportEnv := missingTargetForge(t)
	runDir := t.TempDir()
	cmd := setupWorkspaceCommandWithTarget(t, server.Bin, runDir, filepath.Join(runDir, "workspace"), filepath.Join(runDir, "orientation.json"), head, target)
	for key, value := range transportEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 128 {
		t.Fatalf("protocol v2 refusal exit = %v, want 128; output = %s", err, output)
	}
	for _, want := range []string{"upload-pack: not our ref " + target, "target fetch refused: the forge would not serve target sha " + target, "likely because the base branch moved", "the next sweep re-derives"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("protocol v2 refusal output lacks %q: %s", want, output)
		}
	}
}

// Real protocol v2 upload-pack rejects a SHA absent from its object store.
// The unserved repository supplies only the requested SHA, never error text.
func missingTargetForge(t *testing.T) (*setupForge, string, string, map[string]string) {
	t.Helper()
	targetRepository, _ := createGitRepository(t, "AGENTS.md", "served target guidance\n")
	_, target := createGitRepository(t, "AGENTS.md", "unserved target guidance\n")
	headRepository, head := createGitRepository(t, "AGENTS.md", "head guidance\n")
	ssh := filepath.Join(t.TempDir(), "ssh")
	writeScript(t, ssh, "#!/usr/bin/env sh\nexec git upload-pack '"+targetRepository+"'\n")
	return newSetupForgeForTarget(t, head, headRepository, target, "ssh://fixture/target", ""), head, target, map[string]string{
		"GIT_SSH_COMMAND": ssh, "GIT_SSH_VARIANT": "ssh",
		"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "protocol.version", "GIT_CONFIG_VALUE_0": "2",
	}
}

// The SSH stand-in replaces only sshd: both client and upload-pack are real
// Git. Protocol v0 exposes the server's refusal to accept unadvertised wants;
// the admitted target remains in the server's object store after main moves.
func refusedTargetForge(t *testing.T) (*setupForge, string, string, map[string]string) {
	t.Helper()
	targetRepository, target := createGitRepository(t, "AGENTS.md", "target guidance\n")
	if err := os.WriteFile(filepath.Join(targetRepository, "AGENTS.md"), []byte("moved guidance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, targetRepository, "commit", "-am", "move target")
	headRepository, head := createGitRepository(t, "AGENTS.md", "head guidance\n")
	ssh := filepath.Join(t.TempDir(), "ssh")
	writeScript(t, ssh, "#!/usr/bin/env sh\nexec git -c uploadpack.allowAnySHA1InWant=false -c uploadpack.allowReachableSHA1InWant=false -c uploadpack.allowTipSHA1InWant=false upload-pack '"+targetRepository+"'\n")
	return newSetupForgeForTarget(t, head, headRepository, target, "ssh://fixture/target", ""), head, target, map[string]string{
		"GIT_SSH_COMMAND": ssh, "GIT_SSH_VARIANT": "ssh",
		"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "protocol.version", "GIT_CONFIG_VALUE_0": "0",
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

// setupForge is a stand-in for the minos binary's forge surface: the
// snapshot, repository-metadata and clone-credential answers a configured
// forge's adaptation would give, written as files the stub script serves.
type setupForge struct {
	Bin string
	dir string
}

// newSetupForge serves the fixture pull request and, when secondary is a
// clone URL, the secondary repository owner/repository-plans that configured
// guidance sources may name.
func newSetupForge(t *testing.T, head, repository, secondary string) *setupForge {
	t.Helper()
	return newSetupForgeForTarget(t, head, repository, head, repository, secondary)
}

// A head repository other than the target is a fork, named fork/repository.
func newSetupForgeForTarget(t *testing.T, head, repository, target, targetRepository, secondary string) *setupForge {
	t.Helper()
	stub := &setupForge{dir: t.TempDir()}
	headRepository := "owner/repository"
	if repository != targetRepository {
		headRepository = "fork/repository"
	}
	stub.write(t, "snapshot.json", fmt.Sprintf(`{
		"pull_request":17,
		"title":"Fixture change",
		"body":"Not in scope: a concrete transport.",
		"head_sha":%q,"head_branch":"feature","head_repository":%q,
		"target_sha":%q,"target_branch":"main","target_repository":"owner/repository"
	}`, head, headRepository, target))
	stub.repository(t, "owner/repository", targetRepository)
	if headRepository != "owner/repository" {
		stub.repository(t, headRepository, repository)
	}
	if secondary != "" {
		stub.repository(t, "owner/repository-plans", secondary)
	}
	stub.credential(t, "minos", "forge-token")
	stub.Bin = filepath.Join(stub.dir, "minos")
	writeScript(t, stub.Bin, `#!/usr/bin/env sh
set -eu
stub="$(dirname "$0")"
[ "$1" = forge ] || exit 2
case "$2" in
  snapshot) cat "$stub/snapshot.json" ;;
  clone-credential) cat "$stub/credential.json" ;;
  repository-metadata)
    [ -f "$stub/metadata/$3/$4.json" ] || { echo '{"lookup_status":404}'; exit 1; }
    cat "$stub/metadata/$3/$4.json" ;;
  *) exit 2 ;;
esac
`)
	return stub
}

func (s *setupForge) write(t *testing.T, name, contents string) {
	t.Helper()
	path := filepath.Join(s.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (s *setupForge) repository(t *testing.T, name, cloneURL string) {
	t.Helper()
	s.write(t, filepath.Join("metadata", name+".json"), fmt.Sprintf(`{"clone_url":%q,"default_branch":"main"}`, cloneURL))
}

func (s *setupForge) credential(t *testing.T, username, password string) {
	t.Helper()
	s.write(t, "credential.json", fmt.Sprintf(`{"username":%q,"password":%q}`, username, password))
}

func runSetupWorkspace(t *testing.T, minosBin, runDir, workspace, orientation, head string) {
	t.Helper()
	runSetupWorkspaceWithTarget(t, minosBin, runDir, workspace, orientation, head, head)
}

// secondaryGuidance is the configured source most tests use: README.md in
// the secondary repository the fixture forge serves.
const secondaryGuidance = `[{"repository":"owner/repository-plans","path":"README.md"}]`

func runSetupWorkspaceWithGuidance(t *testing.T, minosBin, runDir, workspace, orientation, head, sources string) {
	t.Helper()
	cmd := setupWorkspaceCommand(t, minosBin, runDir, workspace, orientation, head)
	cmd.Env = append(cmd.Env, "MINOS_GUIDANCE_SOURCES="+sources)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup-workspace failed: %v\n%s", err, output)
	}
}

func runSetupWorkspaceWithTarget(t *testing.T, minosBin, runDir, workspace, orientation, head, target string) {
	t.Helper()
	cmd := setupWorkspaceCommandWithTarget(t, minosBin, runDir, workspace, orientation, head, target)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup-workspace failed: %v\n%s", err, output)
	}
}

func setupWorkspaceCommand(t *testing.T, minosBin, runDir, workspace, orientation, head string) *exec.Cmd {
	t.Helper()
	return setupWorkspaceCommandWithTarget(t, minosBin, runDir, workspace, orientation, head, head)
}

func setupWorkspaceCommandWithTarget(t *testing.T, minosBin, runDir, workspace, orientation, head, target string) *exec.Cmd {
	t.Helper()
	script := filepath.Join("..", "..", "scripts", "run-body", "setup-workspace")
	cmd := exec.Command(script)
	cmd.Env = append(os.Environ(),
		"MINOS_BIN="+minosBin,
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
