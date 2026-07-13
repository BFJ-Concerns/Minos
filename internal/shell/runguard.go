package shell

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/ledger"
	"bfj/minos/internal/product"
)

const runPresenceReaction = "eyes"

func RunGuardCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run-guard", flag.ContinueOnError)
	configRoot := fs.String("config", DefaultConfigRoot, "configuration root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: minos run-guard [--config root] begin|current|release|retryable-exit|advance|clearance|wait|revalidate")
	}
	cfg, adaptation, facts, store, token, err := loadRunGuard(*configRoot)
	if err != nil {
		return err
	}
	defer store.Close()
	switch fs.Arg(0) {
	case "begin":
		return beginRun(ctx, cfg, adaptation, facts, store, token)
	case "current":
		return currentRun(ctx, cfg, facts, store, token)
	case "release":
		return releaseRun(ctx, adaptation, facts, store, token)
	case "retryable-exit":
		if fs.NArg() != 2 {
			return fmt.Errorf("usage: minos run-guard retryable-exit FAILURE_CATEGORY")
		}
		return declareRetryableExit(ctx, adaptation, facts, store, token, fs.Arg(1))
	case "advance":
		if fs.NArg() != 5 {
			return fmt.Errorf("usage: minos run-guard advance OLD_HEAD OLD_TARGET NEW_HEAD NEW_TARGET")
		}
		updated, err := store.UpdateObservedPair(ctx, coordinationKey(facts), token, fs.Arg(1), fs.Arg(2), fs.Arg(3), fs.Arg(4))
		if err != nil {
			return err
		}
		if !updated {
			return ledger.ErrNotOwner
		}
		fmt.Println("advanced")
		return nil
	case "clearance":
		if fs.NArg() != 3 {
			return fmt.Errorf("usage: minos run-guard clearance HEAD TARGET")
		}
		set, err := store.SetClearance(ctx, coordinationKey(facts), token, fs.Arg(1), fs.Arg(2))
		if err != nil {
			return err
		}
		if !set {
			return ledger.ErrNotOwner
		}
		fmt.Println("cleared")
		return nil
	case "wait":
		if fs.NArg() < 2 || fs.NArg() > 3 {
			return fmt.Errorf("usage: minos run-guard wait FINGERPRINT [FAILSAFE_RFC3339]")
		}
		var failsafe *time.Time
		if fs.NArg() == 3 {
			parsed, err := time.Parse(time.RFC3339Nano, fs.Arg(2))
			if err != nil {
				return err
			}
			failsafe = &parsed
		}
		set, err := store.SetWait(ctx, ledger.Wait{Key: coordinationKey(facts), Fingerprint: fs.Arg(1), FailsafeAt: failsafe}, token)
		if err != nil {
			return err
		}
		if !set {
			return ledger.ErrNotOwner
		}
		fmt.Println("waiting")
		return nil
	case "revalidate":
		if fs.NArg() != 2 {
			return fmt.Errorf("usage: minos run-guard revalidate LIFECYCLE_INDEX")
		}
		return revalidateAfterCompaction(ctx, cfg, facts, store, token, fs.Arg(1))
	default:
		return fmt.Errorf("unknown run-guard action %q", fs.Arg(0))
	}
}

func revalidateAfterCompaction(ctx context.Context, cfg ServiceConfig, facts Facts, store *ledger.Store, token int64, indexPath string) error {
	current, err := currentAttempt(ctx, cfg, facts, store, token)
	if err != nil {
		return err
	}
	if !current {
		return ledger.ErrNotOwner
	}
	repo, err := FindRepoConfig(cfg.Root, facts)
	if err != nil {
		return err
	}
	if os.Getenv("MINOS_GOVERNING_IDENTITY") != governingIdentity(repo) || os.Getenv("MINOS_DEPLOYMENT_PROFILE") != deploymentIdentity(cfg, repo) {
		return fmt.Errorf("trusted lifecycle inputs changed after compaction")
	}
	runDir, err := filepath.Abs(os.Getenv("MINOS_RUN_DIR"))
	if err != nil || strings.TrimSpace(runDir) == "" {
		return fmt.Errorf("MINOS_RUN_DIR is required")
	}
	index, err := filepath.Abs(indexPath)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(runDir, index)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("lifecycle index must be a file inside MINOS_RUN_DIR")
	}
	for _, path := range []string{index, filepath.Join(runDir, "resolved-lead.json")} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("required revalidation evidence %s is unavailable", path)
		}
	}
	fmt.Println("revalidated")
	return nil
}

func loadRunGuard(configRoot string) (ServiceConfig, Adaptation, Facts, *ledger.Store, int64, error) {
	cfg, err := LoadServiceConfig(configRoot)
	if err != nil {
		return ServiceConfig{}, Adaptation{}, Facts{}, nil, 0, err
	}
	forgeName := os.Getenv("MINOS_FORGE")
	forge, ok := cfg.Forges[forgeName]
	if !ok {
		return ServiceConfig{}, Adaptation{}, Facts{}, nil, 0, fmt.Errorf("unknown forge %q", forgeName)
	}
	adaptation, err := NewAdaptation(forge)
	if err != nil {
		return ServiceConfig{}, Adaptation{}, Facts{}, nil, 0, err
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		return ServiceConfig{}, Adaptation{}, Facts{}, nil, 0, err
	}
	token, err := attemptToken()
	if err != nil {
		store.Close()
		return ServiceConfig{}, Adaptation{}, Facts{}, nil, 0, err
	}
	return cfg, adaptation, envFacts(forgeName), store, token, nil
}

