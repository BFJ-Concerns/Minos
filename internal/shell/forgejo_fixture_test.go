package shell

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type webhookFixture struct {
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

func TestForgejo14FixturesNormaliseOccasions(t *testing.T) {
	tests := []struct {
		file     string
		occasion string
	}{
		{"001-pull_request-opened.json", "pr-opened"},
		{"011-pull_request-synchronized.json", "pr-synchronized"},
		{"017-issue_comment-created.json", "comment-created"},
		{"018-pull_request-label_updated.json", "label-updated"},
		{"019-pull_request-label_updated.json", "label-updated"},
		{"020-pull_request-edited.json", "pr-edited"},
		{"022-pull_request_approved-reviewed.json", "review-approved"},
		{"023-pull_request_rejected-reviewed.json", "review-rejected"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			fixture := readFixture(t, tt.file)
			facts := runNormaliseEvent(t, fixture)
			if facts.Occasion != tt.occasion {
				t.Fatalf("occasion = %q, want %q", facts.Occasion, tt.occasion)
			}
			if facts.Owner != "pump19" || facts.Repo != "subject" || facts.PR == "" {
				t.Fatalf("repo facts not normalised: %#v", facts)
			}
			if facts.HeadSHA == "" {
				t.Fatalf("expected head SHA in %#v", facts)
			}
		})
	}
}

func TestForgejo14ReviewFixturesCarryConsolidatedHeaders(t *testing.T) {
	approved := readFixture(t, "022-pull_request_approved-reviewed.json")
	if approved.Headers["X-Forgejo-Event"] != "pull_request_approved" {
		t.Fatalf("approved event header = %q", approved.Headers["X-Forgejo-Event"])
	}
	if approved.Headers["X-Forgejo-Event-Type"] != "pull_request_review_approved" {
		t.Fatalf("approved event-type header = %q", approved.Headers["X-Forgejo-Event-Type"])
	}
	rejected := readFixture(t, "023-pull_request_rejected-reviewed.json")
	if rejected.Headers["X-Forgejo-Event"] != "pull_request_rejected" {
		t.Fatalf("rejected event header = %q", rejected.Headers["X-Forgejo-Event"])
	}
	if rejected.Headers["X-Forgejo-Event-Type"] != "pull_request_review_rejected" {
		t.Fatalf("rejected event-type header = %q", rejected.Headers["X-Forgejo-Event-Type"])
	}
}

func readFixture(t *testing.T, name string) webhookFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "forgejo14", name))
	if err != nil {
		t.Fatal(err)
	}
	var fixture webhookFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func runNormaliseEvent(t *testing.T, fixture webhookFixture) Facts {
	t.Helper()
	script := filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "normalise-event")
	cmd := exec.Command(script)
	cmd.Stdin = bytes.NewBufferString(fixture.Body)
	cmd.Env = append(os.Environ(),
		"PUMP19_HEADER_X_FORGEJO_EVENT="+fixture.Headers["X-Forgejo-Event"],
		"PUMP19_HEADER_X_GITEA_EVENT="+fixture.Headers["X-Gitea-Event"],
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("normalise-event failed: %v\n%s", err, out)
	}
	facts, err := ParseFacts(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("ParseFacts failed: %v\n%s", err, out)
	}
	return facts
}
