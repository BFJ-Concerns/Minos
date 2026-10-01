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
	"strconv"
	"strings"
	"time"
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
	repos, err := LoadRepoConfigs(cfg)
	if err != nil {
		return err
	}
	var candidates []sweepCandidate
	// One unreadable repo must not stop the pass: the rest of the fleet is
	// swept, and only a total outage fails the unit. Skipped repos are
	// counted across passes; a repo quietly failing for an hour files an
	// operator alert.
	var attempted, unreadable int
	skipped := make(map[string]string)
	for _, repo := range repos {
		slug := repo.Owner + "/" + repo.Repo
		forgeConfig, ok := cfg.Forges[repo.Forge]
		if !ok {
			skipped[slug] = fmt.Sprintf("unknown forge %q", repo.Forge)
			log.Printf("attention: repo %s skipped this pass: unknown forge %s", slug, repo.Forge)
			continue
		}
		attempted++
		adaptation, err := NewAdaptation(forgeConfig)
		if err != nil {
			unreadable++
			skipped[slug] = err.Error()
			log.Printf("attention: repo %s skipped this pass: %v", slug, err)
			continue
		}
		pullRequests, err := adaptation.ListOpenPRs(ctx, repo.Forge, repo.Owner, repo.Repo)
		if err != nil {
			unreadable++
			skipped[slug] = err.Error()
			log.Printf("attention: repo %s skipped this pass: %v", slug, err)
			continue
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
	activeUnits, err := activeRunUnitNames(ctx)
	if err != nil {
		log.Printf("inspect active runs for candidate ordering: %v", err)
	}
	candidates = orderSweepCandidates(candidates, activeUnits)
	document := sweepDeferralDocument{Deferrals: []sweepDeferral{}, Partial: len(skipped) > 0, SkippedRepositories: skipped}
	for _, candidate := range candidates {
		result, err := reconcilePullRequest(ctx, cfg, candidate.repo, candidate.facts)
		if err != nil {
			log.Printf("%s#%s: %v", candidate.facts.RepoSlug(), candidate.facts.PR, err)
			continue
		}
		if result.DeferralReason != "" {
			document.Deferrals = append(document.Deferrals, sweepDeferral{
				Forge: candidate.facts.Forge, Owner: candidate.facts.Owner, Repo: candidate.facts.Repo,
				PR: candidate.facts.PR, Reason: result.DeferralReason, Reasons: result.DeferralReasons,
			})
		}
		row := sweepDecisionRow{Forge: candidate.facts.Forge, Owner: candidate.facts.Owner, Repo: candidate.facts.Repo, PR: candidate.facts.PR, Decision: result.Decision}
		if result.TerminalOutcome != "" {
			row.Outcome = result.TerminalOutcome
			document.Terminal = append(document.Terminal, row)
		}
		switch result.Decision {
		case SpawnSuppressed:
			row.BlockingUnit, row.Detail = result.BlockingUnit, result.Detail
			document.Suppressed = append(document.Suppressed, row)
		case SpawnStarted, SpawnContinued:
			// Current forge eligibility is observable; an earlier deferral is not.
			row.Cause = "current forge head is eligible"
			document.Readied = append(document.Readied, row)
		}
		if message := sweepDecisionMessage(candidate.facts, result); message != "" {
			log.Print(message)
		}
	}
	if err := expireInactiveRunHandoffs(ctx, cfg, repos); err != nil {
		log.Printf("expire inactive run handoffs: %v", err)
	}
	recordRepoSkips(ctx, cfg, skipped)
	if attempted > 0 && unreadable == attempted {
		return fmt.Errorf("every configured repo (%d) was unreadable this pass", attempted)
	}
	if err := publishSweepDocument(cfg, document); err != nil {
		log.Printf("publish sweep deferrals: %v", err)
	}
	return nil
}

var renameSweepDeferrals = os.Rename

// publishSweepDocument replaces the previous pass as one complete document.
// A failed publication preserves the previous record and never changes admission.
func publishSweepDocument(cfg ServiceConfig, document sweepDeferralDocument) error {
	document.Kind = sweepDeferralDocumentKind
	document.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	return publishProjectionDocument(cfg.Runs.Dir, sweepDeferralsFilename, document, renameSweepDeferrals)
}

// orderSweepCandidates keeps continuation priority absolute, then within each
// priority class takes one candidate per repo in turn, so a repo with a steady
// stream of actionable pull requests cannot claim every free run slot in a
// single pass while later-configured repos starve. Each repo's own queue runs
// oldest pull request first: age is served in creation order, and a stuck head
// can only delay its own repo's lane, never the fleet.
//
// The rotation alone is not enough: slots usually free between passes, not
// mid-pass, and a rotation that restarts from the same repo every pass hands
// each freed slot to that repo again. activeUnits carries the live run units,
// and repos already holding slots yield the lane to repos holding fewer, so
// service alternates across passes instead of draining one repo's queue first.
func orderSweepCandidates(candidates []sweepCandidate, activeUnits []string) []sweepCandidate {
	sort.SliceStable(candidates, func(i, j int) bool {
		return pullRequestNumber(candidates[i].facts) < pullRequestNumber(candidates[j].facts)
	})
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].priority < candidates[j].priority
	})
	ordered := make([]sweepCandidate, 0, len(candidates))
	for start := 0; start < len(candidates); {
		end := start
		for end < len(candidates) && candidates[end].priority == candidates[start].priority {
			end++
		}
		ordered = append(ordered, interleaveByRepo(candidates[start:end], activeUnits)...)
		start = end
	}
	return ordered
}

