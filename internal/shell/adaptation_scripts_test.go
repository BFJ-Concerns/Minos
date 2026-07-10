package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdaptCommandLoadsCredentialAndRejectsPaths(t *testing.T) {
	root := t.TempDir()
	adaptationDir := filepath.Join(root, "adaptation")
	if err := os.Mkdir(adaptationDir, 0o755); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(root, "token")
	webhookSecret := filepath.Join(root, "webhook-secret")
	if err := os.WriteFile(credential, []byte("service-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(webhookSecret, []byte("webhook-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptationDir, "inspect"), "#!/usr/bin/env sh\nprintf '%s:%s' \"$PUMP19_FORGE_TOKEN\" \"$1\"\n")
	service := "[listener]\nbind = \":0\"\n\n[forges.local]\nadaptation = \"" + adaptationDir + "\"\napi-base = \"http://forge.invalid\"\nwebhook-secret-file = \"" + webhookSecret + "\"\ncredential-file = \"" + credential + "\"\n\n[runs]\ndir = \"" + filepath.Join(root, "runs") + "\"\nmax-concurrent = 2\n\n[sweep]\nliveness-threshold = \"1h\"\n"
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PUMP19_CONFIG", root)
	t.Setenv("PUMP19_FORGE", "local")
	var out bytes.Buffer
	if err := AdaptCommand(context.Background(), []string{"inspect", "argument"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "service-token:argument" {
		t.Fatalf("adapt output = %q", out.String())
	}
	if err := AdaptCommand(context.Background(), []string{"../escape"}, strings.NewReader(""), &out); err == nil {
		t.Fatal("adapt accepted a caller-selected path")
	}
}

func TestForgejoPostReviewMapsServiceStateAndPreservesAnchors(t *testing.T) {
	dir := t.TempDir()
	body := filepath.Join(dir, "body.md")
	comments := filepath.Join(dir, "comments.json")
	if err := os.WriteFile(body, []byte("Review body\n\nPump-19: bar=passed coverage=full head=abc run=review verdict=converged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(comments, []byte(`[{"path":"main.go","body":"Finding","new_position":12,"old_position":0}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	captured := runForgejoWriteScript(t, "post-review", "{}", "owner", "repo", "7", "abc", "APPROVE", body, comments)
	if !strings.Contains(captured, "/pulls/7/reviews") || !strings.Contains(captured, `"event":"APPROVED"`) || !strings.Contains(captured, `"new_position":12`) {
		t.Fatalf("post-review request lost state or anchor:\n%s", captured)
	}
}

func TestForgejoUpdateCommentTargetsStableComment(t *testing.T) {
	dir := t.TempDir()
	body := filepath.Join(dir, "body.md")
	if err := os.WriteFile(body, []byte("Updated\n\nPump-19: finding=F-7KQ3 head=abc priority=P1 run=review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	captured := runForgejoWriteScript(t, "update-comment", "{}", "owner", "repo", "91", body)
	if !strings.Contains(captured, "PATCH") || !strings.Contains(captured, "/issues/comments/91") || !strings.Contains(captured, "F-7KQ3") {
		t.Fatalf("update-comment request lost identity or body:\n%s", captured)
	}
}

func TestForgejoPresenceReactionsUseAuthenticatedIssueReaction(t *testing.T) {
	added := runForgejoWriteScript(t, "add-reaction", "{}", "owner", "repo", "7", "eyes")
	if !strings.Contains(added, "-X\nPOST") || !strings.Contains(added, "/issues/7/reactions") || !strings.Contains(added, `{"content":"eyes"}`) {
		t.Fatalf("add-reaction request is incomplete:\n%s", added)
	}

	removed := runForgejoWriteScript(t, "remove-reaction", "{}", "owner", "repo", "7", "eyes")
	if !strings.Contains(removed, "-X\nDELETE") || !strings.Contains(removed, "/issues/7/reactions") || !strings.Contains(removed, `{"content":"eyes"}`) {
		t.Fatalf("remove-reaction request is incomplete:\n%s", removed)
	}
}

func TestForgejoAssignIfMissingWritesOnlyWhenBotIsAbsent(t *testing.T) {
	absent := runForgejoAssignIfMissing(t, `{"assignees":[{"login":"alice"}]}`)
	if strings.Count(absent, "-X GET") != 1 || strings.Count(absent, "-X PATCH") != 1 {
		t.Fatalf("absent bot should produce one read and one assignment:\n%s", absent)
	}
	if !strings.Contains(absent, "/pulls/7") || !strings.Contains(absent, "/issues/7") || !strings.Contains(absent, `{"assignees":["Minos"]}`) {
		t.Fatalf("assignment request is incomplete:\n%s", absent)
	}

	present := runForgejoAssignIfMissing(t, `{"assignees":[{"login":"minos"}]}`)
	if strings.Count(present, "-X GET") != 1 || strings.Contains(present, "-X PATCH") {
		t.Fatalf("existing bot assignment should produce one read and no write:\n%s", present)
	}
}

func TestForgejoLabelActorUsesLatestMatchingTimelineEvent(t *testing.T) {
	tests := []struct {
		fixture string
		label   string
		actor   string
	}{
		{"timeline-ready-added.json", LabelReady, "bob"},
		{"timeline-partial-coverage-added.json", LabelPartialCoverage, "bob"},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			out := runForgejoScriptWithFixture(t, "label-actor", tt.fixture, "owner", "repo", "1", tt.label)
			if strings.TrimSpace(out) != tt.actor {
				t.Fatalf("label actor = %q, want %s", out, tt.actor)
			}
		})
	}
}

func TestForgejoLatestLabelEventReportsGenericAddedAndRemovedOccasions(t *testing.T) {
	tests := []struct {
		fixture string
		action  string
		label   string
		actor   string
	}{
		{"timeline-ready-added.json", "added", LabelReady, "bob"},
		{"timeline-ready-removed.json", "removed", LabelReady, "bob"},
		{"timeline-partial-coverage-added.json", "added", LabelPartialCoverage, "bob"},
		{"timeline-partial-coverage-removed.json", "removed", LabelPartialCoverage, "bob"},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			out := runForgejoScriptWithFixture(t, "latest-label-event", tt.fixture, "owner", "repo", "1")
			values, err := parseKeyValues(strings.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			if values["ACTION"] != tt.action || values["LABEL"] != tt.label || values["ACTOR"] != tt.actor {
				t.Fatalf("latest label event = %#v, want action=%s label=%s actor=%s", values, tt.action, tt.label, tt.actor)
			}
		})
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

func TestForgejoListReviewCommentsPaginatesAndNormalisesAnchors(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "list-review-comments")
	fakeBin := t.TempDir()
	curl := filepath.Join(fakeBin, "curl")
	script := `#!/usr/bin/env sh
for arg do url="$arg"; done
case "$url" in
  *reviews/17/comments*page=1*) printf '%s\n' '[{"id":91,"body":"finding","path":"main.go","position":12,"original_position":0,"pull_request_review_id":17}]' ;;
  *reviews/17/comments*) printf '%s\n' '[]' ;;
  *reviews*page=1*) printf '%s\n' '[{"id":17}]' ;;
  *) printf '%s\n' '[]' ;;
esac
`
	if err := os.WriteFile(curl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "owner", "repo", "7")
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"PUMP19_API_BASE=http://forge.invalid",
		"PUMP19_FORGE_TOKEN=token",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list-review-comments failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `"id": 91`) || !strings.Contains(string(out), `"new_position": 12`) || !strings.Contains(string(out), `"review_id": 17`) {
		t.Fatalf("normalised review comment missing identity or anchor:\n%s", out)
	}
}

func TestForgejoGetStatusesMatchesCapturedForgejo14Shape(t *testing.T) {
	out := runForgejoScriptWithFixture(t, "get-statuses", "commit-statuses.json", "owner", "repo", "abcdef")
	if strings.Contains(out, "created_unix") {
		t.Fatalf("normalised statuses invented created_unix:\n%s", out)
	}
	var statuses []Status
	if err := json.Unmarshal([]byte(out), &statuses); err != nil {
		t.Fatal(err)
	}
	if len(statuses) < 2 || statuses[0].ID <= statuses[1].ID || statuses[1].ID <= 0 {
		t.Fatalf("normalised statuses did not preserve descending positive ids: %#v", statuses)
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

func runForgejoScriptWithFixture(t *testing.T, name, fixture string, args ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "forgejo14", fixture))
	if err != nil {
		t.Fatal(err)
	}
	return runForgejoScript(t, name, string(data), args...)
}

func runForgejoWriteScript(t *testing.T, name, response string, args ...string) string {
	t.Helper()
	fakeBin := t.TempDir()
	capture := filepath.Join(fakeBin, "capture")
	curl := filepath.Join(fakeBin, "curl")
	script := "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >\"$PUMP19_TEST_CAPTURE\"\nprintf '%s\\n' '" + response + "'\n"
	if err := os.WriteFile(curl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "scripts", "adaptations", "forgejo", name)
	cmd := exec.Command(path, args...)
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"PUMP19_API_BASE=http://forge.invalid",
		"PUMP19_FORGE_TOKEN=token",
		"PUMP19_TEST_CAPTURE="+capture,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", name, err, out)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func runForgejoAssignIfMissing(t *testing.T, response string) string {
	t.Helper()
	fakeBin := t.TempDir()
	capture := filepath.Join(fakeBin, "capture")
	responseFile := filepath.Join(fakeBin, "response.json")
	if err := os.WriteFile(responseFile, []byte(response+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	curl := filepath.Join(fakeBin, "curl")
	script := `#!/usr/bin/env sh
set -eu
printf '%s\n' "$*" >>"$PUMP19_TEST_CAPTURE"
method=
while [ "$#" -gt 0 ]; do
  if [ "$1" = -X ]; then
    method="$2"
    shift 2
    continue
  fi
  shift
done
if [ "$method" = GET ]; then
  cat "$PUMP19_TEST_RESPONSE"
else
  printf '{}\n'
fi
`
	if err := os.WriteFile(curl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "assign-if-missing")
	cmd := exec.Command(path, "owner", "repo", "7", "Minos")
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"PUMP19_API_BASE=http://forge.invalid",
		"PUMP19_FORGE_TOKEN=token",
		"PUMP19_TEST_CAPTURE="+capture,
		"PUMP19_TEST_RESPONSE="+responseFile,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("assign-if-missing failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
