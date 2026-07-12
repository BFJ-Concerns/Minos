package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestForgejoPresenceReactionUsesAuthenticatedIssueReaction(t *testing.T) {
	added := runForgejoWriteScript(t, "add-reaction", "{}", "owner", "repo", "7", "eyes")
	if !strings.Contains(added, "-X\nPOST") || !strings.Contains(added, "/issues/7/reactions") || !strings.Contains(added, `{"content":"eyes"}`) {
		t.Fatalf("add-reaction request is incomplete:\n%s", added)
	}
}

func TestEnsembleLauncherKeepsDurableRecordsInAttemptDirectory(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "ensemble")
	capture := filepath.Join(t.TempDir(), "capture")
	runDir := filepath.Join(t.TempDir(), "attempt")
	writeScript(t, fake, `#!/bin/sh
set -eu
printf '%s\n' "$ENSEMBLE_RUN_RECORD_DIR" >"$MINOS_TEST_CAPTURE"
printf '%s\n' "$@" >>"$MINOS_TEST_CAPTURE"
`)
	path := filepath.Join("..", "..", "scripts", "run-body", "launch-ensemble")
	cmd := exec.Command(path, "--json-args", "@review.json", "review.js")
	cmd.Env = append(os.Environ(), "MINOS_ENSEMBLE="+fake, "MINOS_RUN_DIR="+runDir, "MINOS_TEST_CAPTURE="+capture)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launch ensemble: %v\n%s", err, out)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(runDir, "ensemble") + "\n--json-args\n@review.json\nreview.js\n"
	if string(data) != want {
		t.Fatalf("launcher capture = %q, want %q", data, want)
	}
}

func TestForgejoRemoveReactionTreatsConfirmedAbsenceAsSuccess(t *testing.T) {
	fakeBin := t.TempDir()
	curl := filepath.Join(fakeBin, "curl")
	capture := filepath.Join(fakeBin, "capture")
	writeScript(t, curl, `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$MINOS_TEST_CAPTURE"
output=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    -w) shift 2 ;;
    *) shift ;;
  esac
done
: >"$output"
printf '404'
`)
	path := filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "remove-reaction")
	for attempt := 1; attempt <= 2; attempt++ {
		cmd := exec.Command(path, "owner", "repo", "7", "eyes")
		cmd.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "MINOS_API_BASE=http://forge.invalid", "MINOS_FORGE_TOKEN=token", "MINOS_TEST_CAPTURE="+capture)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("idempotent removal %d failed: %v\n%s", attempt, err, out)
		}
	}
	requests, err := os.ReadFile(capture)
	if err != nil || !strings.Contains(string(requests), "-X DELETE") || !strings.Contains(string(requests), "/issues/7/reactions") || !strings.Contains(string(requests), `{"content":"eyes"}`) {
		t.Fatalf("remove-reaction request is incomplete: %q err=%v", requests, err)
	}
}

func TestForgejoListOpenPRsPaginatesUntilEmpty(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "list-open-prs")
	fakeBin := t.TempDir()
	curl := filepath.Join(fakeBin, "curl")
	script := `#!/usr/bin/env sh
for arg do url="$arg"; done
case "$url" in
  *page=1*) printf '%s\n' '[{"number":1,"head":{"sha":"aaaaaaaaaaaa"},"base":{"ref":"main"},"user":{"login":"alice"},"draft":false,"labels":[]}]' ;;
  *page=2*) printf '%s\n' '[{"number":2,"head":{"sha":"bbbbbbbbbbbb"},"base":{"ref":"main"},"user":{"login":"bob"},"draft":false,"labels":[]}]' ;;
  *) printf '%s\n' '[]' ;;
esac
`
	if err := os.WriteFile(curl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "owner", "repo")
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "MINOS_API_BASE=http://forge.invalid", "MINOS_FORGE_TOKEN=token")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list-open-prs failed: %v\n%s", err, out)
	}
	if strings.Count(string(out), "OWNER=owner") != 2 || !strings.Contains(string(out), "PR=1") || !strings.Contains(string(out), "PR=2") {
		t.Fatalf("list-open-prs pagination output:\n%s", out)
	}
}

func runForgejoWriteScript(t *testing.T, name, response string, args ...string) string {
	t.Helper()
	fakeBin := t.TempDir()
	capture := filepath.Join(fakeBin, "capture")
	curl := filepath.Join(fakeBin, "curl")
	script := "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >\"$MINOS_TEST_CAPTURE\"\nprintf '%s\\n' '" + response + "'\n"
	if err := os.WriteFile(curl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "scripts", "adaptations", "forgejo", name)
	cmd := exec.Command(path, args...)
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "MINOS_API_BASE=http://forge.invalid", "MINOS_FORGE_TOKEN=token", "MINOS_TEST_CAPTURE="+capture)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", name, err, out)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
