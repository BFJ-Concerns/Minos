package shell

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type sweepCandidate struct {
	repo     RepoConfig
	facts    Facts
	priority int
}

var inspectHandoffSnapshot = currentSnapshot

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
			if inspected, err := currentContinuationPriority(ctx, cfg, facts); err != nil {
				log.Printf("%s#%s: inspect continuation priority: %v", facts.RepoSlug(), facts.PR, err)
			} else {
				priority = inspected
			}
			candidates = append(candidates, sweepCandidate{repo: repo, facts: facts, priority: priority})
		}
	}
	factsByUnit := make(map[string]Facts, len(candidates))
	for _, candidate := range candidates {
		factsByUnit[UnitName(candidate.facts)] = candidate.facts
	}
	if err := sweepRunResidue(ctx, cfg, factsByUnit); err != nil {
		log.Printf("sweep run residue: %v", err)
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
	if err := expireInactiveRunHandoffs(ctx, cfg, repos); err != nil {
		log.Printf("expire inactive run handoffs: %v", err)
	}
	return nil
}

func expireInactiveRunHandoffs(ctx context.Context, cfg ServiceConfig, repos []RepoConfig) error {
	configured := make(map[string]RepoConfig, len(repos))
	for _, repo := range repos {
		configured[repo.Owner+"\x00"+repo.Repo] = repo
	}
	paths, err := filepath.Glob(filepath.Join(cfg.Runs.Dir, ".handoffs", "*.json"))
	if err != nil {
		return fmt.Errorf("list continuation handoffs: %w", err)
	}
	var expiryErrors []error
	for _, path := range paths {
		original, err := os.ReadFile(path)
		if err != nil {
			expiryErrors = append(expiryErrors, fmt.Errorf("read continuation handoff %s: %w", filepath.Base(path), err))
			continue
		}
		handoff, err := readRunHandoffStructure(path)
		if err != nil {
			continue
		}
		facts := Facts{Owner: handoff.PullRequest.Owner, Repo: handoff.PullRequest.Repo, PR: handoff.PullRequest.Number, HeadSHA: handoff.Head}
		if UnitName(facts) != strings.TrimSuffix(filepath.Base(path), ".json") {
			continue
		}
		repo, isConfigured := configured[facts.Owner+"\x00"+facts.Repo]
		shouldExpire := !isConfigured
		if isConfigured {
			facts.Forge = repo.Forge
			snapshot, snapshotErr := inspectHandoffSnapshot(ctx, cfg, facts)
			if snapshotErr != nil {
				expiryErrors = append(expiryErrors, fmt.Errorf("inspect continuation handoff %s: %w", filepath.Base(path), snapshotErr))
				continue
			}
			shouldExpire = snapshot.Merged || snapshot.State == "closed"
		}
		if !shouldExpire {
			continue
		}
		if err := removeUnchangedHandoff(cfg.Runs.Dir, path, original); err != nil {
			expiryErrors = append(expiryErrors, err)
		}
	}
	return errors.Join(expiryErrors...)
}

func removeUnchangedHandoff(runsDir, path string, original []byte) error {
	unlock, err := lockAdmission(runsDir)
	if err != nil {
		return fmt.Errorf("lock continuation handoff expiry for %s: %w", filepath.Base(path), err)
	}
	defer unlock()
	current, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("re-read continuation handoff %s: %w", filepath.Base(path), err)
	}
	if !bytes.Equal(current, original) {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("expire continuation handoff %s: %w", filepath.Base(path), err)
	}
	return nil
}

func sweepDecisionMessage(facts Facts, result ReconcileResult) string {
	if result.Decision == SpawnStarted {
		return fmt.Sprintf("started %s#%s", facts.RepoSlug(), facts.PR)
	}
	if strings.HasPrefix(string(result.Decision), deferredDecisionPrefix) {
		return fmt.Sprintf("%s#%s: %s", facts.RepoSlug(), facts.PR, result.Decision)
	}
	pullRequest := fmt.Sprintf("%s#%s", facts.RepoSlug(), facts.PR)
	switch result.Decision {
	case SpawnSuppressed:
		return fmt.Sprintf("%s: suppressed by active unit %s", pullRequest, result.BlockingUnit)
	case SpawnContinued:
		return pullRequest + ": continued previous run"
	case ReconcileRecovered:
		return pullRequest + ": recovered terminal Minos status"
	case ReconcileNothing:
		return pullRequest + ": nothing to do"
	case SpawnAttention:
		return fmt.Sprintf("%s: attention: stopped stalled continuation because its %s", pullRequest, result.Detail)
	}
	return ""
}
