package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

// requestedReviewComment is the finding location workflows know before the
// Forgejo-specific review boundary decides whether that line can be inline.
type requestedReviewComment struct {
	Path string `json:"path"`
	Line int64  `json:"line"`
	// EndLine is the last line a finding about a range concerns. Zero means
	// the finding concerns Line alone.
	EndLine int64  `json:"end_line,omitempty"`
	Body    string `json:"body"`
}

func validRequestedReviewComment(comment requestedReviewComment) bool {
	return comment.Path != "" && comment.Body != "" && comment.Line > 0 &&
		(comment.EndLine == 0 || comment.EndLine >= comment.Line)
}

// extraLines is the count Forgejo's review-comment schema wants: the lines
// after the anchored one that the comment covers.
func (comment requestedReviewComment) extraLines() int64 {
	if comment.EndLine <= comment.Line {
		return 0
	}
	return comment.EndLine - comment.Line
}

// location names the lines a finding concerns for prose that has to say so.
func (comment requestedReviewComment) location() string {
	if comment.EndLine > comment.Line {
		return fmt.Sprintf("lines %d-%d", comment.Line, comment.EndLine)
	}
	return fmt.Sprintf("line %d", comment.Line)
}

// anchoringCapabilities are the anchoring freedoms an adaptation declares its
// forge can carry, read from `capabilities.json` beside the adaptation
// scripts. An adaptation that declares nothing keeps the strict geometry: a
// finding it cannot place inside a diff hunk folds into the review body.
type anchoringCapabilities struct {
	ReviewComments struct {
		OutOfHunkAnchoring bool `json:"out-of-hunk-anchoring"`
	} `json:"review-comments"`
}

func readAnchoringCapabilities(adaptationDir string) anchoringCapabilities {
	var declared anchoringCapabilities
	if adaptationDir == "" {
		return declared
	}
	data, err := os.ReadFile(filepath.Join(adaptationDir, "capabilities.json"))
	if err != nil {
		return declared
	}
	if err := json.Unmarshal(data, &declared); err != nil {
		return anchoringCapabilities{}
	}
	return declared
}

func readRequestedComments(path string) ([]requestedReviewComment, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var comments []requestedReviewComment
	if err := decoder.Decode(&comments); err != nil {
		return nil, fmt.Errorf("decode review comments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("decode review comments: trailing JSON content")
	}
	for index, comment := range comments {
		if !validRequestedReviewComment(comment) {
			return nil, fmt.Errorf("review comment %d needs path, body, a positive line and an end line no earlier than it", index+1)
		}
	}
	return comments, nil
}

// anchorReviewComments places each finding on the line it concerns, reading
// the merge-base-to-head diff for the geometry: a line the head still carries
// anchors on the new side, a line the change removed anchors on the old side,
// and a finding about a range covers it. How far past the diff hunks a finding
// may still be anchored is the adaptation's declaration, not this module's
// assumption. Geometry failures degrade every remaining finding into the
// review body: anchoring is presentation, while losing the finding would be a
// publication failure.
func anchorReviewComments(
	ctx context.Context,
	workspace string,
	target string,
	head string,
	requested []requestedReviewComment,
	capabilities anchoringCapabilities,
) (inline []forge.ReviewComment, bodyAddendum string, diagnostic string) {
	if len(requested) == 0 {
		return nil, "", ""
	}
	diff, err := mergeBaseDiff(ctx, workspace, target, head)
	if err != nil {
		return nil, foldedReviewComments(requested), err.Error()
	}
	newSide, oldSide := sideIntervals(diff)
	var folded []requestedReviewComment
	for _, comment := range requested {
		anchored := forge.ReviewComment{
			Path:            comment.Path,
			Body:            comment.Body,
			ExtraLinesCount: comment.extraLines(),
		}
		switch {
		case lineInIntervals(comment.Line, newSide[comment.Path]):
			anchored.NewPosition = comment.Line
		case lineInIntervals(comment.Line, oldSide[comment.Path]):
			anchored.OldPosition = comment.Line
		case capabilities.ReviewComments.OutOfHunkAnchoring && pathInDiff(comment.Path, newSide, oldSide):
			anchored.NewPosition = comment.Line
		default:
			folded = append(folded, comment)
			continue
		}
		inline = append(inline, anchored)
	}
	return inline, foldedReviewComments(folded), ""
}

// pathInDiff reports whether the change touched the file at all. A finding
// about an untouched file has no anchor on this pull request under any
// capability, so it folds rather than landing on a file the author did not
// change.
func pathInDiff(path string, newSide, oldSide map[string][][2]int64) bool {
	_, changedHead := newSide[path]
	_, changedBase := oldSide[path]
	return changedHead || changedBase
}

func mergeBaseDiff(ctx context.Context, workspace, target, head string) ([]byte, error) {
	if workspace == "" {
		return nil, fmt.Errorf("anchoring unavailable: MINOS_WORKSPACE is not set")
	}
	mergeBase, err := reviewGitOutput(ctx, workspace, "merge-base", target, head)
	if err != nil {
		return nil, fmt.Errorf("anchoring unavailable: determine merge base: %w", err)
	}
	diff, err := reviewGitOutput(
		ctx,
		workspace,
		"diff",
		"--no-ext-diff",
		"--no-color",
		"--unified=3",
		"-M",
		strings.TrimSpace(string(mergeBase)),
		head,
	)
	if err != nil {
		return nil, fmt.Errorf("anchoring unavailable: read pull-request diff: %w", err)
	}
	return diff, nil
}

func reviewGitOutput(ctx context.Context, workspace string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", workspace}, args...)
	output, err := exec.CommandContext(ctx, "git", commandArgs...).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, detail)
	}
	return output, nil
}

