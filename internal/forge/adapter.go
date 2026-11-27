package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BFJ-Concerns/Minos/internal/product"
)

type Adapter struct {
	runner        Runner
	serviceLogin  string
	statusContext string
}

// NewAdapter binds the adapter to the service's identity: the login its
// guarded writes are checked against and the context its statuses carry.
func NewAdapter(runner Runner, serviceLogin, statusContext string) (*Adapter, error) {
	if runner == nil {
		return nil, fmt.Errorf("forge runner is required")
	}
	if strings.TrimSpace(serviceLogin) == "" {
		return nil, fmt.Errorf("service login is required")
	}
	if strings.TrimSpace(statusContext) == "" {
		return nil, fmt.Errorf("status context is required")
	}
	return &Adapter{runner: runner, serviceLogin: serviceLogin, statusContext: statusContext}, nil
}

func (a *Adapter) Snapshot(ctx context.Context, repository Repository, pullRequest int64) (Snapshot, error) {
	return a.snapshot(ctx, repository, pullRequest, nil)
}

// SnapshotWithoutStatuses reads pull-request evidence without querying commit statuses.
func (a *Adapter) SnapshotWithoutStatuses(ctx context.Context, repository Repository, pullRequest int64) (Snapshot, error) {
	return a.snapshot(ctx, repository, pullRequest, []string{"without-statuses"})
}

func (a *Adapter) snapshot(ctx context.Context, repository Repository, pullRequest int64, options []string) (Snapshot, error) {
	args := append([]string{repository.Owner, repository.Name, strconv.FormatInt(pullRequest, 10)}, options...)
	out, err := a.runner.Run(ctx, RunRequest{Operation: "snapshot", Arguments: args})
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(out, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode forge snapshot: %w", err)
	}
	if snapshot.OpenDependencies == nil {
		snapshot.OpenDependencies = make([]Dependency, 0)
	}
	if err := validateSnapshot(snapshot, repository, pullRequest); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// ChangedPaths reads both sides of renamed files from the forge's pull
// request diff. The adaptation checks that the diff still binds to the
// observed head and target before returning any paths.
func (a *Adapter) ChangedPaths(ctx context.Context, guard Guard, targetBranch string) ([]string, error) {
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "changed-paths",
		Arguments: append(a.guardArguments(guard), targetBranch),
	})
	if err != nil {
		return nil, err
	}
	var paths []string
	if err := json.Unmarshal(out, &paths); err != nil {
		return nil, fmt.Errorf("decode changed paths: %w", err)
	}
	if paths == nil || slices.Contains(paths, "") {
		return nil, fmt.Errorf("changed paths must be an array of non-empty paths")
	}
	return paths, nil
}

// PullRequestRetired checks the pull request itself, independently of the
// target branch, statuses and reviews that a full snapshot also requires.
func (a *Adapter) PullRequestRetired(ctx context.Context, repository Repository, pullRequest int64) (bool, error) {
	out, err := a.runner.Run(ctx, RunRequest{Operation: "pull-request-state", Arguments: []string{repository.Owner, repository.Name, strconv.FormatInt(pullRequest, 10)}})
	if err != nil {
		return false, err
	}
	var state string
	if err := json.Unmarshal(out, &state); err != nil {
		return false, fmt.Errorf("decode pull request state: %w", err)
	}
	switch state {
	case "closed", "merged", "missing":
		return true, nil
	case "open":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected pull request state %q", state)
	}
}

// RepositoryMetadata reads a repository's clone URL and default branch.
func (a *Adapter) RepositoryMetadata(ctx context.Context, repository Repository) (RepositoryMetadata, error) {
	out, err := a.runner.Run(ctx, RunRequest{Operation: "repository-metadata", Arguments: []string{repository.Owner, repository.Name}})
	if err != nil {
		return RepositoryMetadata{}, err
	}
	var metadata struct {
		RepositoryMetadata
		LookupStatus int `json:"lookup_status"`
	}
	if err := json.Unmarshal(out, &metadata); err != nil {
		return RepositoryMetadata{}, fmt.Errorf("decode repository metadata: %w", err)
	}
	if metadata.LookupStatus != 0 {
		return RepositoryMetadata{}, &RepositoryLookupError{Repository: repository, Status: metadata.LookupStatus}
	}
	if metadata.CloneURL == "" || metadata.DefaultBranch == "" {
		return RepositoryMetadata{}, fmt.Errorf("repository metadata for %s/%s omitted its clone URL or default branch", repository.Owner, repository.Name)
	}
	return metadata.RepositoryMetadata, nil
}

