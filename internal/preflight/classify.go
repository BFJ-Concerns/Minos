package preflight

import "strings"

type FailureKind string

const (
	FailureUnknown         FailureKind = "unknown"
	FailureStaleOAuth      FailureKind = "stale-oauth"
	FailureQuota           FailureKind = "quota-exhausted"
	FailureAccountDisabled FailureKind = "account-disabled"
)

type BackendFailure struct {
	StatusCode int
	Message    string
	Immediate  *bool
	CostUSD    *float64
}

type Classification struct {
	Kind     FailureKind
	Recovery string
}

func ClassifyBackendFailure(failure BackendFailure) Classification {
	message := strings.ToLower(failure.Message)
	disabledShape := failure.StatusCode == 403 && strings.Contains(message, "organization has disabled claude subscription access")
	// The shared-subscription stale-refresh incident is uniquely characterised
	// by this misleading 403 arriving before any work or cost is incurred.
	if disabledShape && failure.Immediate != nil && *failure.Immediate && failure.CostUSD != nil && *failure.CostUSD == 0 {
		return Classification{
			Kind:     FailureStaleOAuth,
			Recovery: "re-authenticate Claude as the deployment user, then copy the refreshed credentials to the deployment account if authentication occurred elsewhere",
		}
	}
	if failure.StatusCode == 429 || strings.Contains(message, "quota") || strings.Contains(message, "usage limit") {
		return Classification{Kind: FailureQuota, Recovery: "restore account quota or wait for the usage window to reset"}
	}
	if disabledShape {
		return Classification{Kind: FailureUnknown, Recovery: "re-run credential diagnostics with observable timing and cost evidence, then inspect the deployment log"}
	}
	if failure.StatusCode == 403 && strings.Contains(message, "disabled") {
		return Classification{Kind: FailureAccountDisabled, Recovery: "restore Claude subscription access with the account administrator"}
	}
	return Classification{Kind: FailureUnknown, Recovery: "inspect the deployment diagnostic log"}
}
