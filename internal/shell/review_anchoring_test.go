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
		context.Background(), workspace, target, head, requested, anchoringCapabilities{},
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
		context.Background(), "", "target", "head", requested, permissiveAnchoring(),
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
		context.Background(), workspace, target, head, requested, anchoringCapabilities{},
	)
	if diagnostic != "" {
		t.Fatalf("unexpected diagnostic: %s", diagnostic)
	}
	if len(inline) != 3 || inline[0].Body != "inside" || inline[1].Body != "new" || inline[2].Body != "deleted" {
		t.Fatalf("unexpected inline split: %#v", inline)
	}
	if inline[2].OldPosition != 1 || inline[2].NewPosition != 0 {
		t.Fatalf("deleted-file finding did not anchor on the deletion side: %#v", inline[2])
	}
	for _, marker := range []string{"outside", "binary", "untouched", "missing"} {
		if !strings.Contains(addendum, marker) {
			t.Fatalf("fallback omitted %q: %q", marker, addendum)
		}
	}

	inline, addendum, diagnostic = anchorReviewComments(
		context.Background(), workspace, head, head, requested[:1], anchoringCapabilities{},
	)
	if diagnostic != "" || len(inline) != 0 || !strings.Contains(addendum, "inside") {
		t.Fatalf("empty diff did not fold the finding: inline=%#v addendum=%q diagnostic=%q", inline, addendum, diagnostic)
	}
}

// A finding outside every hunk is anchorable or not according to what the
// serving adaptation declares, so the same finding lands inline against a
// forge that carries it and folds into the body against one that does not.
func TestAnchorReviewCommentsFollowsTheDeclaredOutOfHunkCapability(t *testing.T) {
	workspace, target, head := anchoredReviewRepository(t)
	requested := []requestedReviewComment{
		{Path: "src/code.txt", Line: 1, Body: "Outside-hunk concern."},
		{Path: "untouched.txt", Line: 1, Body: "Untouched-file concern."},
	}

	inline, addendum, diagnostic := anchorReviewComments(
		context.Background(), workspace, target, head, requested, permissiveAnchoring(),
	)
	if diagnostic != "" {
		t.Fatalf("unexpected diagnostic: %s", diagnostic)
	}
	if len(inline) != 1 || inline[0].Path != "src/code.txt" || inline[0].NewPosition != 1 {
		t.Fatalf("declared capability did not anchor the out-of-hunk finding: %#v", inline)
	}
	if !strings.Contains(addendum, "Untouched-file concern.") {
		t.Fatalf("a finding about an untouched file must still fold: %q", addendum)
	}

	inline, addendum, diagnostic = anchorReviewComments(
		context.Background(), workspace, target, head, requested, anchoringCapabilities{},
	)
	if diagnostic != "" || len(inline) != 0 {
		t.Fatalf("undeclared capability anchored anyway: inline=%#v diagnostic=%q", inline, diagnostic)
	}
	for _, marker := range []string{"Outside-hunk concern.", "Untouched-file concern."} {
		if !strings.Contains(addendum, marker) {
			t.Fatalf("fallback omitted %q: %q", marker, addendum)
		}
	}
}

// A finding about several lines covers them, and says so in the fallback
// prose when it cannot be anchored at all.
func TestAnchorReviewCommentsCarryRangesAndNameThemWhenFolded(t *testing.T) {
	workspace, target, head := anchoredReviewRepository(t)
	inline, addendum, diagnostic := anchorReviewComments(
		context.Background(), workspace, target, head, []requestedReviewComment{
			{Path: "src/code.txt", Line: 10, EndLine: 12, Body: "Range concern."},
			{Path: "untouched.txt", Line: 3, EndLine: 5, Body: "Folded range concern."},
		}, anchoringCapabilities{},
	)
	if diagnostic != "" {
		t.Fatalf("unexpected diagnostic: %s", diagnostic)
	}
	if len(inline) != 1 || inline[0].NewPosition != 10 || inline[0].ExtraLinesCount != 2 {
		t.Fatalf("range finding did not cover its lines: %#v", inline)
	}
	if !strings.Contains(addendum, "`untouched.txt` lines 3-5") {
		t.Fatalf("folded range did not name its lines: %q", addendum)
	}
}

// The deployed adaptation is the declaration's own consumer: what
// scripts/adaptations/forgejo ships decides how far Minos anchors against it.
func TestReadAnchoringCapabilitiesReadsTheShippedDeclaration(t *testing.T) {
	declared := readAnchoringCapabilities(filepath.Join("..", "..", "scripts", "adaptations", "forgejo"))
	if !declared.ReviewComments.OutOfHunkAnchoring {
		t.Fatal("the Forgejo adaptation no longer declares out-of-hunk anchoring")
	}
	if readAnchoringCapabilities(t.TempDir()).ReviewComments.OutOfHunkAnchoring {
		t.Fatal("an adaptation declaring nothing was read as permissive")
	}
}

func TestReadRequestedCommentsAcceptsRangesAndRejectsInvertedOnes(t *testing.T) {
	directory := t.TempDir()
	valid := filepath.Join(directory, "range.json")
	if err := os.WriteFile(valid, []byte(`[{"path":"src/code.txt","body":"Concern.","line":10,"end_line":12}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	comments, err := readRequestedComments(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].EndLine != 12 || comments[0].extraLines() != 2 {
		t.Fatalf("range comment = %#v", comments)
	}

	inverted := filepath.Join(directory, "inverted.json")
	if err := os.WriteFile(inverted, []byte(`[{"path":"src/code.txt","body":"Concern.","line":10,"end_line":4}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRequestedComments(inverted); err == nil || !strings.Contains(err.Error(), "end line") {
		t.Fatalf("inverted range was not rejected: %v", err)
	}
}

func permissiveAnchoring() anchoringCapabilities {
	var capabilities anchoringCapabilities
	capabilities.ReviewComments.OutOfHunkAnchoring = true
	return capabilities
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
