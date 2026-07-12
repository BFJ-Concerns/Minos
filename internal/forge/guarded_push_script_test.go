package forge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardedCommitPushScrubsWorkspaceGitAndDiscoversRemoteThroughAPI(t *testing.T) {
	adaptation := t.TempDir()
	copyExecutable(t, filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "common.sh"), filepath.Join(adaptation, "common.sh"))
	copyExecutable(t, filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "guarded-commit-push"), filepath.Join(adaptation, "guarded-commit-push"))
	writeExecutable(t, filepath.Join(adaptation, "commit-push"), "#!/usr/bin/env sh\nexit 71\n")

	bin := t.TempDir()
	gitLog := filepath.Join(t.TempDir(), "git.log")
	gitCount := filepath.Join(t.TempDir(), "git.count")
	writeExecutable(t, filepath.Join(bin, "git"), `#!/usr/bin/env sh
set -eu
if [ -n "${MINOS_FORGE_TOKEN:-}" ]; then
  echo token-present >>"$TEST_GIT_LOG"
fi
printf '%s\n' "$*" >>"$TEST_GIT_LOG"
case " $* " in
  *" ls-remote "*) echo "workspace git performed remote discovery" >&2; exit 99 ;;
esac
count=0
[ ! -f "$TEST_GIT_COUNT" ] || count="$(cat "$TEST_GIT_COUNT")"
count=$((count + 1))
printf '%s\n' "$count" >"$TEST_GIT_COUNT"
if [ "$count" -eq 1 ]; then printf '%s\n' expected-head; else printf '%s\n' landed-head; fi
`)
	writeExecutable(t, filepath.Join(bin, "curl"), `#!/usr/bin/env sh
set -eu
url=""
for argument in "$@"; do url="$argument"; done
case "$url" in
  */api/v1/user) printf '%s\n' '{"login":"Minos"}' ;;
  */pulls/4) printf '%s\n' '{"number":4,"state":"open","merged":false,"head":{"sha":"expected-head","ref":"feature","repo":{"full_name":"acme/widget"}},"base":{"ref":"main","repo":{"full_name":"acme/widget"}}}' ;;
  */branches/main) printf '%s\n' '{"commit":{"id":"expected-target"},"user_can_merge":true}' ;;
  */branches/feature) printf '%s\n' '{"commit":{"id":"landed-head"}}' ;;
  *) echo "unexpected curl URL: $url" >&2; exit 2 ;;
esac
`)

	message := filepath.Join(t.TempDir(), "message")
	if err := os.WriteFile(message, []byte("guarded push\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_GIT_LOG", gitLog)
	t.Setenv("TEST_GIT_COUNT", gitCount)

	adapter, err := NewAdapter(ScriptRunner{Directory: adaptation, APIBase: "https://forge.invalid", Credential: "secret-token"}, conformanceOwnership{}, "Minos")
	if err != nil {
		t.Fatal(err)
	}
	result := adapter.Push(t.Context(), Guard{
		Ownership: LifecycleOwnership(1), Repository: Repository{Owner: "acme", Name: "widget"},
		PullRequest: 4, HeadSHA: "expected-head", TargetSHA: "expected-target",
	}, PushRequest{
		Branch: "feature", AuthorName: "Minos", AuthorEmail: "minos@example.invalid",
		MessageFile: message, Workspace: t.TempDir(),
	})
	if result.Outcome != WriteApplied || result.SHA != "landed-head" {
		t.Fatalf("Push() = %#v, want API-discovered applied outcome", result)
	}

	log, err := os.ReadFile(gitLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "token-present") {
		t.Fatalf("workspace Git inherited forge token:\n%s", log)
	}
	if strings.Contains(string(log), "ls-remote") {
		t.Fatalf("workspace Git performed remote discovery:\n%s", log)
	}
	if !strings.Contains(string(log), "-c core.hooksPath=/dev/null -c core.fsmonitor=") {
		t.Fatalf("workspace Git did not disable hooks and fsmonitor:\n%s", log)
	}
}

func copyExecutable(t *testing.T, source, destination string) {
	t.Helper()
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, content, 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
