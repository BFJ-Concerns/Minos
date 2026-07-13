package shell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/incidents"
	"bfj/minos/internal/ledger"
	"bfj/minos/internal/product"
	"bfj/minos/internal/reconcile"
)

// A guarded branch deletion may be confirmed not to have happened without the
// forge explaining whether its refusal is transient. Retrying a small bounded
// number of times covers transient refusal; after that, preserving the branch
// with an operator signal is safer than allowing cleanup to mask stale-owner
// recovery forever.
const cleanupRetryLimit = 3

func buildSnapshot(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts) (reconcile.ForgeSnapshot, error) {
	adapter, err := newBehaviouralForge(cfg, facts.Forge, denyForgeMutation{})
	if err != nil {
		return reconcile.ForgeSnapshot{}, err
	}
	pullRequest, err := strconv.ParseInt(facts.PR, 10, 64)
	if err != nil {
		return reconcile.ForgeSnapshot{}, fmt.Errorf("invalid pull request %q: %w", facts.PR, err)
	}
	current, err := adapter.Snapshot(ctx, forge.Repository{Owner: facts.Owner, Name: facts.Repo}, pullRequest)
	if err != nil {
		return reconcile.ForgeSnapshot{}, err
	}
	if current.AuthenticatedUser != cfg.Service.BotLogin {
		return reconcile.ForgeSnapshot{}, fmt.Errorf("forge snapshot authenticated as %q, want service identity %q", current.AuthenticatedUser, cfg.Service.BotLogin)
	}
	state := productStateFromForge(current, cfg.Service.BotLogin)
	snapshot := reconcile.ForgeSnapshot{
		Key: coordinationKey(facts), HeadSHA: current.HeadSHA, TargetBranch: current.TargetBranch,
		TargetSHA: current.TargetSHA, TargetKnown: true, Open: current.State == "open" && !current.Merged, Merged: current.Merged,
		Draft: current.Draft, SkipDrafts: true, AuthorInScope: repoAuthorEligible(repo, current.Author),
		Occasion: facts.Occasion, Product: state, ProductHead: current.HeadSHA,
		GoverningIdentity: governingIdentity(repo), DeploymentProfile: deploymentIdentity(cfg, repo),
		RequiredChecks: requiredCheckSnapshotFromForge(current), Mergeability: strconv.FormatBool(current.Mergeable),
	}
	if record, ok := currentProductRecord(current, cfg.Service.BotLogin); ok {
		snapshot.ProductHead = record["head"]
		snapshot.ProductTarget = record["target"]
		if governing := record["governing"]; governing != "" {
			snapshot.ProductGoverning = governing
			snapshot.ProductGoverningKnown = true
		}
		switch reconcile.BlockKind(record["block-kind"]) {
		case reconcile.FindingBlock, reconcile.PermissionBlock:
			snapshot.ProductBlockKind = reconcile.BlockKind(record["block-kind"])
			snapshot.ProductBlockIdentity = record["block-identity"]
			snapshot.ProductBlockKnown = snapshot.ProductBlockIdentity != ""
		}
	}
	return snapshot, nil
}

func productStateFromForge(snapshot forge.Snapshot, serviceLogin string) product.State {
	var newest forge.Status
	for _, status := range snapshot.Statuses {
		if status.Provider != forge.ForgejoProvider || status.Context != forge.OwnedStatusContext || status.Creator != serviceLogin || status.ID <= newest.ID {
			continue
		}
		newest = status
	}
	for _, state := range product.States() {
		if state.ForgeState() == string(newest.State) && state.Description() == strings.TrimSpace(newest.Description) {
			return state
		}
	}
	return product.State{}
}

func currentProductRecord(snapshot forge.Snapshot, serviceLogin string) (map[string]string, bool) {
	var newest forge.Review
	for _, review := range snapshot.Reviews {
		if review.User == serviceLogin && review.CommitID == snapshot.HeadSHA && review.ID > newest.ID {
			newest = review
		}
	}
	if newest.ID == 0 {
		return nil, false
	}
	return product.TrailingRecord(newest.Body)
}

func repoAuthorEligible(repo RepoConfig, author string) bool {
	for _, rule := range repo.Eligibility {
		if len(rule.Authors) == 0 || matchesGlobAny(rule.Authors, author) {
			return true
		}
	}
	return false
}

