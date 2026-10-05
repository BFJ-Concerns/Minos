package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFailureLedgerPushUsesSelectedForgeCredential(t *testing.T) {
	state := newGitHubFixtureState(t)
	repository, _ := createGitRepository(t, "FAILURES.md", "# Failures\n")
	gitServer := newAuthenticatedGitServer(t, func(username, password string) bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return username == "x-access-token" && slices.Contains(state.mintedTokens, password)
	})
	gitServer.serve(t, repository)
	remote := filepath.Join(gitServer.root, filepath.Base(filepath.Dir(repository)), "source")
	runGit(t, ".", "--git-dir", remote, "config", "http.receivepack", "true")
	checkout := filepath.Join(t.TempDir(), "ledger")
	runGit(t, ".", "clone", remote, checkout)
	configureTestGit(t, checkout)
	runGit(t, checkout, "remote", "set-url", "origin", gitServer.URL+"/"+filepath.Base(filepath.Dir(repository))+"/source")
	writeTestFile(t, filepath.Join(checkout, "FAILURES.md"), "# Failures\n\n## salvaged installation run\n")
	root := t.TempDir()
	contents := fmt.Sprintf(`[service]
bot-login = "review-bot"
[listener]
bind = ":0"
[forges.unused]
adaptation = "/unused"
api-base = "http://unused.invalid"
webhook-secret-file = "/unused"
credential-file = "/unused"
[forges.github]
adaptation = %q
api-base = %q
webhook-secret-file = "/unused"
credential-file = "/wrong-reviewed-forge-credential"
[runs]
dir = %q
failures-forge = "github"
failures-repo = %q
failures-credential-file = %q
`, state.adaptationPath, state.server.URL, t.TempDir(), checkout, state.credentialPath)
	writeTestFile(t, filepath.Join(root, "service.toml"), contents)
	cfg, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	// An unauthenticated real push must be refused and cannot seed the assertion.
	cmd := processGroupCommandContext(t.Context(), "git", "-C", checkout, "push", "origin", "HEAD:main")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_COUNT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("unauthenticated push succeeded: %s", out)
	}
	if err := publishFailureDigest(t.Context(), cfg); err != nil {
		t.Fatalf("authenticated ledger push: %v", err)
	}
	got := gitOutput(t, ".", "--git-dir", remote, "show", "main:FAILURES.md")
	if !strings.Contains(got, "## salvaged installation run") {
		t.Fatalf("remote failure ledger = %q, want salvaged run", got)
	}
	if gitServer.authorised() == 0 {
		t.Fatal("git server authorised no request")
	}
}

func TestFailureLedgerRejectsEmptyCredentialWithoutPublishing(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	checkout := filepath.Join(root, "ledger")
	runGit(t, root, "init", "--bare", remote)
	runGit(t, root, "init", "-b", "main", checkout)
	configureTestGit(t, checkout)
	writeTestFile(t, filepath.Join(checkout, "FAILURES.md"), "# Failures\n")
	runGit(t, checkout, "add", "FAILURES.md")
	runGit(t, checkout, "commit", "-m", "seed ledger")
	runGit(t, checkout, "remote", "add", "origin", remote)
	runGit(t, checkout, "push", "origin", "main")
	before := gitOutput(t, ".", "--git-dir", remote, "rev-parse", "main")
	writeTestFile(t, filepath.Join(checkout, "FAILURES.md"), "# Failures\n\n## retained local evidence\n")
	credential := filepath.Join(root, "empty-credential")
	writeTestFile(t, credential, "")
	var cfg ServiceConfig
	configureFailureForge(t, &cfg)
	cfg.Runs.FailuresRepo = checkout
	cfg.Runs.FailuresCredentialFile = credential
	cfg.Service.CommitAuthorName = "Review Bot"
	cfg.Service.CommitAuthorEmail = "bot@example.org"
	if err := publishFailureDigest(t.Context(), cfg); !errors.Is(err, errEmptyForgeCredential) {
		t.Fatalf("empty ledger credential error = %v, want errEmptyForgeCredential", err)
	}
	if after := gitOutput(t, ".", "--git-dir", remote, "rev-parse", "main"); after != before {
		t.Fatalf("remote ledger moved from %s to %s after empty credential refusal", before, after)
	}
	if after := gitOutput(t, checkout, "rev-parse", "HEAD"); after != before {
		t.Fatalf("local ledger committed from %s to %s after empty credential refusal", before, after)
	}
	if contents, err := os.ReadFile(filepath.Join(checkout, "FAILURES.md")); err != nil || !strings.Contains(string(contents), "## retained local evidence") {
		t.Fatalf("local evidence lost after credential refusal: contents = %q, error = %v", contents, err)
	}
}
