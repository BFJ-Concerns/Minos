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

func continuationPriority(snapshot forge.Snapshot, botLogin string) int {
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
	if found && (latest.Description == product.Incomplete().Description() ||
		latest.Description == product.Working().Description() ||
		latest.Description == product.Continuation().Description()) {
		return 0
	}
	return 1
}

func reconcilePullRequest(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts) (string, error) {
	adapter, snapshot, err := currentForgeSnapshot(ctx, cfg, facts)
	if err != nil {
		return "", err
	}
	if snapshot.State != "open" || snapshot.Merged || snapshot.Draft {
		return "nothing", nil
	}
	facts.HeadSHA = snapshot.HeadSHA
	facts.BaseSHA = snapshot.TargetSHA
	facts.BaseRef = snapshot.TargetBranch
	facts.HeadRef = snapshot.HeadBranch
	if review, reviewed := currentReview(snapshot, cfg.Service.BotLogin); reviewed {
		state, terminal := terminalState(review)
		if terminal {
			if hasTerminalStatus(snapshot, cfg, facts, state) {
				return "nothing", nil
			}
			pr, _ := strconv.ParseInt(facts.PR, 10, 64)
			result := adapter.SetProductStatus(ctx, forge.Guard{
				Repository:  forge.Repository{Owner: facts.Owner, Name: facts.Repo},
				PullRequest: pr, HeadSHA: facts.HeadSHA, TargetSHA: facts.BaseSHA,
			}, state)
			switch result.Outcome {
			case forge.WriteApplied:
				return "recovered", nil
			case forge.WriteRejected:
				return "nothing", nil
			default:
				return "", fmt.Errorf("restore terminal Minos status: %s", result.Reason)
			}
		}
	}
	if reason, deferred := dependencyDeferral(snapshot); deferred {
		return "deferred: " + reason, nil
	}
	outcome, err := SpawnRun(ctx, cfg, repo, facts)
	if err != nil {
		return "", err
	}
	return string(outcome), nil
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
