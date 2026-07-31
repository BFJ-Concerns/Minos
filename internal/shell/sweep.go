package shell

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"
)

type sweepCandidate struct {
	repo     RepoConfig
	facts    Facts
	priority int
}

func SweepCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	configRoot := fs.String("config", DefaultConfigRoot, "configuration root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := LoadServiceConfig(*configRoot)
	if err != nil {
		return err
	}
	repos, err := LoadRepoConfigs(cfg.Root)
	if err != nil {
		return err
	}
	var candidates []sweepCandidate
	for _, repo := range repos {
		forgeConfig, ok := cfg.Forges[repo.Forge]
		if !ok {
			log.Printf("repo %s/%s: unknown forge %s", repo.Owner, repo.Repo, repo.Forge)
			continue
		}
		adaptation, err := NewAdaptation(forgeConfig)
		if err != nil {
			return err
		}
		pullRequests, err := adaptation.ListOpenPRs(ctx, repo.Forge, repo.Owner, repo.Repo)
		if err != nil {
			return err
		}
		for _, facts := range pullRequests {
			priority := 1
			if snapshot, err := currentSnapshot(ctx, cfg, facts); err != nil {
				log.Printf("%s#%s: inspect continuation priority: %v", facts.RepoSlug(), facts.PR, err)
			} else {
				priority = continuationPriority(snapshot, cfg.Service.BotLogin)
			}
			candidates = append(candidates, sweepCandidate{repo: repo, facts: facts, priority: priority})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].priority < candidates[j].priority
	})
	for _, candidate := range candidates {
		result, err := reconcilePullRequest(ctx, cfg, candidate.repo, candidate.facts)
		if err != nil {
			log.Printf("%s#%s: %v", candidate.facts.RepoSlug(), candidate.facts.PR, err)
			continue
		}
		if message := sweepDecisionMessage(candidate.facts, result); message != "" {
			log.Print(message)
		}
	}
	return nil
}

func sweepDecisionMessage(facts Facts, result string) string {
	if result == "started" {
		return fmt.Sprintf("started %s#%s", facts.RepoSlug(), facts.PR)
	}
	if strings.HasPrefix(result, "deferred: ") {
		return fmt.Sprintf("%s#%s: %s", facts.RepoSlug(), facts.PR, result)
	}
	return ""
}
