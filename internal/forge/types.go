// Package forge defines the small behavioural protocol Minos requires from a
// forge. Forge-specific scripts normalise wire formats; this package owns the
// safety vocabulary consumed by reconciliation and the accountable lead.
package forge

import (
	"context"
	"io"
)

const (
	ForgejoProvider    = "forgejo"
	OwnedStatusContext = "Minos"
)

type Repository struct {
	Owner string
	Name  string
}

type Guard struct {
	Repository  Repository
	PullRequest int64
	HeadSHA     string
	TargetSHA   string
}

type StatusState string

const (
	StatusAbsent    StatusState = "absent"
	StatusQueued    StatusState = "queued"
	StatusPending   StatusState = "pending"
	StatusSuccess   StatusState = "success"
	StatusFailure   StatusState = "failure"
	StatusError     StatusState = "error"
	StatusCancelled StatusState = "cancelled"
	StatusTimeout   StatusState = "timeout"
	StatusNeutral   StatusState = "neutral"
	StatusSkipped   StatusState = "skipped"
)

type Status struct {
	ID                  int64       `json:"id"`
	Provider            string      `json:"provider"`
	Context             string      `json:"context"`
	State               StatusState `json:"state"`
	Creator             string      `json:"creator"`
	Description         string      `json:"description"`
	TargetURL           string      `json:"target_url"`
	ProtectionSatisfied bool        `json:"protection_satisfied"`
}

type Review struct {
	ID       int64  `json:"id"`
	State    string `json:"state"`
	CommitID string `json:"commit_id"`
	Body     string `json:"body"`
	User     string `json:"user"`
}

type IssueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User string `json:"user"`
}

type Dependency struct {
	Repository string `json:"repository"`
	Number     int64  `json:"number"`
}

type Snapshot struct {
	AuthenticatedUser     string          `json:"authenticated_user"`
	Repository            string          `json:"repository"`
	PullRequest           int64           `json:"pull_request"`
	State                 string          `json:"state"`
	Merged                bool            `json:"merged"`
	Draft                 bool            `json:"draft"`
	Author                string          `json:"author"`
	Mergeable             bool            `json:"mergeable"`
	HeadSHA               string          `json:"head_sha"`
	HeadBranch            string          `json:"head_branch"`
	HeadRepository        string          `json:"head_repository"`
	TargetSHA             string          `json:"target_sha"`
	TargetBranch          string          `json:"target_branch"`
	TargetRepository      string          `json:"target_repository"`
	DefaultBranch         string          `json:"default_branch"`
	SourceProtected       bool            `json:"source_protected"`
	CanMerge              bool            `json:"can_merge"`
	TargetSyncMethod      string          `json:"target_sync_method"`
	RequiredChecks        []CheckIdentity `json:"required_checks"`
	FailedChecks          []CheckIdentity `json:"failed_checks"`
	Statuses              []Status        `json:"statuses"`
	Labels                []string        `json:"labels"`
	Reviews               []Review        `json:"reviews"`
	DependenciesAvailable bool            `json:"dependencies_available"`
	DependencyError       string          `json:"dependency_error,omitempty"`
	OpenDependencies      []Dependency    `json:"open_dependencies"`
	AllowedMergeMethods   []MergeMethod   `json:"allowed_merge_methods"`

	CheckDecision CheckDecision `json:"check_decision"`
}

type MergeMethod string

const (
	MergeMethodMerge       MergeMethod = "merge"
	MergeMethodRebase      MergeMethod = "rebase"
	MergeMethodRebaseMerge MergeMethod = "rebase-merge"
	MergeMethodSquash      MergeMethod = "squash"
	MergeMethodFastForward MergeMethod = "fast-forward-only"
)

type ReviewVerdict string

const (
	ReviewApprove        ReviewVerdict = "APPROVE"
	ReviewRequestChanges ReviewVerdict = "REQUEST_CHANGES"
	ReviewVerdictComment ReviewVerdict = "COMMENT"
)

type ReviewComment struct {
	Path        string `json:"path"`
	Body        string `json:"body"`
	NewPosition int64  `json:"new_position"`
	OldPosition int64  `json:"old_position"`
}

type reviewWritePayload struct {
	State    ReviewVerdict   `json:"state"`
	Body     string          `json:"body"`
	Comments []ReviewComment `json:"comments"`
}

type CheckIdentity struct {
	Provider string `json:"provider"`
	Context  string `json:"context"`
}

type CheckDecision string

const (
	ChecksPass    CheckDecision = "pass"
	ChecksPending CheckDecision = "pending"
	ChecksFail    CheckDecision = "fail"
)

type WriteOutcome string

const (
	WriteApplied   WriteOutcome = "applied"
	WriteRejected  WriteOutcome = "rejected"
	WriteRetryable WriteOutcome = "retryable"
	WriteUncertain WriteOutcome = "uncertain"
)

type WriteResult struct {
	Outcome WriteOutcome `json:"outcome"`
	Reason  string       `json:"reason,omitempty"`
	SHA     string       `json:"sha,omitempty"`
}

type RunRequest struct {
	Operation string
	Arguments []string
	Stdin     io.Reader
	Env       map[string]string
}

type Runner interface {
	Run(context.Context, RunRequest) ([]byte, error)
}
