package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"bfj/minos/internal/product"
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
	out, err := a.runner.Run(ctx, RunRequest{Operation: "snapshot", Arguments: []string{repository.Owner, repository.Name, strconv.FormatInt(pullRequest, 10)}})
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

// Alert files an operator alert as a repository issue: one open issue per
// title, with repeat alerts arriving as comments on it.
func (a *Adapter) Alert(ctx context.Context, repository Repository, title, body string) WriteResult {
	return a.writeIssue(ctx, "alert", []string{repository.Owner, repository.Name}, title, body)
}

// AddMarker writes one marker in its configured form under the guard.
func (a *Adapter) AddMarker(ctx context.Context, guard Guard, marker Marker) WriteResult {
	return a.markerWrite(ctx, guard, marker, "guarded-add-reaction", "guarded-add-label")
}

// FileIssue delivers one marked entry to its configured repository. Its
// identity survives reviewed-head movement and closed destination issues.
func (a *Adapter) FileIssue(ctx context.Context, repository Repository, title, body string) WriteResult {
	return a.writeIssue(ctx, "guarded-file-issue", []string{repository.Owner, repository.Name, a.serviceLogin}, title, body)
}

// writeIssue carries the shared title/body payload across issue operations;
// each adaptation owns its own identity and deduplication contract.
func (a *Adapter) writeIssue(ctx context.Context, operation string, arguments []string, title, body string) WriteResult {
	payload, err := json.Marshal(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{Title: title, Body: body})
	if err != nil {
		return WriteResult{Outcome: WriteRejected, Reason: "encode " + operation + ": " + err.Error()}
	}
	out, runErr := a.runner.Run(ctx, RunRequest{
		Operation: operation,
		Arguments: arguments,
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
