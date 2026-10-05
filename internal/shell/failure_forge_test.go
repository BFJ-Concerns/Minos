package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFailureForgeConfiguration(t *testing.T) {
	for _, test := range []struct{ name, extra, runs, want, wantError string }{
		{name: "sole forge defaults", want: "local"},
		{name: "multiple forges require selection", extra: `[forges.other]
adaptation = "/adapt"
api-base = "http://other.invalid"
webhook-secret-file = "/secret"
credential-file = "/credential"
`, runs: "failures-repo = \"/ledger\"\n", wantError: "runs.failures-forge is required"},
		{name: "unknown selection refused", runs: "failures-forge = \"absent\"\n", wantError: "runs.failures-forge \"absent\""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			contents := strings.Replace(testServiceConfig, "[runs]\n", test.extra+"[runs]\n"+test.runs, 1)
			writeTestFile(t, filepath.Join(root, "service.toml"), contents)
			cfg, err := LoadServiceConfig(root)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want %q", err, test.wantError)
				}
			} else if err != nil || cfg.Runs.FailuresForge != test.want {
				t.Fatalf("failures forge = %q, error = %v, want %q", cfg.Runs.FailuresForge, err, test.want)
			}
		})
	}
}

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
