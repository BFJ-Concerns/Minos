package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"bfj/minos/internal/findings"
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
		if len(args) != 5 && len(args) != 6 {
			return fmt.Errorf("usage: minos forge review BODY_FILE COMMENTS_JSON MANIFEST_JSON BAR_ATTESTATION_JSON [permission-policy]")
		}
		manifest, _, result, err := reviewDecision(args[3], args[4], repo, facts, guard, token)
		if err != nil {
			return err
		}
		blockKind := reconcile.BlockKind("")
		if result == product.ReviewHasMaterialFindings() {
			blockKind = reconcile.FindingBlock
		}
		if len(args) == 6 {
			if args[5] != string(reconcile.PermissionBlock) {
				return fmt.Errorf("unknown review block kind %q", args[5])
			}
			blockKind = reconcile.PermissionBlock
		}
		body, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		if _, exists := product.TrailingRecord(string(body)); exists {
			return fmt.Errorf("review body already has a Minos product record")
		}
		comments, err := readManifestComments(args[2], manifest)
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
		index, err := manifest.Index()
		if err != nil {
			return err
		}
		indexLine, err := index.RecordLine()
		if err != nil {
			return err
		}
		reviewBody := strings.TrimRight(string(body), "\r\n")
		if manifest.Policy.Mode == findings.PublishThroughP3Mode && hasDisclosedFinding(manifest) {
			reviewBody += fmt.Sprintf("\n\n## Quieter verified findings\n\nRepository policy discloses every verified finding through P3; only findings at or above %s are material or merge-blocking.", manifest.Policy.PublishThreshold)
		}
		reviewBody += "\n\n" + indexLine + "\n" + line
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

type manifestComment struct {
	OccurrenceID findings.OccurrenceID `json:"occurrence_id"`
	NewPosition  int64                 `json:"new_position"`
	OldPosition  int64                 `json:"old_position"`
}

func readManifestComments(path string, manifest findings.DispositionManifest) ([]forge.ReviewComment, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var supplied []manifestComment
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&supplied); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("trailing review comment JSON")
	}
	byOccurrence := make(map[findings.OccurrenceID]manifestComment, len(supplied))
	for _, comment := range supplied {
		if !comment.OccurrenceID.Valid() || comment.NewPosition < 0 || comment.OldPosition < 0 || comment.NewPosition == 0 && comment.OldPosition == 0 {
			return nil, fmt.Errorf("invalid manifest comment for occurrence %s", comment.OccurrenceID)
		}
		if _, duplicate := byOccurrence[comment.OccurrenceID]; duplicate {
			return nil, fmt.Errorf("duplicate manifest comment for occurrence %s", comment.OccurrenceID)
		}
		byOccurrence[comment.OccurrenceID] = comment
	}
	comments := make([]forge.ReviewComment, 0, len(supplied))
	for _, entry := range manifest.Findings {
		if entry.Publication != findings.PublicationPublished && entry.Publication != findings.PublicationDisclosed {
			continue
		}
		comment, exists := byOccurrence[entry.Finding.OccurrenceID]
		if !exists {
			return nil, fmt.Errorf("published occurrence %s has no review comment", entry.Finding.OccurrenceID)
		}
		delete(byOccurrence, entry.Finding.OccurrenceID)
		comments = append(comments, forge.ReviewComment{Path: entry.Finding.Anchor.Path, Body: renderFindingComment(entry), NewPosition: comment.NewPosition, OldPosition: comment.OldPosition})
	}
	if len(byOccurrence) != 0 {
		return nil, fmt.Errorf("review comments contain an occurrence outside the policy publication set")
	}
	return comments, nil
}

func renderFindingComment(entry findings.FindingDisposition) string {
	parts := []string{fmt.Sprintf("**[%s] %s**", entry.Finding.VerifiedPriority, entry.Finding.Title), entry.Finding.Message}
	if entry.Finding.Suggestion != "" {
		parts = append(parts, "Suggestion: "+entry.Finding.Suggestion)
	}
	parts = append(parts, fmt.Sprintf("<!-- Minos: finding=%s occurrence=%s -->", entry.Finding.LineageID, entry.Finding.OccurrenceID))
	return strings.Join(parts, "\n\n")
}

func reviewDecision(manifestPath, attestationPath string, repo RepoConfig, facts Facts, guard forge.Guard, token int64) (findings.DispositionManifest, findings.BarAttestation, product.ReviewResult, error) {
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return findings.DispositionManifest{}, findings.BarAttestation{}, product.ReviewResult{}, err
	}
	manifest, err := findings.DecodeManifest(manifestData)
	if err != nil {
		return findings.DispositionManifest{}, findings.BarAttestation{}, product.ReviewResult{}, err
	}
	if manifest.Context.Forge != facts.Forge || manifest.Context.Owner != facts.Owner || manifest.Context.Repo != facts.Repo || strconv.FormatInt(manifest.Context.PullRequest, 10) != facts.PR || manifest.Context.HeadSHA != guard.HeadSHA || manifest.Context.TargetSHA != guard.TargetSHA || manifest.Context.AttemptToken != token || manifest.Context.GoverningIdentity != governingIdentity(repo) || manifest.Policy != repo.FindingPolicy() {
		return findings.DispositionManifest{}, findings.BarAttestation{}, product.ReviewResult{}, fmt.Errorf("disposition manifest does not match the currently owned repository, revisions, policy, or governing identity")
	}
	attestationData, err := os.ReadFile(attestationPath)
	if err != nil {
		return findings.DispositionManifest{}, findings.BarAttestation{}, product.ReviewResult{}, err
	}
	attestation, err := findings.DecodeBarAttestation(attestationData, manifest)
	if err != nil {
		return findings.DispositionManifest{}, findings.BarAttestation{}, product.ReviewResult{}, err
	}
	if manifest.HasMaterialFindings() && manifest.HasConfirmedQuietDelivery() {
		return findings.DispositionManifest{}, findings.BarAttestation{}, product.ReviewResult{}, fmt.Errorf("confirmed material-head manifest is delivery evidence, not a replacement decision input")
	}
	if attestation.Verdict != findings.BarPass {
		return manifest, attestation, product.ReviewIncomplete(), nil
	}
	if manifest.HasMaterialFindings() {
		return manifest, attestation, product.ReviewHasMaterialFindings(), nil
	}
	if err := manifest.ConvergenceReady(attestation); err != nil {
		return manifest, attestation, product.ReviewIncomplete(), nil
	}
	return manifest, attestation, product.ReviewConverged(), nil
}

func hasDisclosedFinding(manifest findings.DispositionManifest) bool {
	for _, entry := range manifest.Findings {
		if entry.Publication == findings.PublicationDisclosed {
			return true
		}
	}
	return false
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
