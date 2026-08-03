package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"bfj/minos/internal/forge"
)

// requestedReviewComment is the finding location workflows know before the
// Forgejo-specific review boundary decides whether that line can be inline.
type requestedReviewComment struct {
	Path string `json:"path"`
	Line int64  `json:"line"`
	Body string `json:"body"`
}

func validRequestedReviewComment(comment requestedReviewComment) bool {
	return comment.Path != "" && comment.Body != "" && comment.Line > 0
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
			return nil, fmt.Errorf("review comment %d needs path, body and a positive line", index+1)
		}
	}
	return comments, nil
}

// anchorReviewComments applies Forgejo's merge-base-to-head hunk geometry.
// Geometry failures degrade every finding into the review body: anchoring is
// presentation, while losing the finding would be a publication failure.
func anchorReviewComments(
	ctx context.Context,
	workspace string,
	target string,
	head string,
	requested []requestedReviewComment,
) (inline []forge.ReviewComment, bodyAddendum string, diagnostic string) {
	if len(requested) == 0 {
		return nil, "", ""
	}
	diff, err := mergeBaseDiff(ctx, workspace, target, head)
	if err != nil {
		return nil, foldedReviewComments(requested), err.Error()
	}
	intervals := newSideIntervals(diff)
	var folded []requestedReviewComment
	for _, comment := range requested {
		if lineInIntervals(comment.Line, intervals[comment.Path]) {
			inline = append(inline, forge.ReviewComment{
				Path:        comment.Path,
				Body:        comment.Body,
				NewPosition: comment.Line,
			})
			continue
		}
		folded = append(folded, comment)
	}
	return inline, foldedReviewComments(folded), ""
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

var hunkHeader = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)(?:,([0-9]+))? @@`)

// newSideIntervals returns the new-file lines present in each diff hunk.
// Forgejo accepts file line numbers, then decides inline membership from this
// same hunk geometry; it may independently rewrite the stored blame position.
func newSideIntervals(diff []byte) map[string][][2]int64 {
	intervals := make(map[string][][2]int64)
	currentPath := ""
	lines := bytes.Split(diff, []byte{'\n'})
	for index, rawLine := range lines {
		line := string(rawLine)
		if index > 0 &&
			strings.HasPrefix(string(lines[index-1]), "--- ") &&
			strings.HasPrefix(line, "+++ ") {
			currentPath = diffNewPath(strings.TrimPrefix(line, "+++ "))
			continue
		}
		if currentPath == "" {
			continue
		}
		match := hunkHeader.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		start, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			continue
		}
		count := int64(1)
		if match[2] != "" {
			count, err = strconv.ParseInt(match[2], 10, 64)
			if err != nil {
				continue
			}
		}
		if count > 0 {
			intervals[currentPath] = append(intervals[currentPath], [2]int64{start, start + count - 1})
		}
	}
	return intervals
}

func diffNewPath(label string) string {
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
	return strings.TrimPrefix(label, "b/")
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
		fmt.Fprintf(&addendum, "\n#### `%s` line %d\n\n%s\n", comment.Path, comment.Line, comment.Body)
	}
	return addendum.String()
}
