package shell

import "fmt"

type RunKind string

const (
	RunReview RunKind = "review"
	RunFix    RunKind = "fix"
	RunFinish RunKind = "finish"
)

var runKinds = []RunKind{RunReview, RunFix, RunFinish}

const (
	LabelReviewing        = "Reviewing"
	LabelFixing           = "Fixing"
	LabelFinishing        = "Finishing"
	LabelConverged        = "Converged"
	LabelStandingFindings = "Standing Findings"
	LabelPartialCoverage  = "Partial Coverage"
	LabelReady            = "Ready"
)

func InFlightLabel(kind RunKind) (string, error) {
	switch kind {
	case RunReview:
		return LabelReviewing, nil
	case RunFix:
		return LabelFixing, nil
	case RunFinish:
		return LabelFinishing, nil
	default:
		return "", fmt.Errorf("unknown run kind %q", kind)
	}
}

func StatusContext(kind RunKind) (string, error) {
	switch kind {
	case RunReview:
		return "pump19/review", nil
	case RunFix:
		return "pump19/fix", nil
	case RunFinish:
		return "pump19/finish", nil
	default:
		return "", fmt.Errorf("unknown run kind %q", kind)
	}
}

func ParseRunKind(value string) (RunKind, error) {
	switch RunKind(value) {
	case RunReview, RunFix, RunFinish:
		return RunKind(value), nil
	default:
		return "", fmt.Errorf("unknown run kind %q", value)
	}
}
