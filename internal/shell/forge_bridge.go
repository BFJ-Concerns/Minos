package shell

import (
	"context"
	"fmt"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/ledger"
)

// denyForgeMutation makes read-only snapshot construction explicit. Any
// accidental mutation through that adapter is rejected before a script runs.
type denyForgeMutation struct{}

func (denyForgeMutation) Check(context.Context, forge.Ownership) error {
	return fmt.Errorf("forge mutation is not available on the snapshot reader")
}

func newBehaviouralForge(cfg ServiceConfig, forgeName string, ownership forge.OwnershipChecker) (*forge.Adapter, error) {
	configuration, ok := cfg.Forges[forgeName]
	if !ok {
		return nil, fmt.Errorf("unknown forge %q", forgeName)
	}
	credential, err := ReadSecret(configuration.CredentialFile)
	if err != nil {
		return nil, err
	}
	return forge.NewAdapter(forge.ScriptRunner{
		Directory:  configuration.Adaptation,
		APIBase:    configuration.APIBase,
		Credential: credential,
	}, ownership, cfg.Service.BotLogin)
}

type ledgerForgeOwnership struct {
	store *ledger.Store
	key   ledger.Key
}

// ownedLease resolves both halves of lifecycle authority from the coordination
// ledger. The attempt environment records where a run began; it cannot remain
// authoritative after the owner carries a service-authored push.
func ownedLease(ctx context.Context, store *ledger.Store, key ledger.Key, token int64) (ledger.Lease, error) {
	lease, found, err := store.Lease(ctx, key)
	if err != nil {
		return ledger.Lease{}, err
	}
	if !found || lease.Token != token {
		return ledger.Lease{}, ledger.ErrNotOwner
	}
	return lease, nil
}

func (checker ledgerForgeOwnership) Check(ctx context.Context, ownership forge.Ownership) error {
	switch ownership.Kind {
	case forge.OwnershipReconciliation:
		return nil
	case forge.OwnershipLifecycle:
		if ownership.Token <= 0 {
			return fmt.Errorf("invalid lifecycle ownership token")
		}
		_, err := ownedLease(ctx, checker.store, checker.key, ownership.Token)
		return err
	default:
		return fmt.Errorf("invalid forge ownership kind %q", ownership.Kind)
	}
}
