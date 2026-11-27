package shell

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
)

func currentSnapshot(ctx context.Context, cfg ServiceConfig, facts Facts) (forge.Snapshot, error) {
	_, snapshot, err := currentForgeSnapshot(ctx, cfg, facts, true)
	return snapshot, err
}

func currentForgeSnapshot(ctx context.Context, cfg ServiceConfig, facts Facts, commitStatus bool) (*forge.Adapter, forge.Snapshot, error) {
	adapter, err := newBehaviouralForge(cfg, facts.Forge)
	if err != nil {
		return nil, forge.Snapshot{}, err
	}
	pr, err := strconv.ParseInt(facts.PR, 10, 64)
	if err != nil {
		return nil, forge.Snapshot{}, err
	}
	snapshot, err := pullRequestSnapshot(ctx, adapter, forge.Repository{Owner: facts.Owner, Name: facts.Repo}, pr, commitStatus)
	return adapter, snapshot, err
}

func pullRequestSnapshot(ctx context.Context, adapter *forge.Adapter, repository forge.Repository, pr int64, commitStatus bool) (forge.Snapshot, error) {
	if commitStatus {
		return adapter.Snapshot(ctx, repository, pr)
	}
	return adapter.SnapshotWithoutStatuses(ctx, repository, pr)
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
	return continuationPriority(forge.Snapshot{Statuses: statuses}, cfg.BotLogin(facts.Forge), cfg.Service.StatusContext), nil
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

// latestOwnedStatus reads commit statuses normalised by the configured adaptation.
// Provider records their origin; ownership is the configured context and creator.
func latestOwnedStatus(snapshot forge.Snapshot, botLogin, statusContext string) (forge.Status, bool) {
	var latest forge.Status
	found := false
	for _, status := range snapshot.Statuses {
		if status.Context == statusContext &&
			status.Creator == botLogin &&
			(!found || status.ID > latest.ID) {
			latest = status
			found = true
		}
	}
	return latest, found
}

func continuationPriority(snapshot forge.Snapshot, botLogin, statusContext string) int {
	latest, found := latestOwnedStatus(snapshot, botLogin, statusContext)
	state, recognised := product.StateForDescription(latest.Description)
	if found && recognised && (state == product.Incomplete() ||
		state == product.Working() || state == product.Continuation()) {
		return 0
	}
	return 1
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
	// DeferralReason is the precedence-selected reason for leaving an open
	// pull request unstarted. DeferralReasons carries the reasons observed
	// on that pass. Admission never reads the projection back.
	DeferralReason  string
	DeferralReasons []string
	TerminalOutcome string
	BlockingUnit    string
	Detail          string
}

func reconcilePullRequest(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts) (ReconcileResult, error) {
	adapter, snapshot, err := currentForgeSnapshot(ctx, cfg, facts, repo.Review.CommitStatus)
	if err != nil {
		return ReconcileResult{Decision: ReconcileNothing, DeferralReason: "pull request snapshot unavailable"}, err
	}
	return reconcilePullRequestSnapshot(ctx, cfg, repo, facts, adapter, snapshot)
}

func reconcilePullRequestSnapshot(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, adapter *forge.Adapter, snapshot forge.Snapshot) (ReconcileResult, error) {
	if !repo.Review.CommitStatus {
		snapshot.Statuses = nil
	}
	guard := forge.Guard{
		Repository:  forge.Repository{Owner: facts.Owner, Name: facts.Repo},
		PullRequest: snapshot.PullRequest,
		HeadSHA:     snapshot.HeadSHA,
		TargetSHA:   snapshot.TargetSHA,
	}
	if pullRequestClosed(snapshot) {
		return ReconcileResult{Decision: ReconcileNothing}, errors.Join(removeReviewedChange(cfg, facts.Forge, guard), removeCommandRecord(cfg, facts.Forge, guard))
	}
	if snapshot.State != "open" {
		return ReconcileResult{Decision: ReconcileNothing}, nil
	}
	denials := repo.Admission.metadataDeferrals(snapshot)
	var pathReadErr error
	if len(denials) == 0 && len(repo.Admission.Paths) > 0 {
		paths, err := adapter.ChangedPaths(ctx, guard, snapshot.TargetBranch)
		switch {
		case err != nil:
			denials = append(denials, "admission changed paths unavailable")
			pathReadErr = fmt.Errorf("read paths for admission: %w", err)
		case !repo.Admission.matchesPaths(paths):
			denials = append(denials, "admission paths did not match")
		}
	}
	eligibility := assessPullRequestAdmission(cfg, repo, snapshot)
	if len(denials) > 0 {
		return ReconcileResult{
			Decision: ReconcileNothing, DeferralReason: denials[0],
			DeferralReasons: append(denials, eligibility.reasons...), TerminalOutcome: eligibility.terminalOutcome,
		}, pathReadErr
	}
	for _, terminal := range []struct {
		role   string
		marker *forge.Marker
		state  product.State
	}{
		{"clean", repo.Markers.Clean, product.Clean()},
		{"attention", repo.Markers.Attention, product.Attention()},
	} {
		if terminal.marker == nil || !staleTerminalMarker(snapshot, cfg.BotLogin(facts.Forge), cfg.Service.StatusContext, terminal.state) {
			continue
		}
		if result := adapter.RemoveMarker(ctx, guard, *terminal.marker); result.Outcome != forge.WriteApplied {
			return ReconcileResult{}, fmt.Errorf("remove stale %s marker: %s: %s", terminal.role, result.Outcome, result.Reason)
		}
	}
	botLogin := cfg.BotLogin(facts.Forge)
	commands, err := readIssuedCommands(ctx, adapter, guard.Repository, snapshot.Comments, botLogin)
	if err != nil {
		return ReconcileResult{
			Decision: ReconcileNothing, DeferralReason: "comment commands unavailable",
			DeferralReasons: append([]string{"comment commands unavailable"}, eligibility.reasons...), TerminalOutcome: eligibility.terminalOutcome,
		}, fmt.Errorf("read comment commands: %w", err)
	}
	record := readCommandRecord(cfg, facts.Forge, guard)
	readStatuses := func(head string) ([]forge.Status, error) {
		if !repo.Review.CommitStatus {
			return nil, nil
		}
		return adapter.CommitStatuses(ctx, guard.Repository, head)
	}
	effect := commandsInForce(commands, snapshot, record, readStatuses, botLogin, cfg.Service.StatusContext)
	acknowledged := acknowledgeCommands(ctx, adapter, guard, effect.unacknowledged)
	if len(acknowledged) > 0 || len(effect.answered) > 0 {
		updateCommandRecord(cfg, facts.Forge, guard, func(record *commandRecord) {
			for _, id := range acknowledged {
				if !slices.Contains(record.Acknowledged, id) {
					record.Acknowledged = append(record.Acknowledged, id)
				}
			}
			for _, id := range effect.answered {
				if !slices.Contains(record.Answered, id) {
					record.Answered = append(record.Answered, id)
				}
			}
		})
	}
	if effect.skipped {
		return ReconcileResult{
			Decision: ReconcileDecision(deferredDecisionPrefix + skippedByCommandReason), DeferralReason: skippedByCommandReason,
			DeferralReasons: append([]string{skippedByCommandReason}, eligibility.reasons...), TerminalOutcome: eligibility.terminalOutcome,
		}, nil
	}
	if effect.reviewRequested {
		eligibility.completedRun = false
		eligibility.reasons = slices.DeleteFunc(eligibility.reasons, func(reason string) bool { return reason == "completed-marker" })
		facts.reviewRequested = true
		repo.PushInspection.Outcomes = commandedOutcomes(repo.PushInspection.Outcomes, effect)
	}
	facts.HeadSHA = snapshot.HeadSHA
	facts.BaseSHA = snapshot.TargetSHA
	facts.BaseRef = snapshot.TargetBranch
	facts.HeadRef = snapshot.HeadBranch
	if eligibility.workInProgress {
		reason := fmt.Sprintf("work-in-progress branch %q", snapshot.HeadBranch)
		return ReconcileResult{Decision: ReconcileDecision(deferredDecisionPrefix + reason), DeferralReason: reason, DeferralReasons: eligibility.reasons, TerminalOutcome: eligibility.terminalOutcome}, nil
	}
	if eligibility.completedRun {
		completed := ReconcileResult{Decision: ReconcileNothing, DeferralReason: "completed-marker", DeferralReasons: eligibility.reasons, TerminalOutcome: eligibility.terminalOutcome}
		return completed, settleCompletedMarkers(ctx, adapter, guard, repo.Markers, eligibility.terminalOutcome)
	}
	if eligibility.dependencyDeferred {
		return ReconcileResult{Decision: ReconcileDecision(deferredDecisionPrefix + eligibility.dependencyReason), DeferralReason: eligibility.dependencyReason, DeferralReasons: eligibility.reasons}, nil
	}
	outcome, err := SpawnRun(ctx, cfg, repo, facts)
	if err != nil {
		return ReconcileResult{}, err
	}
	if outcome.Outcome == SpawnStarted || outcome.Outcome == SpawnContinued {
		recordCommandedRun(ctx, cfg, facts.Forge, adapter, guard, effect.waiting, repo.Review.CommitStatus)
	}
	return ReconcileResult{
		Decision:     outcome.Outcome,
		BlockingUnit: outcome.BlockingUnit,
		Detail:       outcome.Detail,
	}, nil
}

// staleTerminalMarker reports whether the current head's completion
// state (headCompletion) is not the one a terminal marker stands for. A
// PR-wide marker — reaction or label — has no head identity of its own;
// the head's completion is its warrant, and the marker itself is never
// read.
func staleTerminalMarker(snapshot forge.Snapshot, botLogin, statusContext string, want product.State) bool {
	state, completed := headCompletion(snapshot, botLogin, statusContext)
	return !completed || state != want
}

// headCompletion is the current head's completion marker. The summary
// comment speaks first: a record that names this head with a finished
// state completes it. Where the comment says nothing finished of this
// head, the head is completed by a terminal Minos review of it, or else by
// the latest Minos status on it when that status is terminal. Those two
// are all a head carries when the run that finished it wrote no summary
// comment, as the resident lead writes none, and such a head reads as
// finished for as long as it stands.
// Admission and marker reconciliation both read this, so a head is never
// deferred as one outcome while carrying the other's marker.
func headCompletion(snapshot forge.Snapshot, botLogin, statusContext string) (product.State, bool) {
	if record, found := snapshot.SummaryRecord(); found {
		if state, completed := record.Completion(snapshot.HeadSHA); completed {
			return state, true
		}
	}
	if review, found := currentReview(snapshot, botLogin); found {
		if state, terminal := terminalState(review); terminal {
			return state, true
		}
	}
	if status, found := latestOwnedStatus(snapshot, botLogin, statusContext); found {
		if state, terminal := product.CompletionMarker(string(status.State), status.Description); terminal {
			return state, true
		}
	}
	return product.State{}, false
}

// settleCompletedMarkers gives a completed head the markers its verdict
// calls for: no in-flight marker, and the terminal marker of the outcome
// when one is configured. The run writes both itself; this repairs a head
// whose run recorded its verdict and then failed a marker write, without
// another review. Both writes are idempotent, so a head already carrying
// the right markers costs only the guards' reads. The other outcome's
// terminal marker is removed by the stale-marker pass above.
func settleCompletedMarkers(ctx context.Context, adapter *forge.Adapter, guard forge.Guard, markers Markers, outcome string) error {
	if markers.InFlight != nil {
		if result := adapter.RemoveMarker(ctx, guard, *markers.InFlight); result.Outcome != forge.WriteApplied {
			return fmt.Errorf("remove in-flight marker from completed head: %s: %s", result.Outcome, result.Reason)
		}
	}
	terminal := markers.Attention
	if outcome == product.Clean().Name() {
		terminal = markers.Clean
	}
	if terminal != nil {
		if result := adapter.AddMarker(ctx, guard, *terminal); result.Outcome != forge.WriteApplied {
			return fmt.Errorf("add %s marker to completed head: %s: %s", outcome, result.Outcome, result.Reason)
		}
	}
	return nil
}

// pullRequestAdmissionEligibility holds the non-writing admission chain. The
// completion marker is head-bound (headCompletion). The sweep never writes
// statuses itself — the terminal status is the lead's own best-effort write
// (the 2026-08-30 Decision rejects code-owned status reconciliation).
type pullRequestAdmissionEligibility struct {
	workInProgress     bool
	completedRun       bool
	dependencyDeferred bool
	dependencyReason   string
	reasons            []string
	terminalOutcome    string
}

func assessPullRequestAdmission(cfg ServiceConfig, repo RepoConfig, snapshot forge.Snapshot) pullRequestAdmissionEligibility {
	if !repo.Review.CommitStatus {
		snapshot.Statuses = nil
	}
	eligibility := pullRequestAdmissionEligibility{}
	if workInProgressBranch(snapshot.HeadBranch, repo.WorkInProgressBranchPrefixes) {
		eligibility.workInProgress = true
		eligibility.reasons = append(eligibility.reasons, fmt.Sprintf("work-in-progress branch %q", snapshot.HeadBranch))
	}
	if state, completed := headCompletion(snapshot, cfg.BotLogin(repo.Forge), cfg.Service.StatusContext); completed {
		eligibility.terminalOutcome = state.Name()
	}
	eligibility.completedRun = eligibility.terminalOutcome != ""
	if eligibility.completedRun {
		eligibility.reasons = append(eligibility.reasons, "completed-marker")
	}
	if reason, deferred := dependencyDeferral(snapshot); deferred {
		eligibility.dependencyDeferred = true
		eligibility.dependencyReason = reason
		eligibility.reasons = append(eligibility.reasons, reason)
	}
	return eligibility
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
