package shell

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/product"
)

func currentSnapshot(ctx context.Context, cfg ServiceConfig, facts Facts) (forge.Snapshot, error) {
	_, snapshot, err := currentForgeSnapshot(ctx, cfg, facts)
	return snapshot, err
}

func currentForgeSnapshot(ctx context.Context, cfg ServiceConfig, facts Facts) (*forge.Adapter, forge.Snapshot, error) {
	adapter, err := newBehaviouralForge(cfg, facts.Forge)
	if err != nil {
		return nil, forge.Snapshot{}, err
	}
	pr, err := strconv.ParseInt(facts.PR, 10, 64)
	if err != nil {
		return nil, forge.Snapshot{}, err
	}
	snapshot, err := adapter.Snapshot(ctx, forge.Repository{Owner: facts.Owner, Name: facts.Repo}, pr)
	return adapter, snapshot, err
}

func alreadyReviewed(snapshot forge.Snapshot, botLogin string) bool {
	_, found := currentReview(snapshot, botLogin)
	return found
}

func currentReview(snapshot forge.Snapshot, botLogin string) (forge.Review, bool) {
	var latest forge.Review
	found := false
	for _, review := range snapshot.Reviews {
		if review.User == botLogin && review.CommitID == snapshot.HeadSHA && (!found || review.ID > latest.ID) {
			latest = review
			found = true
		}
	}
	return latest, found
}

func latestOwnedStatus(snapshot forge.Snapshot, botLogin string) (forge.Status, bool) {
	var latest forge.Status
	found := false
	for _, status := range snapshot.Statuses {
		if status.Provider == forge.ForgejoProvider &&
			status.Context == forge.OwnedStatusContext &&
			status.Creator == botLogin &&
			(!found || status.ID > latest.ID) {
			latest = status
			found = true
		}
	}
	return latest, found
}

func continuationPriority(snapshot forge.Snapshot, botLogin string) int {
	latest, found := latestOwnedStatus(snapshot, botLogin)
	if found && (latest.Description == product.Incomplete().Description() ||
		latest.Description == product.Working().Description() ||
		latest.Description == product.Continuation().Description()) {
		return 0
	}
	return 1
}

// A clean, attention, or merged Minos status on the current head marks a
// completed run even when no terminal review exists: a converged clean run
// posts no approve review (the 👍 reaction carries all-clear), so the status
// is the head's only durable completion marker on that path.
func completedRunStatus(snapshot forge.Snapshot, botLogin, targetURL string) bool {
	var latest forge.Status
	found := false
	for _, status := range snapshot.Statuses {
		if status.Provider == forge.ForgejoProvider && status.Context == forge.OwnedStatusContext &&
			status.Creator == botLogin && status.TargetURL == targetURL &&
			(!found || status.ID > latest.ID) {
			latest = status
			found = true
		}
	}
	if !found {
		return false
	}
	return latest.Description == product.Clean().Description() ||
		latest.Description == product.Attention().Description() ||
		latest.Description == product.Merged().Description()
}

type ReconcileDecision string

const (
	SpawnStarted           ReconcileDecision = "started"
	SpawnContinued         ReconcileDecision = "continued"
	SpawnSuppressed        ReconcileDecision = "suppressed"
	SpawnAttention         ReconcileDecision = "attention"
	ReconcileRecovered     ReconcileDecision = "recovered"
	ReconcileNothing       ReconcileDecision = "nothing"
	deferredDecisionPrefix                   = "deferred: "
)

type ReconcileResult struct {
	Decision     ReconcileDecision
	BlockingUnit string
	Detail       string
}

