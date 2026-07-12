package shell

import "fmt"

type RunKind string

const (
	RunReview RunKind = "review"
	RunFix    RunKind = "fix"
	RunFinish RunKind = "finish"
	RunFlaky  RunKind = "flaky"
)

var runKinds = []RunKind{RunReview, RunFix, RunFinish, RunFlaky}

const (
	LabelReviewing        = "Reviewing"
	LabelFixing           = "Fixing"
	LabelFinishing        = "Finishing"
	LabelConverged        = "Converged"
	LabelStandingFindings = "Standing Findings"
	LabelPartialCoverage  = "Partial Coverage"
	LabelReady            = "Ready"
	LabelFlakyTests       = "Flaky Tests"
	LabelRepairingFlaky   = "Repairing Flaky Tests"
)

func InFlightLabel(kind RunKind) (string, error) {
	switch kind {
	case RunReview:
		return LabelReviewing, nil
	case RunFix:
		return LabelFixing, nil
	case RunFinish:
		return LabelFinishing, nil
	case RunFlaky:
		// Flaky Tests is the standing safety condition. Keeping the run-owned
		// claim separate prevents crash recovery from clearing the pause itself.
		return LabelRepairingFlaky, nil
	default:
		return "", fmt.Errorf("unknown run kind %q", kind)
	}
}

func StatusContext(kind RunKind) (string, error) {
	switch kind {
	case RunReview:
		return "minos/review", nil
	case RunFix:
		return "minos/fix", nil
	case RunFinish:
		return "minos/finish", nil
	case RunFlaky:
		return "minos/flaky", nil
	default:
		return "", fmt.Errorf("unknown run kind %q", kind)
	}
}

func ParseRunKind(value string) (RunKind, error) {
	switch RunKind(value) {
	case RunReview, RunFix, RunFinish, RunFlaky:
		return RunKind(value), nil
	default:
		return "", fmt.Errorf("unknown run kind %q", value)
	}
}
