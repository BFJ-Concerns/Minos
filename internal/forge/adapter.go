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
	runner       Runner
	serviceLogin string
}

func NewAdapter(runner Runner, serviceLogin string) (*Adapter, error) {
	if runner == nil {
		return nil, fmt.Errorf("forge runner is required")
	}
	if strings.TrimSpace(serviceLogin) == "" {
		return nil, fmt.Errorf("service login is required")
	}
	return &Adapter{runner: runner, serviceLogin: serviceLogin}, nil
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
	if err := validateSnapshot(snapshot, repository, pullRequest); err != nil {
		return Snapshot{}, err
	}
	snapshot.CheckDecision = ReduceRequiredChecks(snapshot.RequiredChecks, snapshot.Statuses)
	snapshot.FailedChecks = FailedRequiredChecks(snapshot.RequiredChecks, snapshot.Statuses)
	return snapshot, nil
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
	if !state.Valid() {
		return WriteResult{Outcome: WriteRejected, Reason: "invalid product state"}
	}
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "guarded-set-status",
		Arguments: append(a.guardArguments(guard), OwnedStatusContext, state.ForgeState(), state.Description()),
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

func (a *Adapter) AddReaction(ctx context.Context, guard Guard, content string) WriteResult {
	if strings.TrimSpace(content) == "" {
		return WriteResult{Outcome: WriteRejected, Reason: "reaction content is required"}
	}
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "guarded-add-reaction",
		Arguments: append(a.guardArguments(guard), content),
	})
	return decodeWriteResult(out, err)
}

func (a *Adapter) RemoveReaction(ctx context.Context, guard Guard, content string) WriteResult {
	if strings.TrimSpace(content) == "" {
		return WriteResult{Outcome: WriteRejected, Reason: "reaction content is required"}
	}
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "guarded-remove-reaction",
		Arguments: append(a.guardArguments(guard), content),
	})
	return decodeWriteResult(out, err)
}

func (a *Adapter) RemoveLabel(ctx context.Context, guard Guard, label string) WriteResult {
	if strings.TrimSpace(label) == "" {
		return WriteResult{Outcome: WriteRejected, Reason: "label is required"}
	}
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "guarded-remove-label",
		Arguments: append(a.guardArguments(guard), label),
	})
	return decodeWriteResult(out, err)
}

func (a *Adapter) Merge(ctx context.Context, guard Guard, method MergeMethod) WriteResult {
	out, err := a.runner.Run(ctx, RunRequest{Operation: "guarded-merge", Arguments: append(a.guardArguments(guard), string(method))})
	return decodeWriteResult(out, err)
}

func (a *Adapter) DeleteSourceBranch(ctx context.Context, guard Guard, branch string) WriteResult {
	if strings.TrimSpace(branch) == "" {
		return WriteResult{Outcome: WriteRejected, Reason: "source branch is required"}
	}
	out, err := a.runner.Run(ctx, RunRequest{
		Operation: "guarded-delete-source-branch",
		Arguments: append(a.guardArguments(guard), branch),
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
