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

func (checker ledgerForgeOwnership) Check(ctx context.Context, ownership forge.Ownership) error {
	switch ownership.Kind {
	case forge.OwnershipReconciliation:
		return nil
	case forge.OwnershipLifecycle:
		if ownership.Token <= 0 {
			return fmt.Errorf("invalid lifecycle ownership token")
		}
		owned, err := checker.store.Owns(ctx, checker.key, ownership.Token)
		if err != nil {
			return err
		}
		if !owned {
			return ledger.ErrNotOwner
		}
		return nil
	default:
		return fmt.Errorf("invalid forge ownership kind %q", ownership.Kind)
	}
}
