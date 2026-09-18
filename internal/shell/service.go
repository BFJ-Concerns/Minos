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

func currentContinuationPriority(ctx context.Context, cfg ServiceConfig, facts Facts) (int, error) {
	adapter, err := newBehaviouralForge(cfg, facts.Forge)
	if err != nil {
		return 0, err
	}
	statuses, err := adapter.CommitStatuses(ctx, forge.Repository{Owner: facts.Owner, Name: facts.Repo}, facts.HeadSHA)
	if err != nil {
		return 0, err
	}
	return continuationPriority(forge.Snapshot{Statuses: statuses}, cfg.Service.BotLogin), nil
}

func alreadyReviewed(snapshot forge.Snapshot, botLogin string) bool {
	_, found := currentReview(snapshot, botLogin)
	return found
}

// currentReview finds the bot's latest review of the current head. Minos
// authors no commits, so any head movement is the author's and simply leaves
// old reviews behind: a review marks a completed run only while its commit is
// still the head.
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

// A clean or attention Minos status on the current head marks a completed run
// even when no terminal review exists: some deliberate stops publish no
// review, so the status is the head's only durable completion marker on that
// path. Head movement leaves the status on the old commit, spending the
// marker; an incomplete status deliberately leaves the pull request eligible
// for a fresh attempt.
func completedRunStatus(snapshot forge.Snapshot, botLogin string) bool {
	latest, found := latestOwnedStatus(snapshot, botLogin)
	if !found {
		return false
	}
	_, completed := product.CompletionMarker(string(latest.State), latest.Description)
	return completed
}

type ReconcileDecision string

const (
	SpawnStarted           ReconcileDecision = "started"
	SpawnContinued         ReconcileDecision = "continued"
	SpawnSuppressed        ReconcileDecision = "suppressed"
	SpawnAttention         ReconcileDecision = "attention"
	ReconcileNothing       ReconcileDecision = "nothing"
	deferredDecisionPrefix                   = "deferred: "
)

type ReconcileResult struct {
	Decision ReconcileDecision
	// DeferralReason is set only when reconciliation deliberately leaves an
	// open pull request unstarted. Sweep records it for the operator-facing
	// projection; admission never reads that record back.
	DeferralReason string
	BlockingUnit   string
	Detail         string
}

func reconcilePullRequest(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts) (ReconcileResult, error) {
	adapter, snapshot, err := currentForgeSnapshot(ctx, cfg, facts)
	if err != nil {
		return ReconcileResult{}, err
	}
	return reconcilePullRequestSnapshot(ctx, cfg, repo, facts, adapter, snapshot)
}

func reconcilePullRequestSnapshot(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, adapter *forge.Adapter, snapshot forge.Snapshot) (ReconcileResult, error) {
	if snapshot.State != "open" || snapshot.Merged || snapshot.Draft {
		return ReconcileResult{Decision: ReconcileNothing}, nil
	}
	if staleApproval(snapshot, cfg.Service.BotLogin) {
		guard := forge.Guard{
			Repository:  forge.Repository{Owner: facts.Owner, Name: facts.Repo},
			PullRequest: snapshot.PullRequest,
			HeadSHA:     snapshot.HeadSHA,
			TargetSHA:   snapshot.TargetSHA,
		}
		if result := adapter.RemoveReaction(ctx, guard, "+1"); result.Outcome != forge.WriteApplied {
			return ReconcileResult{}, fmt.Errorf("remove stale approval reaction: %s: %s", result.Outcome, result.Reason)
		}
	}
	facts.HeadSHA = snapshot.HeadSHA
	facts.BaseSHA = snapshot.TargetSHA
	facts.BaseRef = snapshot.TargetBranch
	facts.HeadRef = snapshot.HeadBranch
	eligibility := assessPullRequestAdmission(cfg, repo, snapshot)
	if eligibility.workInProgress {
		reason := fmt.Sprintf("work-in-progress branch %q", snapshot.HeadBranch)
		return ReconcileResult{Decision: ReconcileDecision(deferredDecisionPrefix + reason), DeferralReason: reason}, nil
	}
	if eligibility.completedRun {
		return ReconcileResult{Decision: ReconcileNothing, DeferralReason: "completed-marker"}, nil
	}
	if eligibility.dependencyDeferred {
		return ReconcileResult{Decision: ReconcileDecision(deferredDecisionPrefix + eligibility.dependencyReason), DeferralReason: eligibility.dependencyReason}, nil
	}
	outcome, err := SpawnRun(ctx, cfg, repo, facts)
	if err != nil {
		return ReconcileResult{}, err
	}
	return ReconcileResult{
		Decision:     outcome.Outcome,
		BlockingUnit: outcome.BlockingUnit,
		Detail:       outcome.Detail,
	}, nil
}

// staleApproval reports whether the current head lacks a Minos clean result.
// The PR-wide reaction has no head identity of its own; a current clean
// status or historical-format approval review is its warrant.
func staleApproval(snapshot forge.Snapshot, botLogin string) bool {
	if status, found := latestOwnedStatus(snapshot, botLogin); found {
		if state, terminal := product.CompletionMarker(string(status.State), status.Description); terminal && state == product.Clean() {
			return false
		}
	}
	if review, found := currentReview(snapshot, botLogin); found {
		if state, terminal := terminalState(review); terminal && state == product.Clean() {
			return false
		}
	}
	return true
}

// pullRequestAdmissionEligibility holds the non-writing admission chain. The
// completion marker is head-bound: a terminal Minos review of the current
// head, or a clean/attention Minos status on it. The sweep never writes
// statuses itself — the terminal status is the lead's own best-effort write
// (the 2026-08-30 Decision rejects code-owned status reconciliation).
type pullRequestAdmissionEligibility struct {
	workInProgress     bool
	completedRun       bool
	dependencyDeferred bool
	dependencyReason   string
}

func assessPullRequestAdmission(cfg ServiceConfig, repo RepoConfig, snapshot forge.Snapshot) pullRequestAdmissionEligibility {
	if workInProgressBranch(snapshot.HeadBranch, repo.WorkInProgressBranchPrefixes) {
		return pullRequestAdmissionEligibility{workInProgress: true}
	}
	if review, reviewed := currentReview(snapshot, cfg.Service.BotLogin); reviewed {
		if _, terminal := terminalState(review); terminal {
			return pullRequestAdmissionEligibility{completedRun: true}
		}
	}
	if completedRunStatus(snapshot, cfg.Service.BotLogin) {
		return pullRequestAdmissionEligibility{completedRun: true}
	}
	if reason, deferred := dependencyDeferral(snapshot); deferred {
		return pullRequestAdmissionEligibility{dependencyDeferred: true, dependencyReason: reason}
	}
	return pullRequestAdmissionEligibility{}
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
	var state product.State
	switch strings.ToUpper(review.State) {
	case "APPROVED", "APPROVE":
		state = product.Clean()
	case "REQUEST_CHANGES", "REQUESTED_CHANGES":
		state = product.Attention()
	default:
		return product.State{}, false
	}
	return product.CompletionMarker(state.ForgeState(), state.Description())
}
