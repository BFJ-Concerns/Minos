package shell

import (
	"context"
	"strconv"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/product"
)

func currentSnapshot(ctx context.Context, cfg ServiceConfig, facts Facts) (forge.Snapshot, error) {
	adapter, err := newBehaviouralForge(cfg, facts.Forge)
	if err != nil {
		return forge.Snapshot{}, err
	}
	pr, err := strconv.ParseInt(facts.PR, 10, 64)
	if err != nil {
		return forge.Snapshot{}, err
	}
	return adapter.Snapshot(ctx, forge.Repository{Owner: facts.Owner, Name: facts.Repo}, pr)
}

func alreadyReviewed(snapshot forge.Snapshot, botLogin string) bool {
	reviewed := false
	for _, review := range snapshot.Reviews {
		if review.User != botLogin || review.CommitID != snapshot.HeadSHA {
			continue
		}
		record, ok := product.TrailingRecord(review.Body)
		if ok && record["head"] == snapshot.HeadSHA && record["target"] == snapshot.TargetSHA {
			reviewed = true
			break
		}
	}
	if !reviewed {
		return false
	}

	var latest forge.Status
	for _, status := range snapshot.Statuses {
		if status.Provider == forge.ForgejoProvider && status.Context == forge.OwnedStatusContext && status.Creator == botLogin && status.ID > latest.ID {
			latest = status
		}
	}
	return statusMatches(latest, product.Attention()) || statusMatches(latest, product.Clean())
}

func statusMatches(status forge.Status, state product.State) bool {
	return status.ID > 0 && status.State == forge.StatusState(state.ForgeState()) && status.Description == state.Description()
}

func reconcilePullRequest(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts) (string, error) {
	snapshot, err := currentSnapshot(ctx, cfg, facts)
	if err != nil {
		return "", err
	}
	if snapshot.State != "open" || snapshot.Merged || snapshot.Draft || alreadyReviewed(snapshot, cfg.Service.BotLogin) {
		return "nothing", nil
	}
	facts.HeadSHA = snapshot.HeadSHA
	facts.BaseSHA = snapshot.TargetSHA
	facts.BaseRef = snapshot.TargetBranch
	facts.HeadRef = snapshot.HeadBranch
	if err := SpawnRun(ctx, cfg, repo, facts); err != nil {
		return "", err
	}
	return "started", nil
}
