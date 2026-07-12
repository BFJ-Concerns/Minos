package shell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"bfj/minos/internal/ledger"
	"bfj/minos/internal/reconcile"
)

func buildSnapshot(ctx context.Context, cfg ServiceConfig, repo RepoConfig, adaptation Adaptation, facts Facts) (reconcile.ForgeSnapshot, error) {
	current, err := adaptation.GetPRFacts(ctx, facts.Forge, facts.Owner, facts.Repo, facts.PR)
	if err != nil {
		return reconcile.ForgeSnapshot{}, err
	}
	current.Forge = facts.Forge
	current.Occasion = facts.Occasion
	statuses, err := adaptation.GetStatuses(ctx, current.Owner, current.Repo, current.HeadSHA)
	if err != nil {
		return reconcile.ForgeSnapshot{}, err
	}
	status, _ := statusForContext(statuses, "Minos")
	target := current.BaseSHA
	targetKnown := target != ""
	// The legacy adaptation exposes only the target branch name. Preserve an
	// explicit unknown rather than mistaking a mutable ref name for a revision.
	if !targetKnown {
		target = "unknown:" + current.BaseRef
		log.Printf("coordination degraded: target revision unknown forge=%s repo=%s pr=%s target-branch=%s", current.Forge, current.RepoSlug(), current.PR, current.BaseRef)
	}
	snapshot := reconcile.ForgeSnapshot{
		Key: coordinationKey(current), HeadSHA: current.HeadSHA, TargetBranch: current.BaseRef,
		TargetSHA: target, TargetKnown: targetKnown, Open: current.Open, Merged: current.Merged,
		Draft: current.Draft, SkipDrafts: true, AuthorInScope: repoAuthorEligible(repo, current.Author),
		Occasion: facts.Occasion, Product: productState(status), ProductHead: current.HeadSHA,
		ProductTarget: target, GoverningIdentity: governingIdentity(repo), DeploymentProfile: deploymentIdentity(cfg, repo),
		RequiredChecks: requiredCheckSnapshot(repo, statuses),
	}
	// The current product-status read carries no authenticated identity for the
	// governing inputs which produced it. Keep that provenance explicitly unknown
	// so a clean result cannot silently suppress the commissioned re-entry axis.
	if snapshot.Product != reconcile.ProductNone {
		snapshot.ProductGoverning = "unknown"
		snapshot.ProductGoverningKnown = false
	}
	return snapshot, nil
}

func productState(status Status) reconcile.ProductState {
	description := strings.TrimSpace(status.Description)
	switch description {
	case "Waiting for review":
		return reconcile.ProductQueued
	case "Reviewing changes":
		return reconcile.ProductWorking
	case "Waiting for checks":
		return reconcile.ProductWaiting
	case "Changes need attention":
		return reconcile.ProductBlocked
	case "Review incomplete":
		return reconcile.ProductPartial
	case "Review stopped; findings remain":
		return reconcile.ProductStopped
	case "Changes approved":
		return reconcile.ProductClean
	case "Changes approved; verification limited":
		return reconcile.ProductCleanLimited
	case "Merged":
		return reconcile.ProductMerged
	default:
		return reconcile.ProductNone
	}
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

func requiredCheckSnapshot(repo RepoConfig, statuses []Status) []reconcile.Check {
	wanted := make(map[string]bool, len(repo.CI.RequiredChecks))
	for _, name := range repo.CI.RequiredChecks {
		wanted[name] = true
	}
	newest := make(map[string]Status)
	for _, status := range statuses {
		if !wanted[status.Context] || status.Context == "Minos" {
			continue
		}
		if prior, ok := newest[status.Context]; !ok || status.ID > prior.ID {
			newest[status.Context] = status
		}
	}
	checks := make([]reconcile.Check, 0, len(repo.CI.RequiredChecks))
	for _, name := range repo.CI.RequiredChecks {
		state := "absent"
		if status, ok := newest[name]; ok {
			state = strings.ToLower(status.State)
		}
		checks = append(checks, reconcile.Check{Identity: name, Conclusion: state})
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
	switch decision.Kind {
	case reconcile.Admit:
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
			_, err := store.ReleaseLease(ctx, lease.Key, lease.Token)
			if err == nil {
				err = os.RemoveAll(lease.Workspace)
			}
			return err
		}
		newLease, err := store.ReplaceLease(ctx, lease.Token, ledger.Lease{Key: snapshot.Key, ObservedHead: snapshot.HeadSHA, ObservedTarget: snapshot.TargetSHA, Unit: UnitName(facts), Workspace: lease.Workspace})
		if err != nil {
			return err
		}
		if err := spawnRunUnit(ctx, cfg, repo, facts, newLease, facts.Occasion); err != nil {
			_, _ = store.ReleaseLease(context.Background(), newLease.Key, newLease.Token)
			_ = os.RemoveAll(newLease.Workspace)
			return err
		}
		fmt.Fprintf(logw, "replaced dead lifecycle for %s#%s token=%d\n", facts.RepoSlug(), facts.PR, newLease.Token)
	case reconcile.CleanUp:
		// The behavioural adapter owns the expected-head delete. Until it lands,
		// retain the obligation rather than issuing an unsafe read-then-delete.
		return fmt.Errorf("guarded branch cleanup adapter is unavailable")
	}
	return nil
}
