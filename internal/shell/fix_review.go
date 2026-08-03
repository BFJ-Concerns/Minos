package shell

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type fixReview struct {
	Verdict  string                   `json:"verdict"`
	Body     string                   `json:"body"`
	Comments []requestedReviewComment `json:"comments"`
}

func readFixReview(path string) (fixReview, error) {
	file, err := os.Open(path)
	if err != nil {
		return fixReview{}, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var review fixReview
	if err := decoder.Decode(&review); err != nil {
		return fixReview{}, fmt.Errorf("decode fix review: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fixReview{}, fmt.Errorf("decode fix review: trailing JSON content")
	}
	if strings.TrimSpace(review.Body) == "" {
		return fixReview{}, fmt.Errorf("fix review needs a body")
	}
	for index, comment := range review.Comments {
		if !validRequestedReviewComment(comment) {
			return fixReview{}, fmt.Errorf("fix review comment %d needs path, body and a positive line", index+1)
		}
	}
	return review, nil
}

func renderFixReview(review fixReview) string {
	var rendered strings.Builder
	rendered.WriteString(strings.TrimRight(review.Body, "\r\n"))
	for _, comment := range review.Comments {
		fmt.Fprintf(&rendered, "\n\n- `%s` line %d: %s", comment.Path, comment.Line, comment.Body)
	}
	return rendered.String()
}