func reconcilePullRequest(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts) (ReconcileResult, error) {
	adapter, snapshot, err := currentForgeSnapshot(ctx, cfg, facts)
	if err != nil {
		return ReconcileResult{}, err
	}
	if snapshot.State != "open" || snapshot.Merged || snapshot.Draft {
		return ReconcileResult{Decision: ReconcileNothing}, nil
	}
	facts.HeadSHA = snapshot.HeadSHA
	facts.BaseSHA = snapshot.TargetSHA
	facts.BaseRef = snapshot.TargetBranch
	facts.HeadRef = snapshot.HeadBranch
	if workInProgressBranch(snapshot.HeadBranch, repo.WorkInProgressBranchPrefixes) {
		return ReconcileResult{Decision: ReconcileDecision(deferredDecisionPrefix + fmt.Sprintf("work-in-progress branch %q", snapshot.HeadBranch))}, nil
	}
	if review, reviewed := currentReview(snapshot, cfg.Service.BotLogin); reviewed {
		state, terminal := terminalState(review)
		if terminal {
			if hasTerminalStatus(snapshot, cfg, facts, state) {
				return ReconcileResult{Decision: ReconcileNothing}, nil
			}
			pr, _ := strconv.ParseInt(facts.PR, 10, 64)
			result := adapter.SetProductStatus(ctx, forge.Guard{
				Repository:  forge.Repository{Owner: facts.Owner, Name: facts.Repo},
				PullRequest: pr, HeadSHA: facts.HeadSHA, TargetSHA: facts.BaseSHA,
			}, state)
			switch result.Outcome {
			case forge.WriteApplied:
				return ReconcileResult{Decision: ReconcileRecovered}, nil
			case forge.WriteRejected:
				return ReconcileResult{Decision: ReconcileNothing}, nil
			default:
				return ReconcileResult{}, fmt.Errorf("restore terminal Minos status: %s", result.Reason)
			}
		}
	}
	if completedRunStatus(snapshot, cfg.Service.BotLogin, statusTargetURL(cfg.Forges[facts.Forge].APIBase, facts)) {
		return ReconcileResult{Decision: ReconcileNothing}, nil
	}
	if reason, deferred := dependencyDeferral(snapshot); deferred {
		return ReconcileResult{Decision: ReconcileDecision(deferredDecisionPrefix + reason)}, nil
	}
	outcome, err := SpawnRun(ctx, cfg, repo, facts)
	if err != nil {
		return ReconcileResult{}, err
	}
	return ReconcileResult{Decision: outcome.Outcome, BlockingUnit: outcome.BlockingUnit, Detail: outcome.Detail}, nil
}

func workInProgressBranch(branch string, prefixes []string) bool {
	if branch == "" {
		return false
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(branch, prefix) {
			return true
		}
	}
	return false
}

func dependencyDeferral(snapshot forge.Snapshot) (string, bool) {
	if !snapshot.DependenciesAvailable {
		reason := snapshot.DependencyError
		if reason == "" {
			reason = "forge adaptation did not report dependency state"
		}
		return "dependency state unavailable: " + reason, true
	}
	if len(snapshot.OpenDependencies) == 0 {
		return "", false
	}
	dependencies := make([]string, 0, len(snapshot.OpenDependencies))
	for _, dependency := range snapshot.OpenDependencies {
		dependencies = append(dependencies, fmt.Sprintf("%s#%d", dependency.Repository, dependency.Number))
	}
	return "open dependencies: " + strings.Join(dependencies, ", "), true
}

func terminalState(review forge.Review) (product.State, bool) {
	switch strings.ToUpper(review.State) {
	case "APPROVED", "APPROVE":
		return product.Clean(), true
	case "REQUEST_CHANGES", "REQUESTED_CHANGES":
		return product.Attention(), true
	default:
		return product.State{}, false
	}
}

func hasTerminalStatus(snapshot forge.Snapshot, cfg ServiceConfig, facts Facts, state product.State) bool {
	wantTarget := statusTargetURL(cfg.Forges[facts.Forge].APIBase, facts)
	for _, status := range snapshot.Statuses {
		if status.Provider == forge.ForgejoProvider && status.Context == forge.OwnedStatusContext &&
			status.Creator == cfg.Service.BotLogin && status.State == forge.StatusState(state.ForgeState()) &&
			status.Description == state.Description() && status.TargetURL == wantTarget {
			return true
		}
	}
	return false
}

func statusTargetURL(apiBase string, facts Facts) string {
	webBase := strings.TrimSuffix(strings.TrimSuffix(apiBase, "/"), "/api/v1")
	return fmt.Sprintf("%s/%s/%s/pulls/%s#minos-target-%s", webBase, facts.Owner, facts.Repo, facts.PR, facts.BaseSHA)
}
