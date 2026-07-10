package shell

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type reviewDedupeInput struct {
	Existing   []existingReviewComment `json:"existing"`
	Candidates []reviewCandidate       `json:"candidates"`
}

type existingReviewComment struct {
	ID          int64  `json:"id"`
	Body        string `json:"body"`
	Path        string `json:"path,omitempty"`
	NewPosition int    `json:"new_position,omitempty"`
	OldPosition int    `json:"old_position,omitempty"`
	ReviewID    int64  `json:"review_id,omitempty"`
}

type reviewCandidate struct {
	Finding   string `json:"finding,omitempty"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Priority  string `json:"priority"`
	Body      string `json:"body"`
	CommentID int64  `json:"comment_id,omitempty"`
}

type reviewDedupeOutput struct {
	Updates []reviewCandidate `json:"updates"`
	New     []reviewCandidate `json:"new"`
}

func dedupeReview(stdin io.Reader, stdout io.Writer) error {
	var input reviewDedupeInput
	decoder := json.NewDecoder(stdin)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return fmt.Errorf("decode dedupe input: %w", err)
	}
	existing := make(map[string]int64)
	for _, comment := range input.Existing {
		marker, ok := trailingMarker(comment.Body)
		if !ok || marker["run"] != "review" || marker["finding"] == "" {
			continue
		}
		handle := marker["finding"]
		if !ValidFindingHandle(handle) {
			return fmt.Errorf("existing comment %d has invalid finding handle %q", comment.ID, handle)
		}
		if _, duplicate := existing[handle]; duplicate {
			return fmt.Errorf("finding handle %s appears on more than one comment", handle)
		}
		existing[handle] = comment.ID
	}
	output := reviewDedupeOutput{Updates: []reviewCandidate{}, New: []reviewCandidate{}}
	seen := make(map[string]bool)
	seenLocations := make(map[string]bool)
	for _, candidate := range input.Candidates {
		if candidate.Path == "" || candidate.Line < 1 || candidate.Body == "" || !validPriority(candidate.Priority) {
			return fmt.Errorf("candidate requires path, positive line, P0-P3 priority, and body")
		}
		location := fmt.Sprintf("%s:%d", candidate.Path, candidate.Line)
		if seenLocations[location] {
			return fmt.Errorf("more than one candidate targets %s", location)
		}
		seenLocations[location] = true
		if candidate.Finding == "" {
			output.New = append(output.New, candidate)
			continue
		}
		if !ValidFindingHandle(candidate.Finding) {
			return fmt.Errorf("candidate has invalid finding handle %q", candidate.Finding)
		}
		if seen[candidate.Finding] {
			return fmt.Errorf("candidate finding handle %s is repeated", candidate.Finding)
		}
		seen[candidate.Finding] = true
		commentID, ok := existing[candidate.Finding]
		if !ok {
			return fmt.Errorf("candidate finding handle %s has no existing comment", candidate.Finding)
		}
		candidate.CommentID = commentID
		output.Updates = append(output.Updates, candidate)
	}
	return json.NewEncoder(stdout).Encode(output)
}

func validPriority(priority string) bool {
	switch priority {
	case "P0", "P1", "P2", "P3":
		return true
	default:
		return false
	}
}

func trailingMarker(body string) (map[string]string, bool) {
	lines := strings.Split(strings.TrimRight(body, "\r\n"), "\n")
	if len(lines) == 0 {
		return nil, false
	}
	values, err := ParseMarker(strings.TrimSuffix(lines[len(lines)-1], "\r"))
	return values, err == nil
}
