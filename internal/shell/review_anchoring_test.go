package shell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewSideIntervalsReadsForgejoHunkGeometry(t *testing.T) {
	diff := []byte(`diff --git a/src/main.go b/src/main.go
--- a/src/main.go
+++ b/src/main.go
@@ -7,3 +7,4 @@
diff --git a/new.txt b/new.txt
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,3 @@
diff --git a/old.txt b/renamed.txt
similarity index 80%
rename from old.txt
rename to renamed.txt
--- a/old.txt
+++ b/renamed.txt
@@ -20 +22 @@
diff --git "a/tab\tname.txt" "b/tab\tname.txt"
--- "a/tab\tname.txt"
+++ "b/tab\tname.txt"
@@ -1 +1 @@
diff --git a/deleted.txt b/deleted.txt
--- a/deleted.txt
+++ /dev/null
@@ -1,2 +0,0 @@
`)

	got := newSideIntervals(diff)
	assertIntervals(t, got["src/main.go"], [][2]int64{{7, 10}})
	assertIntervals(t, got["new.txt"], [][2]int64{{1, 3}})
	assertIntervals(t, got["renamed.txt"], [][2]int64{{22, 22}})
	assertIntervals(t, got["tab\tname.txt"], [][2]int64{{1, 1}})
	if _, exists := got["deleted.txt"]; exists {
		t.Fatalf("deleted file unexpectedly has new-side intervals: %#v", got["deleted.txt"])
	}
}

func TestNewSideIntervalsDistinguishesAddedContentFromHeaders(t *testing.T) {
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-q")
	runGit(t, workspace, "config", "user.name", "Minos Test")
	runGit(t, workspace, "config", "user.email", "minos@example.invalid")

	lines := make([]string, 20)
	for index := range lines {
		lines[index] = "unchanged"
	}
	writeReviewFixtureFile(t, workspace, "src/content.txt", strings.Join(lines, "\n")+"\n")
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-q", "-m", "target")
	target := strings.TrimSpace(gitOutput(t, workspace, "rev-parse", "HEAD"))

	lines = append(lines[:2], append([]string{"++ payload"}, lines[2:]...)...)
	lines[15] = "changed later"
	writeReviewFixtureFile(t, workspace, "src/content.txt", strings.Join(lines, "\n")+"\n")
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-q", "-m", "head")
	head := strings.TrimSpace(gitOutput(t, workspace, "rev-parse", "HEAD"))

	diff, err := mergeBaseDiff(t.Context(), workspace, target, head)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(diff), "\n+++ payload\n") {
		t.Fatalf("real diff did not expose the ambiguous content line:\n%s", diff)
	}
	intervals := newSideIntervals(diff)
	if len(intervals["src/content.txt"]) != 2 {
		t.Fatalf("real path intervals = %#v, all intervals = %#v", intervals["src/content.txt"], intervals)
	}
	if _, exists := intervals["payload"]; exists {
		t.Fatalf("added content became a file path: %#v", intervals)
	}
}

func TestAnchorReviewCommentsSplitsInlineCommentsAndRendersTruthfulFallback(t *testing.T) {
	workspace, target, head := anchoredReviewRepository(t)
	requested := []requestedReviewComment{
		{Path: "src/code.txt", Line: 10, Body: "Anchored concern."},
		{Path: "src/code.txt", Line: 1, Body: "Untouched concern."},
		{Path: "missing.txt", Line: 4, Body: "Missing-path concern."},
	}

	inline, addendum, diagnostic := anchorReviewComments(
		context.Background(), workspace, target, head, requested,
	)
	if diagnostic != "" {
		t.Fatalf("unexpected diagnostic: %s", diagnostic)
	}
	if len(inline) != 1 || inline[0].Path != "src/code.txt" ||
		inline[0].NewPosition != 10 || inline[0].Body != "Anchored concern." {
		t.Fatalf("unexpected inline comments: %#v", inline)
	}
	want := "\n\n---\n\nFindings that could not be anchored inline:\n" +
		"\n#### `src/code.txt` line 1\n\nUntouched concern.\n" +
		"\n#### `missing.txt` line 4\n\nMissing-path concern.\n"
	if addendum != want {
		t.Fatalf("fallback addendum mismatch\nwant: %q\n got: %q", want, addendum)
	}
}

