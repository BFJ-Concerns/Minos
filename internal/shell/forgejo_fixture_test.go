package shell

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type webhookFixture struct {
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

func TestForgejo14FixturesNormaliseOccasions(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "forgejo14", "[0-9][0-9][0-9]-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no Forgejo webhook fixtures found")
	}
	for _, path := range files {
		name := filepath.Base(path)
		occasion, ok := fixtureOccasion(name)
		if !ok {
			t.Fatalf("fixture %s has no expected occasion mapping", name)
		}
		t.Run(name, func(t *testing.T) {
			fixture := readFixture(t, name)
			facts := runNormaliseEvent(t, fixture)
			if facts.Occasion != occasion {
				t.Fatalf("occasion = %q, want %q", facts.Occasion, occasion)
			}
			if facts.Owner != "minos-e2e-owner" || facts.Repo != "subject" || facts.PR == "" {
				t.Fatalf("repo facts not normalised: %#v", facts)
			}
			if facts.HeadSHA == "" {
				t.Fatalf("expected head SHA in %#v", facts)
			}
		})
	}
}

func TestForgejo14ReviewFixturesCarryConsolidatedHeaders(t *testing.T) {
	approved := readFixture(t, findFixture(t, "pull_request_approved-reviewed"))
	if approved.Headers["X-Forgejo-Event"] != "pull_request_approved" {
		t.Fatalf("approved event header = %q", approved.Headers["X-Forgejo-Event"])
	}
	if approved.Headers["X-Forgejo-Event-Type"] != "pull_request_review_approved" {
		t.Fatalf("approved event-type header = %q", approved.Headers["X-Forgejo-Event-Type"])
	}
	rejected := readFixture(t, findFixture(t, "pull_request_rejected-reviewed"))
	if rejected.Headers["X-Forgejo-Event"] != "pull_request_rejected" {
		t.Fatalf("rejected event header = %q", rejected.Headers["X-Forgejo-Event"])
	}
	if rejected.Headers["X-Forgejo-Event-Type"] != "pull_request_review_rejected" {
		t.Fatalf("rejected event-type header = %q", rejected.Headers["X-Forgejo-Event-Type"])
	}
}

func TestForgejo14FinishingOperationShapes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "forgejo14", "finishing-operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Source string `json:"source"`
		Merge  struct {
			Method        string         `json:"method"`
			Path          string         `json:"path"`
			Request       map[string]any `json:"request"`
			SuccessStatus int            `json:"success_status"`
		} `json:"merge"`
		LabelRemoval struct {
			Method        string `json:"method"`
			Path          string `json:"path"`
			SuccessStatus int    `json:"success_status"`
		} `json:"label_removal"`
		ReactionRemoval struct {
			Method        string         `json:"method"`
			Path          string         `json:"path"`
			Request       map[string]any `json:"request"`
			SuccessStatus int            `json:"success_status"`
		} `json:"reaction_removal"`
		BranchDeletion struct {
			Method        string `json:"method"`
			Path          string `json:"path"`
			SuccessStatus int    `json:"success_status"`
		} `json:"branch_deletion"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Source != "Forgejo v14.0.0 tagged OpenAPI" ||
		fixture.Merge.Method != http.MethodPost || fixture.Merge.SuccessStatus != http.StatusOK ||
		fixture.Merge.Request["Do"] != "merge" || fixture.Merge.Request["head_commit_id"] != "HEAD" ||
		fixture.LabelRemoval.Method != http.MethodDelete || fixture.LabelRemoval.SuccessStatus != http.StatusNoContent ||
		fixture.ReactionRemoval.Method != http.MethodDelete || fixture.ReactionRemoval.SuccessStatus != http.StatusOK ||
		fixture.ReactionRemoval.Request["content"] != "eyes" ||
		fixture.BranchDeletion.Method != http.MethodDelete || fixture.BranchDeletion.SuccessStatus != http.StatusNoContent {
		t.Fatalf("unexpected finishing fixture: %#v", fixture)
	}
	for name, path := range map[string]string{
		"merge": fixture.Merge.Path, "label": fixture.LabelRemoval.Path,
		"reaction": fixture.ReactionRemoval.Path, "branch": fixture.BranchDeletion.Path,
	} {
		if !strings.HasPrefix(path, "/api/v1/repos/{owner}/{repo}/") {
			t.Fatalf("%s path = %q", name, path)
		}
	}
}

func fixtureOccasion(name string) (string, bool) {
	switch {
	case strings.Contains(name, "pull_request-opened"):
		return "pr-opened", true
	case strings.Contains(name, "pull_request-synchronized"):
		return "pr-synchronized", true
	case strings.Contains(name, "pull_request-label_updated"):
		return "label-updated", true
	case strings.Contains(name, "pull_request-edited"):
		return "pr-edited", true
	case strings.Contains(name, "pull_request_approved-reviewed"):
		return "review-approved", true
	case strings.Contains(name, "pull_request_rejected-reviewed"):
		return "review-rejected", true
	case strings.Contains(name, "issue_comment-created"):
		return "comment-created", true
	default:
		return "", false
	}
}

func findFixture(t *testing.T, suffix string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "forgejo14", "*-"+suffix+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no fixture matching %s", suffix)
	}
	return filepath.Base(files[0])
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
		"MINOS_HEADER_X_FORGEJO_EVENT="+fixture.Headers["X-Forgejo-Event"],
		"MINOS_HEADER_X_GITEA_EVENT="+fixture.Headers["X-Gitea-Event"],
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
