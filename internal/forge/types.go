// Package forge defines the small behavioural protocol Minos requires from a
// forge. Forge-specific scripts normalise wire formats; this package owns the
// safety vocabulary consumed by reconciliation and the accountable lead.
package forge

import (
	"context"
	"io"
)

const (
	ForgejoProvider = "forgejo"
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

type Dependency struct {
	Repository string `json:"repository"`
	Number     int64  `json:"number"`
}

type Snapshot struct {
	AuthenticatedUser     string       `json:"authenticated_user"`
	Repository            string       `json:"repository"`
	PullRequest           int64        `json:"pull_request"`
	State                 string       `json:"state"`
	Merged                bool         `json:"merged"`
	Draft                 bool         `json:"draft"`
	Author                string       `json:"author"`
	Mergeable             bool         `json:"mergeable"`
	HeadSHA               string       `json:"head_sha"`
	HeadBranch            string       `json:"head_branch"`
	HeadRepository        string       `json:"head_repository"`
	TargetSHA             string       `json:"target_sha"`
	TargetBranch          string       `json:"target_branch"`
	TargetRepository      string       `json:"target_repository"`
	DefaultBranch         string       `json:"default_branch"`
	SourceProtected       bool         `json:"source_protected"`
	CanMerge              bool         `json:"can_merge"`
	Statuses              []Status     `json:"statuses"`
	Labels                []string     `json:"labels"`
	Reviews               []Review     `json:"reviews"`
	DependenciesAvailable bool         `json:"dependencies_available"`
	DependencyError       string       `json:"dependency_error,omitempty"`
	OpenDependencies      []Dependency `json:"open_dependencies"`
}

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
	// ExtraLinesCount is the number of lines after the anchored one the
	// comment covers, as Forgejo's review-comment schema names it: zero is a
	// single-line comment.
	ExtraLinesCount int64 `json:"extra_lines_count"`
}

type reviewWritePayload struct {
	State    ReviewVerdict   `json:"state"`
	Body     string          `json:"body"`
	Comments []ReviewComment `json:"comments"`
}

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
	Written *int         `json:"written,omitempty"`
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
