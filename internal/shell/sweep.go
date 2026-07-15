package shell

import (
	"context"
	"flag"
	"log"
)

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
			result, err := reconcilePullRequest(ctx, cfg, repo, facts)
			if err != nil {
				log.Printf("%s#%s: %v", facts.RepoSlug(), facts.PR, err)
				continue
			}
			if result == "started" {
				log.Printf("started %s#%s", facts.RepoSlug(), facts.PR)
			}
		}
	}
	return nil
}
