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
		if !terminal || hasTerminalStatus(snapshot, cfg, facts, state) {
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
	outcome, err := SpawnRun(ctx, cfg, repo, facts)
	if err != nil {
		return "", err
	}
	return string(outcome), nil
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
