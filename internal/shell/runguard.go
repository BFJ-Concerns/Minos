package shell

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"bfj/minos/internal/ledger"
)

const runPresenceReaction = "eyes"

func RunGuardCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run-guard", flag.ContinueOnError)
	configRoot := fs.String("config", DefaultConfigRoot, "configuration root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: minos run-guard [--config root] begin|current|release|advance|clearance|wait")
	}
	_, adaptation, facts, store, token, err := loadRunGuard(*configRoot)
	if err != nil {
		return err
	}
	defer store.Close()
	switch fs.Arg(0) {
	case "begin":
		return beginRun(ctx, adaptation, facts, store, token)
	case "current":
		return currentRun(ctx, adaptation, facts, store, token)
	case "release":
		return releaseRun(ctx, adaptation, facts, store, token)
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
		if err := store.SetWait(ctx, ledger.Wait{Key: coordinationKey(facts), Fingerprint: fs.Arg(1), FailsafeAt: failsafe}); err != nil {
			return err
		}
		fmt.Println("waiting")
		return nil
	default:
		return fmt.Errorf("unknown run-guard action %q", fs.Arg(0))
	}
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

func beginRun(ctx context.Context, adaptation Adaptation, facts Facts, store *ledger.Store, token int64) error {
	current, err := currentAttempt(ctx, adaptation, facts, store, token)
	if err != nil {
		return err
	}
	if !current {
		fmt.Println("stale")
		return nil
	}
	if err := adaptation.addRunClaimReaction(ctx, facts.Owner, facts.Repo, facts.PR, runPresenceReaction); err != nil {
		return err
	}
	fmt.Println("claimed")
	return nil
}

func currentRun(ctx context.Context, adaptation Adaptation, facts Facts, store *ledger.Store, token int64) error {
	current, err := currentAttempt(ctx, adaptation, facts, store, token)
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

func currentAttempt(ctx context.Context, adaptation Adaptation, facts Facts, store *ledger.Store, token int64) (bool, error) {
	lease, found, err := store.Lease(ctx, coordinationKey(facts))
	if err != nil || !found || lease.Token != token {
		return false, err
	}
	current, err := adaptation.GetPRFacts(ctx, facts.Forge, facts.Owner, facts.Repo, facts.PR)
	if err != nil {
		return false, err
	}
	if current.HeadSHA != lease.ObservedHead {
		return false, nil
	}
	// Until the adapter supplies BASE_SHA, the lease still fences target-aware
	// mutations through the target observed at launch; unknown is degraded, not
	// guessed from a branch name.
	if current.BaseSHA != "" && current.BaseSHA != lease.ObservedTarget {
		return false, nil
	}
	return true, nil
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
