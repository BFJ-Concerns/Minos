package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestForgejoLabelActorUsesLatestMatchingTimelineEvent(t *testing.T) {
	out := runForgejoScript(t, "label-actor", `[{
		"type":"label","label":{"name":"Ready"},"user":{"login":"bob"}
	},{
		"type":"unlabel","label":{"name":"Ready"},"user":{"login":"bob"}
	},{
		"type":"label","label":{"name":"Ready"},"user":{"login":"mallory"}
	}]`, "owner", "repo", "1", "Ready")
	if strings.TrimSpace(out) != "mallory" {
		t.Fatalf("label actor = %q, want mallory", out)
	}
}

func TestForgejoLatestLabelEventReportsGenericAddedAndRemovedOccasions(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "forgejo14", "issue-timeline-label-add-remove.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := runForgejoScript(t, "latest-label-event", string(fixture), "owner", "repo", "1")
	values, err := parseKeyValues(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if values["ACTION"] != "removed" || values["LABEL"] != "Ready" || values["ACTOR"] != "bob" {
		t.Fatalf("latest label event = %#v", values)
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
  *page=2*) printf '%s\n' '[{"number":2,"head":{"sha":"bbbbbbbbbbbb"},"base":{"ref":"main"},"user":{"login":"bob"},"draft":false,"labels":[{"name":"Ready"}]}]' ;;
  *) printf '%s\n' '[]' ;;
esac
`
	if err := os.WriteFile(curl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "owner", "repo")
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"PUMP19_API_BASE=http://forge.invalid",
		"PUMP19_FORGE_TOKEN=token",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list-open-prs failed: %v\n%s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "PR=1") || !strings.Contains(text, "PR=2") || !strings.Contains(text, "LABELS=Ready") {
		t.Fatalf("paginated facts missing from output:\n%s", text)
	}
}

func runForgejoScript(t *testing.T, name, curlOutput string, args ...string) string {
	t.Helper()
	fakeBin := t.TempDir()
	curl := filepath.Join(fakeBin, "curl")
	if err := os.WriteFile(curl, []byte("#!/usr/bin/env sh\ncat <<'JSON'\n"+curlOutput+"\nJSON\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "scripts", "adaptations", "forgejo", name)
	cmd := exec.Command(path, args...)
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"PUMP19_API_BASE=http://forge.invalid",
		"PUMP19_FORGE_TOKEN=token",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", name, err, out)
	}
	return string(out)
}
