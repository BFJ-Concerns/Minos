package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/ledger"
	"bfj/minos/internal/product"
	"bfj/minos/internal/reconcile"
)

// ForgeCommand is the accountable lead's typed route to consequential forge
// operations. Every mutation passes through the behavioural adapter and the
// current ledger fence rather than exposing a script path as authority.
func ForgeCommand(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: minos forge snapshot|wait-fingerprint|status|review|push|merge|cleanup [arguments]")
	}
	cfg, repo, facts, store, token, adapter, guard, err := loadForgeCommand(ctx)
	if err != nil {
		return err
	}
	defer store.Close()

	switch args[0] {
	case "snapshot":
		if len(args) != 1 {
			return fmt.Errorf("usage: minos forge snapshot")
		}
		snapshot, err := adapter.Snapshot(ctx, guard.Repository, guard.PullRequest)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(snapshot)
	case "wait-fingerprint":
		if len(args) != 1 {
			return fmt.Errorf("usage: minos forge wait-fingerprint")
		}
		snapshot, err := buildSnapshot(ctx, cfg, repo, facts)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, reconcile.WaitFingerprint(snapshot))
		return err
	case "status":
		if len(args) != 2 {
			return fmt.Errorf("usage: minos forge status PRODUCT_STATE")
		}
		state, ok := namedProductState(args[1])
		if !ok {
			return fmt.Errorf("unknown product state %q", args[1])
		}
		return emitForgeResult(stdout, "status", adapter.SetProductStatus(ctx, guard, state))
	case "review":
		if len(args) != 4 && len(args) != 5 {
			return fmt.Errorf("usage: minos forge review converged|material|incomplete BODY_FILE COMMENTS_JSON [permission-policy]")
		}
		result, blockKind, err := namedReviewResult(args[1], args[4:])
		if err != nil {
			return err
		}
		body, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		if _, exists := product.TrailingRecord(string(body)); exists {
			return fmt.Errorf("review body already has a Minos product record")
		}
		comments, err := readReviewComments(args[3])
		if err != nil {
			return err
		}
		verdict, err := product.VerdictFor(result)
		if err != nil {
			return err
		}
		record := map[string]string{
			"governing": governingIdentity(repo),
			"head":      guard.HeadSHA,
			"target":    guard.TargetSHA,
		}
		if blockKind != "" {
			record["block-kind"] = string(blockKind)
			if blockKind == reconcile.PermissionBlock {
				snapshot := reconcile.ForgeSnapshot{GoverningIdentity: governingIdentity(repo), DeploymentProfile: deploymentIdentity(cfg, repo)}
				record["block-identity"] = reconcile.BlockReentryIdentity(snapshot)
			}
		}
		line, err := product.FormatRecord(record)
		if err != nil {
			return err
		}
		reviewBody := strings.TrimRight(string(body), "\r\n") + "\n\n" + line
		return emitForgeResult(stdout, "review", adapter.PostReview(ctx, guard, forgeVerdict(verdict), reviewBody, comments))
	case "push":
		if len(args) != 5 {
			return fmt.Errorf("usage: minos forge push BRANCH AUTHOR_NAME AUTHOR_EMAIL MESSAGE_FILE")
		}
		result := adapter.Push(ctx, guard, forge.PushRequest{
			Branch: args[1], AuthorName: args[2], AuthorEmail: args[3], MessageFile: args[4], Workspace: os.Getenv("MINOS_WORKSPACE"),
		})
		if result.Outcome == forge.WriteApplied {
			updated, updateErr := store.UpdateObservedPair(ctx, coordinationKey(facts), token, guard.HeadSHA, guard.TargetSHA, result.SHA, guard.TargetSHA)
			if updateErr != nil {
				return updateErr
			}
			if !updated {
				return ledger.ErrNotOwner
			}
		}
		return emitForgeResult(stdout, "push", result)
	case "merge":
		if len(args) != 2 {
			return fmt.Errorf("usage: minos forge merge METHOD")
		}
		result := adapter.Merge(ctx, guard, forge.MergeMethod(args[1]))
		if result.Outcome == forge.WriteApplied {
			// Merged truth comes from a fresh snapshot. Never call Merge again merely
			// to ask whether the first write took effect.
			snapshot, snapshotErr := adapter.Snapshot(ctx, guard.Repository, guard.PullRequest)
			if snapshotErr != nil {
				return snapshotErr
			}
			if !snapshot.Merged || snapshot.HeadSHA != guard.HeadSHA {
				return fmt.Errorf("merge reported applied without matching merged snapshot")
			}
			if err := store.AddCleanup(ctx, ledger.Cleanup{Key: coordinationKey(facts), MergedHead: snapshot.HeadSHA, Branch: snapshot.HeadBranch}); err != nil {
				return err
			}
		}
		return emitForgeResult(stdout, "merge", result)
	case "cleanup":
		if len(args) != 1 {
			return fmt.Errorf("usage: minos forge cleanup")
		}
		snapshot, err := adapter.Snapshot(ctx, guard.Repository, guard.PullRequest)
		if err != nil {
			return err
		}
		if !snapshot.Merged || snapshot.HeadSHA != guard.HeadSHA {
			return fmt.Errorf("cleanup requires a confirmed merge of the observed head")
		}
		result := adapter.DeleteMergedBranch(ctx, guard)
		switch result.Outcome {
		case forge.WriteApplied:
			if err := store.RemoveCleanup(ctx, coordinationKey(facts)); err != nil {
				return err
			}
		case forge.WriteUncertain, forge.WriteRejected:
			if err := store.BumpCleanupAttempt(ctx, coordinationKey(facts)); err != nil {
				return err
			}
		}
		return emitForgeResult(stdout, "cleanup", result)
	default:
		return fmt.Errorf("unknown forge action %q", args[0])
	}
}

