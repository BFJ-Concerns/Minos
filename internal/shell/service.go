package shell

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/product"
)

const heldTargetFragment = "#minos-target-"
const heldEnvFragment = "+minos-env-"

// heldBinding splits a held status's target URL into the target SHA and the
// environment stamp it was bound to. Either half may be empty: a legacy hold
// carries no stamp and is treated as bound to an unknown environment.
func heldBinding(targetURL string) (boundTarget, envStamp string) {
	fragment := strings.LastIndex(targetURL, heldTargetFragment)
	if fragment < 0 {
		return "", ""
	}
	boundTarget, envStamp, _ = strings.Cut(targetURL[fragment+len(heldTargetFragment):], heldEnvFragment)
	return boundTarget, envStamp
}

// statusForTarget matches an owned status against this pull request's target
// URL, tolerating the environment-stamp suffix held statuses carry.
func statusForTarget(statusURL, targetURL string) bool {
	return statusURL == targetURL || strings.HasPrefix(statusURL, targetURL+heldEnvFragment)
}

type AdmissionContext struct {
	ReleasedHoldHead      string
	ReleasedHoldStage     string
	ReleasedHoldDiagnosis string
	// GroupCandidatesJSON is sweep-only raw material for the lead's grouping
	// judgement.  It is deliberately data, not a selection decision.
	GroupCandidatesJSON string
}

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

func trustedReview(snapshot forge.Snapshot, commits []forge.Commit, botLogin string) (forge.Review, bool) {
	var latest forge.Review
	found := false
	for _, review := range snapshot.Reviews {
		if review.User == botLogin && forge.OwnMovement(commits, review.CommitID, snapshot.HeadSHA, botLogin) && (!found || review.ID > latest.ID) {
			latest = review
			found = true
		}
	}
	return latest, found
}

