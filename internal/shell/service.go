package shell

import (
	"context"
	"strconv"

	"bfj/minos/internal/forge"
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
	for _, review := range snapshot.Reviews {
		if review.User == botLogin && review.CommitID == snapshot.HeadSHA {
			return true
		}
	}
	return false
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
	outcome, err := SpawnRun(ctx, cfg, repo, facts)
	if err != nil {
		return "", err
	}
	return string(outcome), nil
}