func governingIdentity(repo RepoConfig) string {
	raw := fmt.Sprintf("%s|auto-merge=%t|checks=%s|skill=%s|briefs=%s|body=%s", repo.Path, repo.Policy.AutoMerge, strings.Join(repo.CI.RequiredChecks, ","), repo.Adaptation.Skill, repo.Adaptation.Briefs, repo.Adaptation.RunBody)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func deploymentIdentity(cfg ServiceConfig, repo RepoConfig) string {
	forge := cfg.Forges[repo.Forge]
	sum := sha256.Sum256([]byte(repo.Forge + "|" + forge.Adaptation + "|" + forge.APIBase))
	return hex.EncodeToString(sum[:])
}

func requiredCheckSnapshotFromForge(snapshot forge.Snapshot) []reconcile.Check {
	checks := make([]reconcile.Check, 0, len(snapshot.RequiredChecks))
	for _, required := range snapshot.RequiredChecks {
		state := string(forge.StatusAbsent)
		var newest forge.Status
		for _, status := range snapshot.Statuses {
			if status.Provider == required.Provider && status.Context == required.Context && status.ID > newest.ID {
				newest = status
			}
		}
		if newest.ID > 0 {
			state = string(newest.State)
		}
		checks = append(checks, reconcile.Check{Identity: required.Provider + "/" + required.Context, Conclusion: state})
	}
	return checks
}

func ledgerView(ctx context.Context, store *ledger.Store, key ledger.Key, threshold time.Duration) (reconcile.View, error) {
	view := reconcile.View{LivenessWindow: threshold}
	if value, found, err := store.Lease(ctx, key); err != nil {
		return view, err
	} else if found {
		view.Lease = &value
	}
	if value, found, err := store.Wait(ctx, key); err != nil {
		return view, err
	} else if found {
		view.Wait = &value
	}
	if value, found, err := store.Backoff(ctx, key); err != nil {
		return view, err
	} else if found {
		view.Backoff = &value
	}
	cleanups, err := store.ListCleanup(ctx)
	if err != nil {
		return view, err
	}
	for _, value := range cleanups {
		if value.Key == key {
			copy := value
			view.Cleanup = &copy
			break
		}
	}
	return view, nil
}

func executeDecision(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, snapshot reconcile.ForgeSnapshot, decision reconcile.Decision, store *ledger.Store, logw io.Writer) error {
	if decision.ResetWorking {
		if err := publishReconciliationState(ctx, cfg, facts, snapshot, store, product.Queued()); err != nil {
			return fmt.Errorf("return stale working status to queued: %w", err)
		}
	}
	switch decision.Kind {
	case reconcile.Admit:
		if snapshot.Product == product.Working() && !decision.ResetWorking {
			if err := publishReconciliationState(ctx, cfg, facts, snapshot, store, product.Queued()); err != nil {
				return fmt.Errorf("return stale working status to queued: %w", err)
			}
		}
		if err := SpawnRun(ctx, cfg, repo, facts, snapshot.TargetSHA, facts.Occasion); err != nil {
			if errors.Is(err, ledger.ErrCapacity) {
				fmt.Fprintf(logw, "deferred %s#%s: capacity\n", facts.RepoSlug(), facts.PR)
				return nil
			}
			return err
		}
		fmt.Fprintf(logw, "admitted %s#%s head=%s\n", facts.RepoSlug(), facts.PR, facts.HeadSHA)
	case reconcile.Live, reconcile.Nothing:
		fmt.Fprintf(logw, "%s %s#%s: %s\n", decision.Kind, facts.RepoSlug(), facts.PR, decision.Reason)
	case reconcile.Reap:
		lease, found, err := store.Lease(ctx, snapshot.Key)
		if err != nil || !found {
			return err
		}
		// The old containment must be empty before its ownership and workspace
		// are retired, even though current terminal truth needs no successor.
		if err := stopAndVerifyUnitGone(ctx, lease.Unit); err != nil {
			return err
		}
		if err := recordHardKillIncident(ctx, cfg, facts, store, lease, "stale lifecycle reaped; current terminal product retained without a successor"); err != nil {
			return err
		}
		if err := closeRunLease(ctx, cfg, facts, store, lease.Token); err != nil {
			return err
		}
		if err := os.RemoveAll(lease.Workspace); err != nil {
			return err
		}
		fmt.Fprintf(logw, "reaped dead lifecycle for %s#%s; terminal product retained\n", facts.RepoSlug(), facts.PR)
	case reconcile.Replace:
		lease, found, err := store.Lease(ctx, snapshot.Key)
		if err != nil || !found {
			return err
		}
		// The old containment must be empty before a higher token is issued.
		if err := stopAndVerifyUnitGone(ctx, lease.Unit); err != nil {
			return err
		}
		if !snapshot.Open || snapshot.Merged || (snapshot.Draft && snapshot.SkipDrafts) || !snapshot.AuthorInScope {
			err := closeRunLease(ctx, cfg, facts, store, lease.Token)
			if err == nil {
				err = os.RemoveAll(lease.Workspace)
			}
			return err
		}
		forge, ok := cfg.Forges[facts.Forge]
		if !ok {
			return fmt.Errorf("unknown forge %q", facts.Forge)
		}
		adaptation, err := NewAdaptation(forge)
		if err != nil {
			return err
		}
		if err := ensureLaunchReady(ctx, cfg, facts, snapshot.TargetSHA, store); err != nil {
			return err
		}
		// A hard-killed lifecycle cannot report its own failure. Record the
		// replacement before changing ownership so a failed reap is retried
		// against the same deduplicated operational incident.
		if err := recordHardKillIncident(ctx, cfg, facts, store, lease, "stale lifecycle replaced from current forge state"); err != nil {
			return err
		}
		// Remove the old owner's presence before atomically replacing its token.
		// A forge failure leaves the old lease intact for a later reap attempt.
		if err := adaptation.removeRunClaimReaction(ctx, facts.Owner, facts.Repo, facts.PR, runPresenceReaction); err != nil {
			return err
		}
		newLease, err := store.ReplaceLease(ctx, lease.Token, ledger.Lease{Key: snapshot.Key, ObservedHead: snapshot.HeadSHA, ObservedTarget: snapshot.TargetSHA, Unit: UnitName(facts), Workspace: lease.Workspace})
		if err != nil {
			return err
		}
		if err := spawnRunUnit(ctx, cfg, repo, facts, newLease, facts.Occasion); err != nil {
			closeErr := closeRunLease(context.Background(), cfg, facts, store, newLease.Token)
			if closeErr == nil {
				closeErr = os.RemoveAll(newLease.Workspace)
			}
			return errors.Join(err, closeErr)
		}
		fmt.Fprintf(logw, "replaced dead lifecycle for %s#%s token=%d\n", facts.RepoSlug(), facts.PR, newLease.Token)
	case reconcile.CleanUp:
		settled, err := executeCleanup(ctx, cfg, facts, snapshot, store, logw)
		if err != nil {
			return err
		}
		if settled {
			// Cleanup deliberately has first refusal. Once that obligation is
			// settled, continue the same current-state reconciliation so a stale
			// lease cannot require an unrelated future prompt to be retired.
			view, err := ledgerView(ctx, store, snapshot.Key, cfg.Sweep.LivenessThreshold.Duration)
			if err != nil {
				return err
			}
			return executeDecision(ctx, cfg, repo, facts, snapshot, reconcile.Decide(snapshot, view, time.Now()), store, logw)
		}
	}
	return nil
}

func recordHardKillIncident(ctx context.Context, cfg ServiceConfig, facts Facts, store *ledger.Store, lease ledger.Lease, diagnostic string) error {
	logPath := incidentLogPath(cfg, "sweep.log")
	return recordOperationalIncident(ctx, cfg, store, ledger.Incident{
		Key: lease.Key, Category: "lifecycle-hard-kill", ObservedHead: lease.ObservedHead,
		ObservedTarget: lease.ObservedTarget, LogLocation: logPath,
	}, incidents.Event{
		Key: incidents.Key{
			Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo,
			PullRequest: facts.PR, Category: "lifecycle-hard-kill",
		},
		Diagnostic: diagnostic, LogPath: logPath, Attempt: int(lease.Token),
		ObservedHead: lease.ObservedHead, ObservedTarget: lease.ObservedTarget,
	})
}

func publishReconciliationState(ctx context.Context, cfg ServiceConfig, facts Facts, snapshot reconcile.ForgeSnapshot, store *ledger.Store, state product.State) error {
	if state == product.Queued() {
		// The ledger, not the stale status text, decides whether an owner exists.
		// Re-check at the mutation seam because another delivery may have acquired
		// the lease after the reconciliation decision was calculated.
		if _, found, err := store.Lease(ctx, snapshot.Key); err != nil {
			return err
		} else if found {
			return nil
		}
	}
	adapter, err := newBehaviouralForge(cfg, facts.Forge, ledgerForgeOwnership{store: store, key: snapshot.Key})
	if err != nil {
		return err
	}
	pullRequest, err := strconv.ParseInt(facts.PR, 10, 64)
	if err != nil {
		return err
	}
	result := adapter.SetProductStatus(ctx, forge.Guard{
		Ownership: forge.ReconciliationOwnership(), Repository: forge.Repository{Owner: facts.Owner, Name: facts.Repo},
		PullRequest: pullRequest, HeadSHA: snapshot.HeadSHA, TargetSHA: snapshot.TargetSHA,
	}, state)
	if result.Outcome != forge.WriteApplied {
		return fmt.Errorf("forge status %s: %s: %s", state.Name(), result.Outcome, result.Reason)
	}
	return nil
}

func executeCleanup(ctx context.Context, cfg ServiceConfig, facts Facts, snapshot reconcile.ForgeSnapshot, store *ledger.Store, logw io.Writer) (bool, error) {
	if snapshot.Key != coordinationKey(facts) {
		return false, fmt.Errorf("cleanup snapshot identity does not match pull request")
	}
	cleanup, found, err := store.Cleanup(ctx, snapshot.Key)
	if err != nil {
		return false, err
	}
	if !found {
		// Another reconciliation may already have settled the obligation.
		return true, nil
	}
	adapter, err := newBehaviouralForge(cfg, facts.Forge, ledgerForgeOwnership{store: store, key: snapshot.Key})
	if err != nil {
		return false, err
	}
	pullRequest, err := strconv.ParseInt(facts.PR, 10, 64)
	if err != nil {
		return false, err
	}
	result := adapter.DeleteMergedBranch(ctx, forge.Guard{
		Ownership: forge.ReconciliationOwnership(), Repository: forge.Repository{Owner: facts.Owner, Name: facts.Repo},
		PullRequest: pullRequest, HeadSHA: cleanup.MergedHead, TargetSHA: snapshot.TargetSHA,
	})
	switch result.Outcome {
	case forge.WriteApplied:
		return true, store.RemoveCleanup(ctx, snapshot.Key)
	case forge.WriteRetryable:
		if err := store.BumpCleanupAttempt(ctx, snapshot.Key); err != nil {
			return false, err
		}
		attempts := cleanup.Attempts + 1
		if attempts < cleanupRetryLimit {
			fmt.Fprintf(logw, "cleanup retained %s#%s: %s\n", facts.RepoSlug(), facts.PR, result.Reason)
			return false, nil
		}
		if err := recordIncompleteCleanupIncident(ctx, cfg, facts, snapshot, store, cleanup, result.Reason, attempts); err != nil {
			return false, err
		}
		if err := store.RemoveCleanup(ctx, snapshot.Key); err != nil {
			return false, err
		}
		fmt.Fprintf(logw, "cleanup preserved %s#%s after %d retryable refusals: %s\n", facts.RepoSlug(), facts.PR, attempts, result.Reason)
		return true, nil
	case forge.WriteUncertain:
		// Discovery could not establish whether the mutation happened. Keep the
		// obligation regardless of age; only a confirmed no-op is retry-bounded.
		if err := store.BumpCleanupAttempt(ctx, snapshot.Key); err != nil {
			return false, err
		}
		fmt.Fprintf(logw, "cleanup retained %s#%s: %s\n", facts.RepoSlug(), facts.PR, result.Reason)
		return false, nil
	case forge.WriteRejected:
		if err := store.BumpCleanupAttempt(ctx, snapshot.Key); err != nil {
			return false, err
		}
		logPath := incidentLogPath(cfg, "sweep.log")
		if err := recordOperationalIncident(ctx, cfg, store, ledger.Incident{
			Key: snapshot.Key, Category: "cleanup-unsafe", ObservedHead: cleanup.MergedHead,
			ObservedTarget: snapshot.TargetSHA, LogLocation: logPath,
		}, incidents.Event{
			Key: incidents.Key{
				Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo,
				PullRequest: facts.PR, Category: "cleanup-unsafe",
			},
			Diagnostic: "guarded branch cleanup rejected: " + result.Reason,
			LogPath:    logPath, ObservedHead: cleanup.MergedHead, ObservedTarget: snapshot.TargetSHA,
		}); err != nil {
			return false, err
		}
		// Rejection proves that deletion is unsafe, so the branch is deliberately
		// preserved under the durable operator signal. Retaining the obligation
		// would only repeat a mutation which the guard has already forbidden.
		return true, store.RemoveCleanup(ctx, snapshot.Key)
	default:
		return false, fmt.Errorf("invalid cleanup outcome %q", result.Outcome)
	}
}

func recordIncompleteCleanupIncident(ctx context.Context, cfg ServiceConfig, facts Facts, snapshot reconcile.ForgeSnapshot, store *ledger.Store, cleanup ledger.Cleanup, reason string, attempts int) error {
	logPath := incidentLogPath(cfg, "sweep.log")
	diagnostic := fmt.Sprintf("guarded branch cleanup preserved after %d retryable refusals: %s", attempts, reason)
	return recordOperationalIncident(ctx, cfg, store, ledger.Incident{
		Key: snapshot.Key, Category: "cleanup-incomplete", ObservedHead: cleanup.MergedHead,
		ObservedTarget: snapshot.TargetSHA, LogLocation: logPath,
	}, incidents.Event{
		Key: incidents.Key{
			Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo,
			PullRequest: facts.PR, Category: "cleanup-incomplete",
		},
		Diagnostic: diagnostic, LogPath: logPath, Attempt: attempts,
		ObservedHead: cleanup.MergedHead, ObservedTarget: snapshot.TargetSHA,
	})
}