func loadForgeCommand(ctx context.Context) (ServiceConfig, RepoConfig, Facts, *ledger.Store, int64, *forge.Adapter, forge.Guard, error) {
	cfg, err := LoadServiceConfig(os.Getenv("MINOS_CONFIG"))
	if err != nil {
		return ServiceConfig{}, RepoConfig{}, Facts{}, nil, 0, nil, forge.Guard{}, err
	}
	facts := envFacts(os.Getenv("MINOS_FORGE"))
	repo, err := FindRepoConfig(cfg, facts)
	if err != nil {
		return ServiceConfig{}, RepoConfig{}, Facts{}, nil, 0, nil, forge.Guard{}, err
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		return ServiceConfig{}, RepoConfig{}, Facts{}, nil, 0, nil, forge.Guard{}, err
	}
	token, err := attemptToken()
	if err != nil {
		store.Close()
		return ServiceConfig{}, RepoConfig{}, Facts{}, nil, 0, nil, forge.Guard{}, err
	}
	lease, err := ownedLease(ctx, store, coordinationKey(facts), token)
	if err != nil {
		store.Close()
		return ServiceConfig{}, RepoConfig{}, Facts{}, nil, 0, nil, forge.Guard{}, err
	}
	adapter, err := newBehaviouralForge(cfg, facts.Forge, ledgerForgeOwnership{store: store, key: coordinationKey(facts)})
	if err != nil {
		store.Close()
		return ServiceConfig{}, RepoConfig{}, Facts{}, nil, 0, nil, forge.Guard{}, err
	}
	pullRequest, err := strconv.ParseInt(facts.PR, 10, 64)
	if err != nil {
		store.Close()
		return ServiceConfig{}, RepoConfig{}, Facts{}, nil, 0, nil, forge.Guard{}, err
	}
	guard := forge.Guard{
		Ownership: forge.LifecycleOwnership(token), Repository: forge.Repository{Owner: facts.Owner, Name: facts.Repo},
		PullRequest: pullRequest, HeadSHA: lease.ObservedHead, TargetSHA: lease.ObservedTarget,
	}
	return cfg, repo, facts, store, token, adapter, guard, nil
}

func namedProductState(name string) (product.State, bool) {
	for _, state := range product.States() {
		if state.Name() == name {
			return state, true
		}
	}
	return product.State{}, false
}

func namedReviewResult(name string, optionalBlock []string) (product.ReviewResult, reconcile.BlockKind, error) {
	var result product.ReviewResult
	var blockKind reconcile.BlockKind
	switch name {
	case "converged":
		result = product.ReviewConverged()
	case "material":
		result = product.ReviewHasMaterialFindings()
		blockKind = reconcile.FindingBlock
	case "incomplete":
		result = product.ReviewIncomplete()
	default:
		return product.ReviewResult{}, "", fmt.Errorf("unknown review result %q", name)
	}
	if len(optionalBlock) == 1 {
		if optionalBlock[0] != string(reconcile.PermissionBlock) {
			return product.ReviewResult{}, "", fmt.Errorf("unknown review block kind %q", optionalBlock[0])
		}
		blockKind = reconcile.PermissionBlock
	}
	return result, blockKind, nil
}

func forgeVerdict(verdict product.Verdict) forge.ReviewVerdict {
	switch verdict.Name() {
	case "approve":
		return forge.ReviewApprove
	case "request-changes":
		return forge.ReviewRequestChanges
	default:
		return forge.ReviewVerdictComment
	}
}

func readReviewComments(path string) ([]forge.ReviewComment, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var comments []forge.ReviewComment
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&comments); err != nil {
		return nil, err
	}
	return comments, nil
}

func emitForgeResult(stdout io.Writer, operation string, result forge.WriteResult) error {
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return err
	}
	if result.Outcome != forge.WriteApplied {
		return fmt.Errorf("%s %s: %s", operation, result.Outcome, result.Reason)
	}
	return nil
}