// fullObjectName is a commit's full object name, SHA-1 or SHA-256.
var fullObjectName = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// DefaultBranchTip reads the commit a repository's default branch stands at
// now. An empty repository answers with no commit and no error.
func (a *Adapter) DefaultBranchTip(ctx context.Context, repository Repository) (BranchTip, error) {
	out, err := a.runner.Run(ctx, RunRequest{Operation: "default-branch-tip", Arguments: []string{repository.Owner, repository.Name}})
	if err != nil {
		return BranchTip{}, err
	}
	var tip struct {
		DefaultBranch string  `json:"default_branch"`
		Commit        *string `json:"commit"`
	}
	if err := json.Unmarshal(out, &tip); err != nil {
		return BranchTip{}, fmt.Errorf("decode default branch tip: %w", err)
	}
	if tip.DefaultBranch == "" {
		return BranchTip{}, fmt.Errorf("default branch tip for %s/%s named no branch", repository.Owner, repository.Name)
	}
	if tip.Commit == nil {
		return BranchTip{DefaultBranch: tip.DefaultBranch}, nil
	}
	if !fullObjectName.MatchString(*tip.Commit) {
		return BranchTip{}, fmt.Errorf("default branch tip for %s/%s is not a full commit name: %q", repository.Owner, repository.Name, *tip.Commit)
	}
	return BranchTip{DefaultBranch: tip.DefaultBranch, Commit: *tip.Commit}, nil
}

// RepositoryLookupError is the forge's own answer to a repository lookup
// other than success: the HTTP status it gave.
type RepositoryLookupError struct {
	Repository Repository
	Status     int
}

func (e *RepositoryLookupError) Error() string {
	return fmt.Sprintf("repository lookup for %s/%s returned HTTP %d", e.Repository.Owner, e.Repository.Name, e.Status)
}

// CloneCredential asks the adaptation for the HTTPS git credential its
// credential file stands for. Decoding errors never quote the output, which
// carries the secret.
func (a *Adapter) CloneCredential(ctx context.Context, repository Repository) (CloneCredential, error) {
	out, err := a.runner.Run(ctx, RunRequest{Operation: "clone-credential", Arguments: []string{repository.Owner, repository.Name}})
	if err != nil {
		return CloneCredential{}, err
	}
	var credential CloneCredential
	if json.Unmarshal(out, &credential) != nil {
		return CloneCredential{}, fmt.Errorf("decode clone credential: output is not a credential object")
	}
	if credential.Anonymous {
		if credential.Username != "" || credential.Password != "" {
			return CloneCredential{}, fmt.Errorf("anonymous clone credential carried a username or password")
		}
		return credential, nil
	}
	if credential.Username == "" || credential.Password == "" {
		return CloneCredential{}, fmt.Errorf("clone credential omitted its username or password")
	}
	return credential, nil
}

func (a *Adapter) CommitStatuses(ctx context.Context, repository Repository, sha string) ([]Status, error) {
	out, err := a.runner.Run(ctx, RunRequest{Operation: "commit-statuses", Arguments: []string{repository.Owner, repository.Name, sha}})
	if err != nil {
		return nil, err
	}
	var statuses []Status
	if err := json.Unmarshal(out, &statuses); err != nil {
		return nil, fmt.Errorf("decode forge commit statuses: %w", err)
	}
	return statuses, nil
}

// ReviewThreads reads the pull request's inline threads the service itself
// opened: each with its resolution and every comment on it, so a later run
// can see how the author answered an earlier finding. Threads opened by
// anyone else are left out.
func (a *Adapter) ReviewThreads(ctx context.Context, repository Repository, pullRequest int64) ([]ReviewThread, error) {
	out, err := a.runner.Run(ctx, RunRequest{Operation: "review-threads", Arguments: []string{repository.Owner, repository.Name, strconv.FormatInt(pullRequest, 10)}})
	if err != nil {
		return nil, err
	}
	var threads []ReviewThread
	if err := json.Unmarshal(out, &threads); err != nil {
		return nil, fmt.Errorf("decode forge review threads: %w", err)
	}
	own := make([]ReviewThread, 0, len(threads))
	for _, thread := range threads {
		if len(thread.Comments) > 0 && thread.Comments[0].User == a.serviceLogin {
			own = append(own, thread)
		}
	}
	return own, nil
}

// PullRequestComments reads the pull request's conversation comments,
// oldest first, whoever wrote them: an author answers a finding that has no
// thread of its own here.
func (a *Adapter) PullRequestComments(ctx context.Context, repository Repository, pullRequest int64) ([]PullRequestComment, error) {
	out, err := a.runner.Run(ctx, RunRequest{Operation: "pull-request-comments", Arguments: []string{repository.Owner, repository.Name, strconv.FormatInt(pullRequest, 10)}})
	if err != nil {
		return nil, err
	}
	var comments []PullRequestComment
	if err := json.Unmarshal(out, &comments); err != nil {
		return nil, fmt.Errorf("decode forge pull-request comments: %w", err)
	}
	return comments, nil
}

