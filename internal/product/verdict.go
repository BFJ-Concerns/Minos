package product

import "fmt"

// ReviewResult is the decision-bearing outcome of the final assembled review.
type ReviewResult struct{ name string }

var (
	// ReviewConverged means the complete, bar-passed review has no material findings.
	reviewConverged = ReviewResult{name: "converged"}
	// ReviewHasMaterialFindings means verified findings meet the configured material threshold.
	reviewHasMaterialFindings = ReviewResult{name: "material-findings"}
	// ReviewIncomplete means coverage is partial or a bar critique remains unresolved.
	reviewIncomplete = ReviewResult{name: "incomplete"}
)

// Verdict is the forge-neutral meaning of a submitted review verdict.
type Verdict struct{ name string }

var (
	approve        = Verdict{name: "approve"}
	requestChanges = Verdict{name: "request-changes"}
	comment        = Verdict{name: "comment"}
)

func ReviewConverged() ReviewResult           { return reviewConverged }
func ReviewHasMaterialFindings() ReviewResult { return reviewHasMaterialFindings }
func ReviewIncomplete() ReviewResult          { return reviewIncomplete }
func Approve() Verdict                        { return approve }
func RequestChanges() Verdict                 { return requestChanges }
func Comment() Verdict                        { return comment }

func (verdict Verdict) Name() string { return verdict.name }

func (verdict Verdict) Valid() bool {
	return verdict == approve || verdict == requestChanges || verdict == comment
}

// VerdictFor applies the commission's fixed mapping. Forge adapters may
// translate the returned meaning into native spelling, but may not reinterpret it.
func VerdictFor(result ReviewResult) (Verdict, error) {
	switch result {
	case reviewConverged:
		return approve, nil
	case reviewHasMaterialFindings:
		return requestChanges, nil
	case reviewIncomplete:
		return comment, nil
	default:
		return Verdict{}, fmt.Errorf("unknown review result")
	}
}