func checkCausedVerdictSpent(review forge.Review, snapshot forge.Snapshot) bool {
	record, ok := product.TrailingRecord(review.Body)
	if !ok || record[product.RecordTargetKey] == "" || record[product.RecordTargetKey] == snapshot.TargetSHA || snapshot.HeadRepository != snapshot.TargetRepository {
		return false
	}
	switch strings.ToUpper(review.State) {
	case "REQUEST_CHANGES", "REQUESTED_CHANGES":
		return record[product.RecordCauseKey] == product.RecordCauseRequiredChecks
	case "APPROVED", "APPROVE":
		return record[product.RecordCauseKey] == product.RecordCauseChainWait
	default:
		return false
	}
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

func releasedHoldContext(ctx context.Context, adapter *forge.Adapter, snapshot forge.Snapshot, repository forge.Repository, pullRequest int64, botLogin string, commits []forge.Commit, envStamp string) AdmissionContext {
	var held forge.Status
	heldHead := ""
	found := false
	for index := len(commits) - 1; index >= 0; index-- {
		commit := commits[index]
		if !forge.OwnMovement(commits, commit.SHA, snapshot.HeadSHA, botLogin) {
			continue
		}
		statuses := snapshot.Statuses
		if commit.SHA != snapshot.HeadSHA {
			var err error
			statuses, err = adapter.CommitStatuses(ctx, repository, commit.SHA)
			if err != nil {
				return AdmissionContext{}
			}
		}
		for _, status := range statuses {
			if status.Provider == forge.ForgejoProvider && status.Context == forge.OwnedStatusContext &&
				status.Creator == botLogin && status.Description == product.Held().Description() &&
				(!found || status.ID > held.ID) {
				held, heldHead, found = status, commit.SHA, true
			}
		}
		if found {
			break
		}
	}
	boundTarget, boundStamp := heldBinding(held.TargetURL)
	if !found || boundTarget == "" {
		return AdmissionContext{}
	}
	// A hold stays respected only while both bindings still describe the
	// world it was decided in: the target it blamed and the run environment
	// it was judged under. A legacy hold with no stamp binds to an unknown
	// environment and is spent once.
	if boundTarget == snapshot.TargetSHA && boundStamp == envStamp {
		return AdmissionContext{}
	}
	comments, err := adapter.IssueComments(ctx, repository, pullRequest)
	if err != nil {
		return AdmissionContext{}
	}
	latest, found := latestOwnedComment(comments, botLogin)
	if !found {
		return AdmissionContext{}
	}
	stage, diagnosis, ok := parseHeldComment(latest.Body)
	if !ok {
		return AdmissionContext{}
	}
	return AdmissionContext{ReleasedHoldHead: heldHead, ReleasedHoldStage: stage, ReleasedHoldDiagnosis: diagnosis}
}

func parseHeldComment(body string) (string, string, bool) {
	stage, diagnosis, found := heldCommentFields(body)
	if !found || (stage != "finishing" && stage != "review") || diagnosis == "" {
		return "", "", false
	}
	return stage, diagnosis, true
}

func latestOwnedComment(comments []forge.IssueComment, botLogin string) (forge.IssueComment, bool) {
	var latest forge.IssueComment
	found := false
	for _, comment := range comments {
		if comment.User == botLogin && (!found || comment.ID > latest.ID) {
			latest, found = comment, true
		}
	}
	return latest, found
}

func heldCommentFields(body string) (string, string, bool) {
	stageLine, diagnosis, found := strings.Cut(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	if !found || !strings.HasPrefix(stageLine, "Held at: ") {
		return "", "", false
	}
	return strings.TrimPrefix(stageLine, "Held at: "), strings.TrimSpace(diagnosis), true
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

// A clean, attention, held, or merged Minos status on the current head marks
// a completed run even when no terminal review exists: a converged clean run
// posts no approve review (the 👍 reaction carries all-clear), so the status
// is the head's only durable completion marker on that path. Held blocks
// re-runs for the same target; when the target SHA changes the target URL
// changes and the status no longer matches, triggering a fresh run.
func completedRunStatus(snapshot forge.Snapshot, botLogin, targetURL, envStamp string) bool {
	var latest forge.Status
	found := false
	for _, status := range snapshot.Statuses {
		if status.Provider == forge.ForgejoProvider && status.Context == forge.OwnedStatusContext &&
			status.Creator == botLogin && statusForTarget(status.TargetURL, targetURL) &&
			(!found || status.ID > latest.ID) {
			latest = status
			found = true
		}
	}
	if !found {
		return false
	}
	if latest.Description == product.Held().Description() {
		// A hold marks the run completed only while its environment binding
		// still matches; a hold from an older deploy (or with no stamp) is
		// spent, and the pull request earns a fresh attempt.
		_, boundStamp := heldBinding(latest.TargetURL)
		return boundStamp == envStamp
	}
	return latest.Description == product.Clean().Description() ||
		latest.Description == product.Attention().Description() ||
		latest.Description == product.Merged().Description()
}

func trustedCompletedRunStatus(ctx context.Context, adapter *forge.Adapter, repository forge.Repository, snapshot forge.Snapshot, commits []forge.Commit, botLogin, targetURL, envStamp string) bool {
	for index := len(commits) - 1; index >= 0; index-- {
		commit := commits[index]
		if !forge.OwnMovement(commits, commit.SHA, snapshot.HeadSHA, botLogin) {
			continue
		}
		statuses := snapshot.Statuses
		if commit.SHA != snapshot.HeadSHA {
			var err error
			statuses, err = adapter.CommitStatuses(ctx, repository, commit.SHA)
			if err != nil {
				return false
			}
		}
		copy := snapshot
		copy.Statuses = statuses
		if completedRunStatus(copy, botLogin, targetURL, envStamp) {
			return true
		}
	}
	return false
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
	Decision            ReconcileDecision
	BlockingUnit        string
	Detail              string
	GroupCandidatesPath string
}

func reconcilePullRequest(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts) (ReconcileResult, error) {
	return reconcilePullRequestWithCandidates(ctx, cfg, repo, facts, "")
}

func reconcilePullRequestWithCandidates(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, candidates string) (ReconcileResult, error) {
	adapter, snapshot, err := currentForgeSnapshot(ctx, cfg, facts)
	if err != nil {
		return ReconcileResult{}, err
	}
	return reconcilePullRequestSnapshotWithCandidates(ctx, cfg, repo, facts, adapter, snapshot, candidates)
}

func reconcilePullRequestSnapshot(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, adapter *forge.Adapter, snapshot forge.Snapshot) (ReconcileResult, error) {
	return reconcilePullRequestSnapshotWithCandidates(ctx, cfg, repo, facts, adapter, snapshot, "")
}

func reconcilePullRequestSnapshotWithCandidates(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, adapter *forge.Adapter, snapshot forge.Snapshot, candidates string) (ReconcileResult, error) {
	if snapshot.State != "open" || snapshot.Merged || snapshot.Draft {
		return ReconcileResult{Decision: ReconcileNothing}, nil
	}
	facts.HeadSHA = snapshot.HeadSHA
	facts.BaseSHA = snapshot.TargetSHA
	facts.BaseRef = snapshot.TargetBranch
	facts.HeadRef = snapshot.HeadBranch
	pullRequest, _ := strconv.ParseInt(facts.PR, 10, 64)
	repository := forge.Repository{Owner: facts.Owner, Name: facts.Repo}
	commits, commitsErr := adapter.PullRequestCommits(ctx, repository, pullRequest)
	if commitsErr != nil {
		commits = []forge.Commit{{SHA: snapshot.HeadSHA, Author: cfg.Service.BotLogin}}
	}
	admission := releasedHoldContext(ctx, adapter, snapshot, repository, pullRequest, cfg.Service.BotLogin, commits, currentEnvironmentStamp(cfg))
	admission.GroupCandidatesJSON = candidates
	eligibility := assessPullRequestAdmission(ctx, cfg, repo, facts, adapter, snapshot, commits)
	if eligibility.workInProgress {
		return ReconcileResult{Decision: ReconcileDecision(deferredDecisionPrefix + fmt.Sprintf("work-in-progress branch %q", snapshot.HeadBranch))}, nil
	}
	if eligibility.terminalReview {
		if hasTerminalStatus(snapshot, cfg, facts, eligibility.terminalState) {
			return ReconcileResult{Decision: ReconcileNothing}, nil
		}
		pr, _ := strconv.ParseInt(facts.PR, 10, 64)
		result := adapter.SetProductStatus(ctx, forge.Guard{
			Repository:  forge.Repository{Owner: facts.Owner, Name: facts.Repo},
			PullRequest: pr, HeadSHA: facts.HeadSHA, TargetSHA: facts.BaseSHA,
		}, eligibility.terminalState)
		switch result.Outcome {
		case forge.WriteApplied:
			return ReconcileResult{Decision: ReconcileRecovered}, nil
		case forge.WriteRejected:
			return ReconcileResult{Decision: ReconcileNothing}, nil
		default:
			return ReconcileResult{}, fmt.Errorf("restore terminal Minos status: %s", result.Reason)
		}
	}
	if eligibility.completedRun {
		return ReconcileResult{Decision: ReconcileNothing}, nil
	}
	if eligibility.dependencyDeferred {
		return ReconcileResult{Decision: ReconcileDecision(deferredDecisionPrefix + eligibility.dependencyReason)}, nil
	}
	if eligibility.targetBroken {
		return ReconcileResult{Decision: ReconcileDecision(deferredDecisionPrefix + eligibility.targetBrokenReason)}, nil
	}
	runClass := RunClassReview
	if structuralBranch(snapshot.HeadBranch, repo.StructuralBranchPrefixes) {
		runClass = RunClassMaintenance
	}
	outcome, err := SpawnRun(ctx, cfg, repo, facts, admission, runClass)
	if err != nil {
		return ReconcileResult{}, err
	}
	return ReconcileResult{
		Decision:            outcome.Outcome,
		BlockingUnit:        outcome.BlockingUnit,
		Detail:              outcome.Detail,
		GroupCandidatesPath: outcome.GroupCandidatesPath,
	}, nil
}

// pullRequestAdmissionEligibility holds the non-writing admission chain shared
// by ordinary reconciliation and grouped-member surfacing. Reconciliation owns
// the terminal-status repair that follows this judgement; grouping only needs
// to know whether admitting a new run would be correct.
type pullRequestAdmissionEligibility struct {
	workInProgress     bool
	terminalReview     bool
	terminalState      product.State
	completedRun       bool
	dependencyDeferred bool
	dependencyReason   string
	targetBroken       bool
	targetBrokenReason string
}

func (e pullRequestAdmissionEligibility) admitsNewRun() bool {
	return !e.workInProgress && !e.terminalReview && !e.completedRun &&
		!e.dependencyDeferred && !e.targetBroken
}

func assessPullRequestAdmission(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, adapter *forge.Adapter, snapshot forge.Snapshot, commits []forge.Commit) pullRequestAdmissionEligibility {
	if workInProgressBranch(snapshot.HeadBranch, repo.WorkInProgressBranchPrefixes) {
		return pullRequestAdmissionEligibility{workInProgress: true}
	}
	if review, reviewed := trustedReview(snapshot, commits, cfg.Service.BotLogin); reviewed {
		if state, terminal := terminalState(review); terminal && !checkCausedVerdictSpent(review, snapshot) {
			return pullRequestAdmissionEligibility{terminalReview: true, terminalState: state}
		}
	}
	repository := forge.Repository{Owner: facts.Owner, Name: facts.Repo}
	if trustedCompletedRunStatus(ctx, adapter, repository, snapshot, commits, cfg.Service.BotLogin, statusTargetURL(cfg.Forges[facts.Forge].APIBase, facts), currentEnvironmentStamp(cfg)) {
		return pullRequestAdmissionEligibility{completedRun: true}
	}
	if reason, deferred := dependencyDeferral(snapshot); deferred {
		return pullRequestAdmissionEligibility{dependencyDeferred: true, dependencyReason: reason}
	}
	// Last, because it is the only gate that costs a forge read: by here the
	// pull request would otherwise start a run, so the read is spent once per
	// admissible pull request rather than once per pull request swept.
	if reason, broken := targetKnownBroken(ctx, adapter, repository, snapshot.TargetSHA, cfg.Service.BotLogin); broken {
		return pullRequestAdmissionEligibility{targetBroken: true, targetBrokenReason: reason}
	}
	return pullRequestAdmissionEligibility{}
}

// targetKnownBroken reports whether a predecessor run has already proven this
// exact target commit's own gate broken. The marker binds to the commit, so a
// target that moves carries none and every deferred pull request admits again
// without a release path of its own.
func targetKnownBroken(ctx context.Context, adapter *forge.Adapter, repository forge.Repository, targetSHA, botLogin string) (string, bool) {
	if targetSHA == "" {
		return "", false
	}
	statuses, err := adapter.CommitStatuses(ctx, repository, targetSHA)
	if err != nil {
		// An unreadable target is not a proven-broken one. Failing open costs
		// one ordinary run; failing closed would strand every pull request in
		// the repository on a transient read error.
		return "", false
	}
	var marker forge.Status
	found := false
	for _, status := range statuses {
		if status.Provider == forge.ForgejoProvider && status.Context == forge.TargetStatusContext &&
			status.Creator == botLogin && (!found || status.ID > marker.ID) {
			marker, found = status, true
		}
	}
	if !found {
		return "", false
	}
	reason := "the target branch is already proven broken"
	if proving := provingPullRequest(marker.TargetURL); proving != "" {
		reason += " by " + proving
	}
	if marker.Description != "" {
		reason += ": " + marker.Description
	}
	return reason, true
}

// provingPullRequest reads the "#N" reference back out of a target marker's
// URL, so a deferral can name the pull request whose run proved the breakage.
func provingPullRequest(targetURL string) string {
	_, number, found := strings.Cut(targetURL, "/pulls/")
	if !found || number == "" {
		return ""
	}
	if cut := strings.IndexAny(number, "#?/"); cut >= 0 {
		number = number[:cut]
	}
	if number == "" {
		return ""
	}
	return "#" + number
}

func structuralBranch(branch string, prefixes []string) bool {
	return branchMatchesPrefix(branch, prefixes)
}

func workInProgressBranch(branch string, prefixes []string) bool {
	return branchMatchesPrefix(branch, prefixes)
}

func branchMatchesPrefix(branch string, prefixes []string) bool {
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
