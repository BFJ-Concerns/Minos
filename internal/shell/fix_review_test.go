package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAndRenderFixReview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fix-review.json")
	contents := `{
  "comments": [
    {"body":"First repair.","line":73,"path":"src/first.rs"},
    {"path":"src/second.rs","line":9,"body":"Second repair."}
  ],
  "body": "Implemented repairs.\n",
  "verdict": "comment"
}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	review, err := readFixReview(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "Implemented repairs.\n\n- `src/first.rs` line 73: First repair.\n\n- `src/second.rs` line 9: Second repair."
	if got := renderFixReview(review); got != want {
		t.Fatalf("renderFixReview() = %q, want %q", got, want)
	}
}

func TestReadFixReviewRejectsMalformedComment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fix-review.json")
	if err := os.WriteFile(path, []byte(`{"body":"Repairs","comments":[{"path":"a.go","line":0,"body":"Fixed."}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := readFixReview(path)
	if err == nil || !strings.Contains(err.Error(), "positive line") {
		t.Fatalf("readFixReview() error = %v, want positive-line validation", err)
	}
}