func TestAnchorReviewCommentsFoldsAllWhenGeometryIsUnavailable(t *testing.T) {
	requested := []requestedReviewComment{{Path: "src/code.txt", Line: 10, Body: "Concern."}}
	inline, addendum, diagnostic := anchorReviewComments(
		context.Background(), "", "target", "head", requested,
	)
	if len(inline) != 0 {
		t.Fatalf("unexpected inline comments: %#v", inline)
	}
	if !strings.Contains(addendum, "`src/code.txt` line 10") ||
		!strings.Contains(addendum, "Concern.") ||
		!strings.Contains(addendum, "could not be anchored inline") {
		t.Fatalf("fallback lost author-facing detail: %q", addendum)
	}
	if !strings.Contains(diagnostic, "MINOS_WORKSPACE is not set") {
		t.Fatalf("unexpected diagnostic: %q", diagnostic)
	}
}

func TestAnchorReviewCommentsHandlesAdversarialDiffSurfaces(t *testing.T) {
	workspace, target, head := anchoredReviewRepository(t)
	requested := []requestedReviewComment{
		{Path: "src/code.txt", Line: 10, Body: "inside"},
		{Path: "src/code.txt", Line: 1, Body: "outside"},
		{Path: "new.txt", Line: 2, Body: "new"},
		{Path: "deleted.txt", Line: 1, Body: "deleted"},
		{Path: "binary.dat", Line: 1, Body: "binary"},
		{Path: "untouched.txt", Line: 1, Body: "untouched"},
		{Path: "does-not-exist.txt", Line: 1, Body: "missing"},
	}

	inline, addendum, diagnostic := anchorReviewComments(
		context.Background(), workspace, target, head, requested,
	)
	if diagnostic != "" {
		t.Fatalf("unexpected diagnostic: %s", diagnostic)
	}
	if len(inline) != 2 || inline[0].Body != "inside" || inline[1].Body != "new" {
		t.Fatalf("unexpected inline split: %#v", inline)
	}
	for _, marker := range []string{"outside", "deleted", "binary", "untouched", "missing"} {
		if !strings.Contains(addendum, marker) {
			t.Fatalf("fallback omitted %q: %q", marker, addendum)
		}
	}

	inline, addendum, diagnostic = anchorReviewComments(
		context.Background(), workspace, head, head, requested[:1],
	)
	if diagnostic != "" || len(inline) != 0 || !strings.Contains(addendum, "inside") {
		t.Fatalf("empty diff did not fold the finding: inline=%#v addendum=%q diagnostic=%q", inline, addendum, diagnostic)
	}
}

func TestReadRequestedCommentsRejectsLegacyWireShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "comments.json")
	if err := os.WriteFile(path, []byte(`[{"path":"src/code.txt","body":"Concern.","new_position":10}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRequestedComments(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("legacy shape was not rejected: %v", err)
	}
}

func anchoredReviewRepository(t *testing.T) (workspace, target, head string) {
	t.Helper()
	workspace = t.TempDir()
	runGit(t, workspace, "init", "-q")
	runGit(t, workspace, "config", "user.name", "Minos Test")
	runGit(t, workspace, "config", "user.email", "minos@example.invalid")

	if err := os.MkdirAll(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := make([]string, 20)
	for index := range lines {
		lines[index] = "unchanged"
	}
	writeReviewFixtureFile(t, workspace, "src/code.txt", strings.Join(lines, "\n")+"\n")
	writeReviewFixtureFile(t, workspace, "deleted.txt", "deleted\n")
	writeReviewFixtureFile(t, workspace, "untouched.txt", "untouched\n")
	if err := os.WriteFile(filepath.Join(workspace, "binary.dat"), []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-q", "-m", "target")
	target = strings.TrimSpace(gitOutput(t, workspace, "rev-parse", "HEAD"))

	lines[9] = "changed"
	writeReviewFixtureFile(t, workspace, "src/code.txt", strings.Join(lines, "\n")+"\n")
	writeReviewFixtureFile(t, workspace, "new.txt", "one\ntwo\nthree\n")
	if err := os.Remove(filepath.Join(workspace, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "binary.dat"), []byte{0, 1, 9, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "-A")
	runGit(t, workspace, "commit", "-q", "-m", "head")
	head = strings.TrimSpace(gitOutput(t, workspace, "rev-parse", "HEAD"))
	return workspace, target, head
}

func writeReviewFixtureFile(t *testing.T, root, path, contents string) {
	t.Helper()
	fullPath := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertIntervals(t *testing.T, got, want [][2]int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("interval count mismatch: got %#v want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("interval mismatch: got %#v want %#v", got, want)
		}
	}
}
