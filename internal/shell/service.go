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

type AdmissionContext struct {
	ReleasedHoldHead      string
	ReleasedHoldStage     string
	ReleasedHoldDiagnosis string
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
	if strings.ToUpper(review.State) != "REQUEST_CHANGES" && strings.ToUpper(review.State) != "REQUESTED_CHANGES" {
		return false
	}
	record, ok := product.TrailingRecord(review.Body)
	return ok && record[product.RecordCauseKey] == product.RecordCauseRequiredChecks &&
		record[product.RecordTargetKey] != "" && record[product.RecordTargetKey] != snapshot.TargetSHA &&
		snapshot.HeadRepository == snapshot.TargetRepository
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

func releasedHoldContext(ctx context.Context, adapter *forge.Adapter, snapshot forge.Snapshot, repository forge.Repository, pullRequest int64, botLogin string, commits []forge.Commit) AdmissionContext {
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
	fragment := strings.LastIndex(held.TargetURL, heldTargetFragment)
	boundTarget := ""
	if fragment >= 0 {
		boundTarget = held.TargetURL[fragment+len(heldTargetFragment):]
	}
	if !found || boundTarget == "" || boundTarget == snapshot.TargetSHA {
		return AdmissionContext{}
	}
	comments, err := adapter.IssueComments(ctx, repository, pullRequest)
	if err != nil {
		return AdmissionContext{}
	}
	var latest forge.IssueComment
	found = false
	for _, comment := range comments {
		if comment.User == botLogin && (!found || comment.ID > latest.ID) {
			latest = comment
			found = true
		}
	}
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
	stageLine, diagnosis, found := strings.Cut(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	if !found || (stageLine != "Held at: finishing" && stageLine != "Held at: review") {
		return "", "", false
	}
	diagnosis = strings.TrimSpace(diagnosis)
	if diagnosis == "" {
		return "", "", false
	}
	return strings.TrimPrefix(stageLine, "Held at: "), diagnosis, true
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
func completedRunStatus(snapshot forge.Snapshot, botLogin, targetURL string) bool {
	var latest forge.Status
	found := false
	for _, status := range snapshot.Statuses {
		if status.Provider == forge.ForgejoProvider && status.Context == forge.OwnedStatusContext &&
			status.Creator == botLogin && status.TargetURL == targetURL &&
			(!found || status.ID > latest.ID) {
			latest = status
			found = true
		}
	}
	if !found {
		return false
	}
	return latest.Description == product.Clean().Description() ||
		latest.Description == product.Attention().Description() ||
		latest.Description == product.Held().Description() ||
		latest.Description == product.Merged().Description()
}

func trustedCompletedRunStatus(ctx context.Context, adapter *forge.Adapter, repository forge.Repository, snapshot forge.Snapshot, commits []forge.Commit, botLogin, targetURL string) bool {
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
		if completedRunStatus(copy, botLogin, targetURL) {
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
	Decision     ReconcileDecision
	BlockingUnit string
	Detail       string
}

func reconcilePullRequest(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts) (ReconcileResult, error) {
	adapter, snapshot, err := currentForgeSnapshot(ctx, cfg, facts)
	if err != nil {
		return ReconcileResult{}, err
	}
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
	admission := releasedHoldContext(ctx, adapter, snapshot, repository, pullRequest, cfg.Service.BotLogin, commits)
	if workInProgressBranch(snapshot.HeadBranch, repo.WorkInProgressBranchPrefixes) {
		return ReconcileResult{Decision: ReconcileDecision(deferredDecisionPrefix + fmt.Sprintf("work-in-progress branch %q", snapshot.HeadBranch))}, nil
	}
	if review, reviewed := trustedReview(snapshot, commits, cfg.Service.BotLogin); reviewed {
		state, terminal := terminalState(review)
		if terminal && !checkCausedVerdictSpent(review, snapshot) {
			if hasTerminalStatus(snapshot, cfg, facts, state) {
				return ReconcileResult{Decision: ReconcileNothing}, nil
			}
			pr, _ := strconv.ParseInt(facts.PR, 10, 64)
			result := adapter.SetProductStatus(ctx, forge.Guard{
				Repository:  forge.Repository{Owner: facts.Owner, Name: facts.Repo},
				PullRequest: pr, HeadSHA: facts.HeadSHA, TargetSHA: facts.BaseSHA,
			}, state)
			switch result.Outcome {
			case forge.WriteApplied:
				return ReconcileResult{Decision: ReconcileRecovered}, nil
			case forge.WriteRejected:
				return ReconcileResult{Decision: ReconcileNothing}, nil
			default:
				return ReconcileResult{}, fmt.Errorf("restore terminal Minos status: %s", result.Reason)
			}
		}
	}
	if trustedCompletedRunStatus(ctx, adapter, repository, snapshot, commits, cfg.Service.BotLogin, statusTargetURL(cfg.Forges[facts.Forge].APIBase, facts)) {
		return ReconcileResult{Decision: ReconcileNothing}, nil
	}
	if reason, deferred := dependencyDeferral(snapshot); deferred {
		return ReconcileResult{Decision: ReconcileDecision(deferredDecisionPrefix + reason)}, nil
	}
	outcome, err := SpawnRun(ctx, cfg, repo, facts, admission)
	if err != nil {
		return ReconcileResult{}, err
	}
	return ReconcileResult{Decision: outcome.Outcome, BlockingUnit: outcome.BlockingUnit, Detail: outcome.Detail}, nil
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