// MayCommand reports whether the forge shows the user with write access to
// the repository, which is what lets a comment of theirs command the
// service.
func (a *Adapter) MayCommand(ctx context.Context, repository Repository, user string) (bool, error) {
	out, err := a.runner.Run(ctx, RunRequest{Operation: "commenter-may-command", Arguments: []string{repository.Owner, repository.Name, user}})
	if err != nil {
		return false, err
	}
	var may bool
	if err := json.Unmarshal(out, &may); err != nil {
		return false, fmt.Errorf("decode commenter permission: %w", err)
	}
	return may, nil
}

// AcknowledgeComment writes the reaction on one of the pull request's
// conversation comments under the guard. A reaction the service already
// left there counts as written.
func (a *Adapter) AcknowledgeComment(ctx context.Context, guard Guard, commentID int64, reaction string) WriteResult {
	if commentID <= 0 {
		return WriteResult{Outcome: WriteRejected, Reason: "comment id must be positive"}
	}
	if err := (Marker{Reaction: reaction}).Validate(); err != nil {
		return WriteResult{Outcome: WriteRejected, Reason: err.Error()}
	}
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "guarded-acknowledge-comment",
		Arguments: append(a.guardArguments(guard), strconv.FormatInt(commentID, 10), reaction),
	})
	return decodeWriteResult(out, err)
}

func (a *Adapter) Claim(ctx context.Context, repository Repository, pullRequest int64) WriteResult {
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "claim",
		Arguments: []string{
			repository.Owner,
			repository.Name,
			strconv.FormatInt(pullRequest, 10),
			a.serviceLogin,
		},
	})
	return decodeWriteResult(out, err)
}

func (a *Adapter) SetProductStatus(ctx context.Context, guard Guard, state product.State) WriteResult {
	return a.SetProductStatusWithDescription(ctx, guard, state, state.Description())
}

// SetProductStatusWithDescription carries the condition behind a product outcome
// through the same guarded, read-back-confirmed write as the default description.
func (a *Adapter) SetProductStatusWithDescription(ctx context.Context, guard Guard, state product.State, description string) WriteResult {
	if !state.Valid() {
		return WriteResult{Outcome: WriteRejected, Reason: "invalid product state"}
	}
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "guarded-set-status",
		Arguments: append(a.guardArguments(guard), a.statusContext, state.ForgeState(), description),
		Env:       map[string]string{"MINOS_STATUS_CONTEXT": a.statusContext},
	})
	return decodeWriteResult(out, err)
}

func (a *Adapter) PostReview(ctx context.Context, guard Guard, verdict ReviewVerdict, body string, comments []ReviewComment) WriteResult {
	payload, err := json.Marshal(reviewWritePayload{State: verdict, Body: body, Comments: comments})
	if err != nil {
		return WriteResult{Outcome: WriteRejected, Reason: "encode review: " + err.Error()}
	}
	out, runErr := a.runner.Run(ctx, RunRequest{
		Operation: "guarded-post-review",
		Arguments: a.guardArguments(guard),
		Stdin:     bytes.NewReader(payload),
	})
	return decodeWriteResult(out, runErr)
}

// SummaryComment reads the service's summary comment on the pull request,
// nil when it has none. A snapshot carries the same comment; this is the
// read for a caller that needs nothing else of the pull request.
func (a *Adapter) SummaryComment(ctx context.Context, repository Repository, pullRequest int64) (*SummaryComment, error) {
	out, err := a.runner.Run(ctx, RunRequest{Operation: "summary-comment", Arguments: []string{repository.Owner, repository.Name, strconv.FormatInt(pullRequest, 10), a.serviceLogin}})
	if err != nil {
		return nil, err
	}
	var comment *SummaryComment
	if err := json.Unmarshal(out, &comment); err != nil {
		return nil, fmt.Errorf("decode forge summary comment: %w", err)
	}
	if comment != nil && comment.ID <= 0 {
		return nil, fmt.Errorf("forge summary comment carries no id")
	}
	return comment, nil
}