var hunkHeader = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@`)

// newSideIntervals returns the new-file lines present in each diff hunk.
// Forgejo accepts file line numbers, then decides inline membership from this
// same hunk geometry; it may independently rewrite the stored blame position.
func newSideIntervals(diff []byte) map[string][][2]int64 {
	newSide, _ := sideIntervals(diff)
	return newSide
}

// sideIntervals returns the lines each diff hunk covers on both sides of the
// change: the new-file lines the head still carries, keyed by the head path,
// and the old-file lines the change rewrote or removed, keyed by the base
// path. A finding about deleted code has no new-side line, so the old side is
// the only anchor it can have.
func sideIntervals(diff []byte) (newSide, oldSide map[string][][2]int64) {
	newSide = make(map[string][][2]int64)
	oldSide = make(map[string][][2]int64)
	currentNewPath, currentOldPath := "", ""
	lines := bytes.Split(diff, []byte{'\n'})
	for index, rawLine := range lines {
		line := string(rawLine)
		if index > 0 &&
			strings.HasPrefix(string(lines[index-1]), "--- ") &&
			strings.HasPrefix(line, "+++ ") {
			currentOldPath = diffSidePath(strings.TrimPrefix(string(lines[index-1]), "--- "), "a/")
			currentNewPath = diffSidePath(strings.TrimPrefix(line, "+++ "), "b/")
			continue
		}
		if currentNewPath == "" && currentOldPath == "" {
			continue
		}
		match := hunkHeader.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if currentOldPath != "" {
			appendInterval(oldSide, currentOldPath, match[1], match[2])
		}
		if currentNewPath != "" {
			appendInterval(newSide, currentNewPath, match[3], match[4])
		}
	}
	return newSide, oldSide
}

func appendInterval(intervals map[string][][2]int64, path, rawStart, rawCount string) {
	start, err := strconv.ParseInt(rawStart, 10, 64)
	if err != nil {
		return
	}
	count := int64(1)
	if rawCount != "" {
		count, err = strconv.ParseInt(rawCount, 10, 64)
		if err != nil {
			return
		}
	}
	if count > 0 {
		intervals[path] = append(intervals[path], [2]int64{start, start + count - 1})
	}
}

func diffSidePath(label, prefix string) string {
	if label == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(label, `"`) {
		unquoted, err := strconv.Unquote(label)
		if err != nil {
			return ""
		}
		label = unquoted
	}
	return strings.TrimPrefix(label, prefix)
}

func lineInIntervals(line int64, intervals [][2]int64) bool {
	for _, interval := range intervals {
		if line >= interval[0] && line <= interval[1] {
			return true
		}
	}
	return false
}

func foldedReviewComments(comments []requestedReviewComment) string {
	if len(comments) == 0 {
		return ""
	}
	var addendum strings.Builder
	addendum.WriteString("\n\n---\n\nFindings that could not be anchored inline:\n")
	for _, comment := range comments {
		fmt.Fprintf(&addendum, "\n#### `%s` %s\n\n%s\n", comment.Path, comment.location(), comment.Body)
	}
	return addendum.String()
}
