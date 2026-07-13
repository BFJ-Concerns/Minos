package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bfj/minos/internal/product"
)

type Adapter struct {
	runner           Runner
	ownership        OwnershipChecker
	serviceLogin     string
	mergeRetryDelays []time.Duration
}

var defaultMergeRetryDelays = []time.Duration{
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	14 * time.Second,
}

func NewAdapter(runner Runner, ownership OwnershipChecker, serviceLogin string) (*Adapter, error) {
	if runner == nil {
		return nil, fmt.Errorf("forge runner is required")
	}
	if ownership == nil {
		return nil, fmt.Errorf("ownership checker is required")
	}
	if strings.TrimSpace(serviceLogin) == "" {
		return nil, fmt.Errorf("service login is required")
	}
	return &Adapter{
		runner:           runner,
		ownership:        ownership,
		serviceLogin:     serviceLogin,
		mergeRetryDelays: append([]time.Duration(nil), defaultMergeRetryDelays...),
	}, nil
}

func (a *Adapter) Snapshot(ctx context.Context, repository Repository, pullRequest int64) (Snapshot, error) {
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "snapshot",
		Arguments: []string{repository.Owner, repository.Name, strconv.FormatInt(pullRequest, 10)},
	})
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(out, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode forge snapshot: %w", err)
	}
	if err := validateSnapshot(snapshot, repository, pullRequest); err != nil {
		return Snapshot{}, err
	}
	snapshot.CheckDecision = ReduceRequiredChecks(snapshot.RequiredChecks, snapshot.Statuses)
	return snapshot, nil
}

// SetProductStatus is the only route to the service-owned status context. Its
// product value is validated before either ownership or forge I/O, so Go's
// constructible zero value cannot render an empty or improvised status.
func (a *Adapter) SetProductStatus(ctx context.Context, guard Guard, state product.State) WriteResult {
	if !state.Valid() {
		return WriteResult{Outcome: WriteRejected, Reason: "invalid product state"}
	}
	if rejected := a.checkOwnership(ctx, guard.Ownership); rejected != nil {
		return *rejected
	}
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "guarded-set-status",
		Arguments: append(a.guardArguments(guard), OwnedStatusContext, state.ForgeState(), state.Description()),
	})
	return decodeWriteResult(out, err)
}

func (a *Adapter) PostReview(ctx context.Context, guard Guard, verdict ReviewVerdict, body string, comments []ReviewComment) WriteResult {
	if rejected := a.checkOwnership(ctx, guard.Ownership); rejected != nil {
		return *rejected
	}
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

func (a *Adapter) Push(ctx context.Context, guard Guard, request PushRequest) WriteResult {
	if rejected := a.checkOwnership(ctx, guard.Ownership); rejected != nil {
		return *rejected
	}
	arguments := append(a.guardArguments(guard),
		request.Branch,
		request.AuthorName,
		request.AuthorEmail,
		request.MessageFile,
	)
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "guarded-commit-push",
		Arguments: arguments,
		Env:       map[string]string{"MINOS_WORKSPACE": request.Workspace},
	})
	return decodeWriteResult(out, err)
}

func (a *Adapter) Merge(ctx context.Context, guard Guard, method MergeMethod) WriteResult {
	for attempt := 0; ; attempt++ {
		// A confirmed transient Forgejo refusal made no mutation. Re-check the
		// fenced owner before every fresh attempt; uncertain writes are never
		// retried because their effect is not known.
		if rejected := a.checkOwnership(ctx, guard.Ownership); rejected != nil {
			return *rejected
		}
		out, err := a.runner.Run(ctx, RunRequest{
			Operation: "guarded-merge",
			Arguments: append(a.guardArguments(guard), string(method)),
		})
		result, retryable := decodeMergeResult(out, err)
		if !retryable {
			return result
		}
		if attempt == len(a.mergeRetryDelays) {
			return WriteResult{Outcome: WriteUncertain, Reason: "Forgejo merge remained temporarily unavailable after bounded retries"}
		}
		if err := waitForRetry(ctx, a.mergeRetryDelays[attempt]); err != nil {
			return WriteResult{Outcome: WriteUncertain, Reason: "merge retry interrupted: " + err.Error()}
		}
	}
}

func (a *Adapter) DeleteMergedBranch(ctx context.Context, guard Guard) WriteResult {
	if rejected := a.checkOwnership(ctx, guard.Ownership); rejected != nil {
		return *rejected
	}
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "delete-branch",
		Arguments: a.guardArguments(guard),
	})
	return decodeWriteResult(out, err)
}

func (a *Adapter) checkOwnership(ctx context.Context, ownership Ownership) *WriteResult {
	if err := a.ownership.Check(ctx, ownership); err != nil {
		return &WriteResult{Outcome: WriteRejected, Reason: "ownership: " + err.Error()}
	}
	return nil
}

func (a *Adapter) guardArguments(guard Guard) []string {
	return []string{
		guard.Repository.Owner,
		guard.Repository.Name,
		strconv.FormatInt(guard.PullRequest, 10),
		guard.HeadSHA,
		guard.TargetSHA,
		a.serviceLogin,
	}
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

func decodeMergeResult(out []byte, runErr error) (WriteResult, bool) {
	if runErr != nil {
		return decodeWriteResult(out, runErr), false
	}
	var result WriteResult
	if err := json.Unmarshal(out, &result); err != nil {
		return decodeWriteResult(out, nil), false
	}
	if result.Outcome == WriteRetryable {
		return result, true
	}
	return decodeWriteResult(out, nil), false
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
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
	for _, status := range snapshot.Statuses {
		if status.ID <= 0 || status.Provider == "" || status.Context == "" {
			return fmt.Errorf("snapshot contained an incomplete status")
		}
	}
	return nil
}