// repoRunCounts counts the live run units each candidate repo holds, matched
// by the same sanitised unit-name prefix SpawnRun claims them under.
func repoRunCounts(candidates []sweepCandidate, activeUnits []string) map[string]int {
	counts := make(map[string]int)
	for _, candidate := range candidates {
		slug := candidate.facts.RepoSlug()
		if _, seen := counts[slug]; seen {
			continue
		}
		counts[slug] = 0
		prefix := unitSafe.ReplaceAllString(
			fmt.Sprintf("minos-run-%s-%s-pr", candidate.facts.Owner, candidate.facts.Repo), "-")
		for _, unit := range activeUnits {
			if strings.HasPrefix(unit, prefix) {
				counts[slug]++
			}
		}
	}
	return counts
}

func pullRequestNumber(facts Facts) int64 {
	number, _ := strconv.ParseInt(facts.PR, 10, 64)
	return number
}

func interleaveByRepo(candidates []sweepCandidate, activeUnits []string) []sweepCandidate {
	queues := make(map[string][]sweepCandidate)
	var repoOrder []string
	for _, candidate := range candidates {
		slug := candidate.facts.RepoSlug()
		if _, seen := queues[slug]; !seen {
			repoOrder = append(repoOrder, slug)
		}
		queues[slug] = append(queues[slug], candidate)
	}
	counts := repoRunCounts(candidates, activeUnits)
	sort.SliceStable(repoOrder, func(i, j int) bool {
		return counts[repoOrder[i]] < counts[repoOrder[j]]
	})
	interleaved := make([]sweepCandidate, 0, len(candidates))
	for len(interleaved) < len(candidates) {
		for _, slug := range repoOrder {
			if queue := queues[slug]; len(queue) > 0 {
				interleaved = append(interleaved, queue[0])
				queues[slug] = queue[1:]
			}
		}
	}
	return interleaved
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
		if result.Detail != "" {
			return fmt.Sprintf("%s: suppressed because %s", pullRequest, result.Detail)
		}
		return fmt.Sprintf("%s: suppressed by active unit %s", pullRequest, result.BlockingUnit)
	case SpawnContinued:
		return pullRequest + ": continued previous run"
	case ReconcileNothing:
		return pullRequest + ": nothing to do"
	case SpawnAttention:
		return fmt.Sprintf("%s: attention: stopped stalled continuation because its %s", pullRequest, result.Detail)
	}
	return ""
}