func beginRun(ctx context.Context, cfg ServiceConfig, adaptation Adaptation, facts Facts, store *ledger.Store, token int64) error {
	current, err := currentAttempt(ctx, cfg, facts, store, token)
	if err != nil {
		return err
	}
	if !current {
		fmt.Println("stale")
		return nil
	}
	if _, err := store.ClearWait(ctx, coordinationKey(facts), token); err != nil {
		return err
	}
	if err := adaptation.addRunClaimReaction(ctx, facts.Owner, facts.Repo, facts.PR, runPresenceReaction); err != nil {
		return err
	}
	if err := publishLifecycleState(ctx, cfg, facts, store, token, product.Working()); err != nil {
		return errors.Join(err, adaptation.removeRunClaimReaction(context.Background(), facts.Owner, facts.Repo, facts.PR, runPresenceReaction))
	}
	fmt.Println("claimed")
	return nil
}

func currentRun(ctx context.Context, cfg ServiceConfig, facts Facts, store *ledger.Store, token int64) error {
	current, err := currentAttempt(ctx, cfg, facts, store, token)
	if err != nil {
		return err
	}
	if current {
		fmt.Println("current")
	} else {
		fmt.Println("stale")
	}
	return nil
}

func currentAttempt(ctx context.Context, cfg ServiceConfig, facts Facts, store *ledger.Store, token int64) (bool, error) {
	lease, err := ownedLease(ctx, store, coordinationKey(facts), token)
	if errors.Is(err, ledger.ErrNotOwner) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	adapter, err := newBehaviouralForge(cfg, facts.Forge, ledgerForgeOwnership{store: store, key: coordinationKey(facts)})
	if err != nil {
		return false, err
	}
	pullRequest, err := strconv.ParseInt(facts.PR, 10, 64)
	if err != nil {
		return false, err
	}
	current, err := adapter.Snapshot(ctx, forge.Repository{Owner: facts.Owner, Name: facts.Repo}, pullRequest)
	if err != nil {
		return false, err
	}
	if current.HeadSHA != lease.ObservedHead {
		return false, nil
	}
	if current.TargetSHA != lease.ObservedTarget {
		return false, nil
	}
	return true, nil
}

func publishLifecycleState(ctx context.Context, cfg ServiceConfig, facts Facts, store *ledger.Store, token int64, state product.State) error {
	lease, err := ownedLease(ctx, store, coordinationKey(facts), token)
	if err != nil {
		return err
	}
	adapter, err := newBehaviouralForge(cfg, facts.Forge, ledgerForgeOwnership{store: store, key: coordinationKey(facts)})
	if err != nil {
		return err
	}
	pullRequest, err := strconv.ParseInt(facts.PR, 10, 64)
	if err != nil {
		return err
	}
	result := adapter.SetProductStatus(ctx, forge.Guard{
		Ownership: forge.LifecycleOwnership(token), Repository: forge.Repository{Owner: facts.Owner, Name: facts.Repo},
		PullRequest: pullRequest, HeadSHA: lease.ObservedHead, TargetSHA: lease.ObservedTarget,
	}, state)
	if result.Outcome != forge.WriteApplied {
		return fmt.Errorf("forge status %s: %s: %s", state.Name(), result.Outcome, result.Reason)
	}
	return nil
}

func releaseRun(ctx context.Context, adaptation Adaptation, facts Facts, store *ledger.Store, token int64) error {
	owned, err := store.Owns(ctx, coordinationKey(facts), token)
	if err != nil {
		return err
	}
	if !owned {
		fmt.Println("stale")
		return nil
	}
	if err := adaptation.removeRunClaimReaction(ctx, facts.Owner, facts.Repo, facts.PR, runPresenceReaction); err != nil {
		return err
	}
	fmt.Println("released")
	return nil
}

// closeRunLease keeps the forge presence gesture and the coordination row on
// one mechanical exit seam. Reaction removal happens first: if the forge is
// unavailable, retaining the lease is safer than advertising free capacity
// while eyes still claim that Minos owns the pull request.
func closeRunLease(ctx context.Context, cfg ServiceConfig, facts Facts, store *ledger.Store, token int64) error {
	owned, err := store.Owns(ctx, coordinationKey(facts), token)
	if err != nil {
		return err
	}
	if !owned {
		return ledger.ErrNotOwner
	}
	forge, ok := cfg.Forges[facts.Forge]
	if !ok {
		return fmt.Errorf("unknown forge %q", facts.Forge)
	}
	adaptation, err := NewAdaptation(forge)
	if err != nil {
		return err
	}
	if err := adaptation.removeRunClaimReaction(ctx, facts.Owner, facts.Repo, facts.PR, runPresenceReaction); err != nil {
		return err
	}
	released, err := store.ReleaseLease(ctx, coordinationKey(facts), token)
	if err != nil {
		return err
	}
	if !released {
		return ledger.ErrNotOwner
	}
	return nil
}