// WriteSummary makes body the pull request's summary comment under the
// guard: the comment is created when the pull request has none and edited
// in place otherwise, never posted a second time. The body must open with
// a summary record for the guarded head, so the completion marker a write
// leaves can only speak of the head the guard confirmed.
func (a *Adapter) WriteSummary(ctx context.Context, guard Guard, body string) WriteResult {
	record, found := product.ParseSummary(body)
	if !found {
		return WriteResult{Outcome: WriteRejected, Reason: "summary comment does not open with a summary record"}
	}
	if record.Head != guard.HeadSHA {
		return WriteResult{Outcome: WriteRejected, Reason: "summary record names a head other than the guarded one"}
	}
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "guarded-write-summary",
		Arguments: a.guardArguments(guard),
		Stdin:     strings.NewReader(body),
	})
	return decodeWriteResult(out, err)
}

// AddMarker writes one marker in its configured form under the guard.
func (a *Adapter) AddMarker(ctx context.Context, guard Guard, marker Marker) WriteResult {
	return a.markerWrite(ctx, guard, marker, "guarded-add-reaction", "guarded-add-label")
}

// FiledIssue is an open issue carrying the service's filing marker.
type FiledIssue struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// FiledIssues reads the open issues filed by the service in the destination repository.
func (a *Adapter) FiledIssues(ctx context.Context, repository Repository) ([]FiledIssue, error) {
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "filed-issues",
		Arguments: []string{repository.Owner, repository.Name, a.serviceLogin},
	})
	if err != nil {
		return nil, err
	}
	var issues []FiledIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("decode forge filed issues: %w", err)
	}
	if issues == nil {
		return nil, fmt.Errorf("forge filed issues must be a JSON array")
	}
	return issues, nil
}

// FileIssue delivers one marked entry to its configured repository. Its
// identity survives reviewed-head movement and closed destination issues.
func (a *Adapter) FileIssue(ctx context.Context, repository Repository, title, body string) WriteResult {
	payload, err := json.Marshal(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{Title: title, Body: body})
	if err != nil {
		return WriteResult{Outcome: WriteRejected, Reason: "encode guarded-file-issue: " + err.Error()}
	}
	out, runErr := a.runner.Run(ctx, RunRequest{
		Operation: "guarded-file-issue",
		Arguments: []string{repository.Owner, repository.Name, a.serviceLogin},
		Stdin:     bytes.NewReader(payload),
	})
	return decodeWriteResult(out, runErr)
}

// RemoveMarker removes one marker in its configured form under the guard.
func (a *Adapter) RemoveMarker(ctx context.Context, guard Guard, marker Marker) WriteResult {
	return a.markerWrite(ctx, guard, marker, "guarded-remove-reaction", "guarded-remove-label")
}

func (a *Adapter) markerWrite(ctx context.Context, guard Guard, marker Marker, reactionOperation, labelOperation string) WriteResult {
	operation, value := reactionOperation, marker.Reaction
	if marker.Label != "" {
		operation, value = labelOperation, marker.Label
	}
	if err := marker.Validate(); err != nil {
		return WriteResult{Outcome: WriteRejected, Reason: err.Error()}
	}
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: operation,
		Arguments: append(a.guardArguments(guard), value),
	})
	return decodeWriteResult(out, err)
}

func (a *Adapter) guardArguments(guard Guard) []string {
	return []string{guard.Repository.Owner, guard.Repository.Name, strconv.FormatInt(guard.PullRequest, 10), guard.HeadSHA, guard.TargetSHA, a.serviceLogin}
}

func decodeWriteResult(out []byte, runErr error) WriteResult {
	if runErr != nil {
		return WriteResult{Outcome: WriteUncertain, Reason: runErr.Error()}
	}
	var result WriteResult
	if err := json.Unmarshal(out, &result); err != nil {
		return WriteResult{Outcome: WriteUncertain, Reason: "decode guarded write result: " + err.Error()}
	}
	switch result.Outcome {
	case WriteApplied, WriteRejected, WriteRetryable, WriteUncertain:
		return result
	default:
		return WriteResult{Outcome: WriteUncertain, Reason: "guarded write returned invalid outcome " + string(result.Outcome)}
	}
}

func validateSnapshot(snapshot Snapshot, repository Repository, pullRequest int64) error {
	wantRepository := repository.Owner + "/" + repository.Name
	if snapshot.Repository != wantRepository || snapshot.TargetRepository != wantRepository {
		return fmt.Errorf("snapshot repository identity is %q/%q, want %q", snapshot.Repository, snapshot.TargetRepository, wantRepository)
	}
	if snapshot.PullRequest != pullRequest {
		return fmt.Errorf("snapshot pull request is %d, want %d", snapshot.PullRequest, pullRequest)
	}
	if snapshot.AuthenticatedUser == "" || snapshot.Author == "" || snapshot.HeadSHA == "" || snapshot.TargetSHA == "" || snapshot.TargetBranch == "" {
		return fmt.Errorf("snapshot omitted a mandatory identity")
	}
	return nil
}
