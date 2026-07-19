package shell

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupWorkspaceChecksOutHeadClonesAnnexeAndConfiguresAuthor(t *testing.T) {
	repository, head := createGitRepository(t, "code.txt", "reviewed code\n")
	annexe := createGitRepositoryAtHead(t, "README.md", "# Commission\n")
	server := newSetupForge(t, head, repository, annexe)

	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	orientation := filepath.Join(runDir, "orientation.json")
	runSetupWorkspace(t, server.URL, runDir, workspace, orientation, head)

	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != head {
		t.Fatalf("workspace head = %q, want %q", got, head)
	}
	if got := gitOutput(t, workspace, "config", "--get", "http.extraHeader"); got != "Authorization: token forge-token" {
		t.Fatalf("workspace Git authentication = %q, want forge token header", got)
	}
	annexePath := filepath.Join(runDir, "repository-Annexe")
	assertContainsFile(t, filepath.Join(annexePath, "README.md"), "# Commission")
	if got := gitOutput(t, annexePath, "config", "--get", "http.extraHeader"); got != "Authorization: token forge-token" {
		t.Fatalf("annexe Git authentication = %q, want forge token header", got)
	}
	if got := gitOutput(t, annexePath, "config", "user.name"); got != "Minos" {
		t.Fatalf("annexe Git author = %q, want Minos", got)
	}

	state := readOrientation(t, orientation)
	if state.Grounding != "annexe" || state.Guidance != filepath.Join(annexePath, "README.md") {
		t.Fatalf("orientation = %+v, want annexe README grounding", state)
	}

	if err := os.WriteFile(filepath.Join(workspace, "repair.txt"), []byte("repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "repair.txt")
	runGit(t, workspace, "commit", "-m", "fix: repair subject")
	if got := gitOutput(t, workspace, "log", "-1", "--format=%an <%ae>"); got != "Minos <minos@example.invalid>" {
		t.Fatalf("commit author = %q, want Minos identity", got)
	}
}

func TestSetupWorkspaceRecordsRepositoryGuidanceFallbackWithoutAnnexe(t *testing.T) {
	repository, head := createGitRepository(t, "AGENTS.md", "MINOS_REPOSITORY_GUIDANCE_OCHRE_719\n")
	server := newSetupForge(t, head, repository, "")

	runDir := t.TempDir()
	orientation := filepath.Join(runDir, "orientation.json")
	runSetupWorkspace(t, server.URL, runDir, filepath.Join(runDir, "workspace"), orientation, head)

	state := readOrientation(t, orientation)
	if state.Grounding != "repository" || state.Reason != "annexe-not-found" {
		t.Fatalf("orientation = %+v, want explicit no-annexe fallback", state)
	}
	if state.Guidance != filepath.Join(runDir, "workspace", "AGENTS.md") {
		t.Fatalf("orientation guidance = %q, want repository AGENTS.md", state.Guidance)
	}
	assertContainsFile(t, state.Guidance, "MINOS_REPOSITORY_GUIDANCE_OCHRE_719")
	if _, err := os.Stat(filepath.Join(runDir, "repository-Annexe")); !os.IsNotExist(err) {
		t.Fatalf("annexe path exists or stat failed unexpectedly: %v", err)
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
	if state.Guidance != filepath.Join(runDir, "workspace", "CLAUDE.md") {
		t.Fatalf("orientation guidance = %q, want non-empty repository CLAUDE.md", state.Guidance)
	}
	assertContainsFile(t, state.Guidance, "MINOS_REPOSITORY_GUIDANCE_SIENNA_719")
}

func TestSetupWorkspaceRejectsAnnexeWithoutSubstantiveReadmeGuidance(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		contents string
	}{
		{name: "missing", file: "NOTES.md", contents: "# Notes\n"},
		{name: "empty", file: "README.md", contents: ""},
		{name: "whitespace only", file: "README.md", contents: " \n\t"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, head := createGitRepository(t, "code.txt", "reviewed code\n")
			annexe := createGitRepositoryAtHead(t, test.file, test.contents)
			server := newSetupForge(t, head, repository, annexe)
			runDir := t.TempDir()
			cmd := setupWorkspaceCommand(t, server.URL, runDir, filepath.Join(runDir, "workspace"), filepath.Join(runDir, "orientation.json"), head)
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "annexe README.md guidance is missing or empty") {
				t.Fatalf("setup-workspace error = %v, output = %q", err, output)
			}
		})
	}
}

func TestSetupWorkspaceRejectsEmptyRepositoryGuidanceFallback(t *testing.T) {
	repository, head := createGitRepository(t, "AGENTS.md", " \n\t")
	server := newSetupForge(t, head, repository, "")
	runDir := t.TempDir()
	cmd := setupWorkspaceCommand(t, server.URL, runDir, filepath.Join(runDir, "workspace"), filepath.Join(runDir, "orientation.json"), head)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "repository fallback has no non-empty checked-in guidance") {
		t.Fatalf("setup-workspace error = %v, output = %q", err, output)
	}
}

type orientationState struct {
	Repository string `json:"repository"`
	Head       string `json:"head"`
	Grounding  string `json:"grounding"`
	Annexe     string `json:"annexe"`
	Guidance   string `json:"guidance"`
	Reason     string `json:"reason"`
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

func newSetupForge(t *testing.T, head, repository, annexe string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token forge-token" {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/repos/owner/repository/pulls/17":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"head":{"sha":%q,"repo":{"clone_url":%q}}}`, head, repository)
		case "/api/v1/repos/owner/repository-Annexe":
			if annexe == "" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"clone_url":%q}`, annexe)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func runSetupWorkspace(t *testing.T, apiBase, runDir, workspace, orientation, head string) {
	t.Helper()
	cmd := setupWorkspaceCommand(t, apiBase, runDir, workspace, orientation, head)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup-workspace failed: %v\n%s", err, output)
	}
}

func setupWorkspaceCommand(t *testing.T, apiBase, runDir, workspace, orientation, head string) *exec.Cmd {
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
		"MINOS_WORKSPACE="+workspace,
		"MINOS_ORIENTATION="+orientation,
		"MINOS_GIT_AUTHOR_NAME=Minos",
		"MINOS_GIT_AUTHOR_EMAIL=minos@example.invalid",
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
