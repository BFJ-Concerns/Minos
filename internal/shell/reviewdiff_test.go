package shell

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reviewDiffFixture = `diff --git a/unchanged.go b/unchanged.go
index 1111111..2222222 100644
--- a/unchanged.go
+++ b/unchanged.go
@@ -8,4 +8,5 @@
 context
-old
+new
+added
++ looks like a header but is content
 context
diff --git a/new file.txt b/new file.txt
new file mode 100644
--- /dev/null
+++ b/new file.txt
@@ -0,0 +1,2 @@
+first
+second
`

func TestReviewChangedLineAndAnchorUseHeadLineNumbers(t *testing.T) {
	diff := filepath.Join(t.TempDir(), "diff.patch")
	if err := os.WriteFile(diff, []byte(reviewDiffFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"9", "10", "11"} {
		var out bytes.Buffer
		if err := ReviewCommand([]string{"changed-line", diff, "unchanged.go", line}, strings.NewReader(""), &out); err != nil {
			t.Fatalf("changed line %s rejected: %v", line, err)
		}
	}
	if err := ReviewCommand([]string{"changed-line", diff, "unchanged.go", "8"}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("context line was accepted as changed")
	}
	var out bytes.Buffer
	if err := ReviewCommand([]string{"anchor", diff, "new file.txt", "2"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	var anchor ReviewAnchor
	if err := json.Unmarshal(out.Bytes(), &anchor); err != nil {
		t.Fatal(err)
	}
	if anchor.Path != "new file.txt" || anchor.NewPosition != 2 || anchor.OldPosition != 0 {
		t.Fatalf("unexpected anchor: %#v", anchor)
	}
}

func TestDiffEvidenceTracksRemovedBaseLinesSeparately(t *testing.T) {
	diff := filepath.Join(t.TempDir(), "diff.patch")
	if err := os.WriteFile(diff, []byte(reviewDiffFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := diffChangedEvidenceLines(diff)
	if err != nil {
		t.Fatal(err)
	}
	if !changed[evidenceLine{path: "unchanged.go", line: 9, side: baseEvidenceLine}] {
		t.Fatal("removed base line was not accounted")
	}
	if !changed[evidenceLine{path: "unchanged.go", line: 10, side: headEvidenceLine}] || changed[evidenceLine{path: "unchanged.go", line: 10, side: baseEvidenceLine}] {
		t.Fatal("added head line leaked into base-side evidence accounting")
	}
}

func TestReviewCommandsRejectPathsOutsideRepository(t *testing.T) {
	diff := filepath.Join(t.TempDir(), "diff.patch")
	if err := os.WriteFile(diff, []byte(reviewDiffFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReviewCommand([]string{"anchor", diff, "../unchanged.go", "9"}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("parent path was accepted")
	}
}

func TestReviewDedupeSplitsUpdatesFromNewFindings(t *testing.T) {
	input := `{
  "existing": [
    {"id": 91, "body": "Existing finding\n\n<!-- Minos: finding=F-7KQ3 head=abc priority=P1 -->"},
    {"id": 92, "body": "Human comment"}
  ],
  "candidates": [
    {"finding": "F-7KQ3", "path": "a.go", "line": 4, "priority": "P1", "body": "Still present"},
    {"path": "b.go", "line": 8, "priority": "P2", "body": "New issue"}
  ]
}`
	var out bytes.Buffer
	if err := ReviewCommand([]string{"dedupe"}, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	var got reviewDedupeOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Updates) != 1 || got.Updates[0].CommentID != 91 || len(got.New) != 1 {
		t.Fatalf("unexpected dedupe result: %#v", got)
	}
}

func TestReviewDedupeRejectsUnknownRecurringHandle(t *testing.T) {
	input := `{"existing":[],"candidates":[{"finding":"F-7KQ3","path":"a.go","line":4,"priority":"P1","body":"x"}]}`
	if err := ReviewCommand([]string{"dedupe"}, strings.NewReader(input), &bytes.Buffer{}); err == nil {
		t.Fatal("unknown recurring handle was accepted")
	}
}

func TestReviewDedupeRejectsIncompleteCandidate(t *testing.T) {
	input := `{"existing":[],"candidates":[{"path":"a.go","line":0,"priority":"urgent","body":"x"}]}`
	if err := ReviewCommand([]string{"dedupe"}, strings.NewReader(input), &bytes.Buffer{}); err == nil {
		t.Fatal("incomplete candidate was accepted")
	}
}

func TestMarkerCommandRejectsDuplicateKeys(t *testing.T) {
	if err := MarkerCommand([]string{"format", "run=review", "run=fix"}, &bytes.Buffer{}); err == nil {
		t.Fatal("duplicate marker key was accepted")
	}
}
