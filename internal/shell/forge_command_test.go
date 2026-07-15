package shell

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"bfj/minos/internal/findings"
	"bfj/minos/internal/forge"
	"bfj/minos/internal/ledger"
	"bfj/minos/internal/product"
	"bfj/minos/internal/reconcile"
)

func forgeCommandAttempt(t *testing.T) (ServiceConfig, Facts, ledger.Lease) {
	t.Helper()
	cfg, _, facts := coordinationConfig(t)
	if err := os.MkdirAll(filepath.Join(cfg.Root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := "forge=\"local\"\nowner=\"owner\"\nrepo=\"subject\"\n[adaptation]\nbuild=\"true\"\ntest=\"true\"\nskill=\"skill\"\n[finding-disposition]\nmode=\"publish-through-p3\"\n[[eligibility]]\nauthors=[\"*\"]\n"
	if err := os.WriteFile(filepath.Join(cfg.Root, "repos", "subject.toml"), []byte(repoConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(t.Context(), ledger.Lease{Key: coordinationKey(facts), ObservedHead: facts.HeadSHA, ObservedTarget: "target-1", Unit: "unit", Workspace: t.TempDir()}, 2)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)
	t.Setenv("MINOS_PR", facts.PR)
	t.Setenv("MINOS_HEAD_SHA", facts.HeadSHA)
	t.Setenv("MINOS_TARGET_SHA", "target-1")
	t.Setenv("MINOS_BASE_REF", facts.BaseRef)
	t.Setenv("MINOS_ATTEMPT_TOKEN", strconv.FormatInt(lease.Token, 10))
	t.Setenv("MINOS_WORKSPACE", lease.Workspace)
	return cfg, facts, lease
}

func TestForgeCommandPublishesOnlyNamedProductState(t *testing.T) {
	cfg, _, _ := forgeCommandAttempt(t)
	captured := filepath.Join(t.TempDir(), "status")
	writeScript(t, filepath.Join(cfg.Forges["local"].Adaptation, "guarded-set-status"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >"+strconv.Quote(captured)+"\nprintf '{\"outcome\":\"applied\"}\\n'\n")
	var out bytes.Buffer
	if err := ForgeCommand(t.Context(), []string{"status", "working"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(captured)
	if err != nil || !strings.Contains(string(data), "Minos pending Reviewing changes") {
		t.Fatalf("status arguments=%q err=%v", data, err)
	}
	if err := ForgeCommand(t.Context(), []string{"status", "invented"}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("invented product state was accepted")
	}
}

func TestForgeCommandReviewAddsAuthenticatedProductRecord(t *testing.T) {
	cfg, facts, _ := forgeCommandAttempt(t)
	captured := filepath.Join(t.TempDir(), "review")
	writeScript(t, filepath.Join(cfg.Forges["local"].Adaptation, "guarded-post-review"), "#!/bin/sh\ncat >"+strconv.Quote(captured)+"\nprintf '{\"outcome\":\"applied\"}\\n'\n")
	body := filepath.Join(t.TempDir(), "body.md")
	comments := filepath.Join(t.TempDir(), "comments.json")
	if err := os.WriteFile(body, []byte("One material finding.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, attestation, commentData := reviewDecisionFixture(t, cfg, facts, findings.P1, findings.BarPass)
	if err := os.WriteFile(comments, commentData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ForgeCommand(t.Context(), []string{"review", body, comments, manifest, attestation}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		State    forge.ReviewVerdict   `json:"state"`
		Body     string                `json:"body"`
		Comments []forge.ReviewComment `json:"comments"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.State != forge.ReviewRequestChanges {
		t.Fatalf("review verdict=%q", payload.State)
	}
	record, ok := product.TrailingRecord(payload.Body)
	if !ok || record["head"] != facts.HeadSHA || record["target"] != "target-1" || record["governing"] == "" || record["block-kind"] != string(reconcile.FindingBlock) {
		t.Fatalf("review product record=%#v body=%q", record, payload.Body)
	}
	if !strings.Contains(payload.Body, "<!-- Minos-Disposition: ") || len(payload.Comments) != 1 || !strings.Contains(payload.Comments[0].Body, "[P1] Wrong result") {
		t.Fatalf("manifest-bound review body=%q comments=%#v", payload.Body, payload.Comments)
	}
}

func TestForgeCommandFallbackDisclosureStaysNonBlocking(t *testing.T) {
	cfg, facts, _ := forgeCommandAttempt(t)
	captured := filepath.Join(t.TempDir(), "review")
	writeScript(t, filepath.Join(cfg.Forges["local"].Adaptation, "guarded-post-review"), "#!/bin/sh\ncat >"+strconv.Quote(captured)+"\nprintf '{\"outcome\":\"applied\"}\\n'\n")
	body := filepath.Join(t.TempDir(), "body.md")
	comments := filepath.Join(t.TempDir(), "comments.json")
	if err := os.WriteFile(body, []byte("No material findings.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, attestation, commentData := reviewDecisionFixture(t, cfg, facts, findings.P2, findings.BarPass)
	if err := os.WriteFile(comments, commentData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ForgeCommand(t.Context(), []string{"review", body, comments, manifest, attestation}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		State forge.ReviewVerdict `json:"state"`
		Body  string              `json:"body"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.State != forge.ReviewApprove || !strings.Contains(payload.Body, "only findings at or above P1 are material or merge-blocking") {
		t.Fatalf("fallback review=%#v", payload)
	}
	record, ok := product.TrailingRecord(payload.Body)
	if !ok || record["block-kind"] != "" {
		t.Fatalf("fallback product record=%#v", record)
	}
}

func TestForgeReviewRejectsStaleAttestationAndIncompletePublicationSet(t *testing.T) {
	cfg, facts, lease := forgeCommandAttempt(t)
	repo, err := FindRepoConfig(cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath, attestationPath, _ := reviewDecisionFixture(t, cfg, facts, findings.P1, findings.BarPass)
	data, err := os.ReadFile(attestationPath)
	if err != nil {
		t.Fatal(err)
	}
	var attestation findings.BarAttestation
	if err := json.Unmarshal(data, &attestation); err != nil {
		t.Fatal(err)
	}
	attestation.ManifestSHA256 = strings.Repeat("0", 64)
	data, _ = json.Marshal(attestation)
	if err := os.WriteFile(attestationPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	guard := forge.Guard{Repository: forge.Repository{Owner: facts.Owner, Name: facts.Repo}, PullRequest: 7, HeadSHA: facts.HeadSHA, TargetSHA: "target-1"}
	if _, _, _, err := reviewDecision(manifestPath, attestationPath, repo, facts, guard, lease.Token); err == nil || !strings.Contains(err.Error(), "names manifest") {
		t.Fatalf("stale attestation error=%v", err)
	}

	manifestPath, _, _ = reviewDecisionFixture(t, cfg, facts, findings.P1, findings.BarPass)
	emptyComments := filepath.Join(t.TempDir(), "comments.json")
	if err := os.WriteFile(emptyComments, []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestData, _ := os.ReadFile(manifestPath)
	manifest, err := findings.DecodeManifest(manifestData)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readManifestComments(emptyComments, manifest); err == nil || !strings.Contains(err.Error(), "has no review comment") {
		t.Fatalf("incomplete publication set error=%v", err)
	}
}

func TestReviewDecisionRequiresFreshCleanAttestationAfterDelivery(t *testing.T) {
	cfg, facts, lease := forgeCommandAttempt(t)
	repo, err := FindRepoConfig(cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	repo.FindingDisposition.Mode = findings.DestinationMode
	repo.FindingDisposition.Destination = "backlog"
	repo.FindingDisposition.Target = "owner/subject"
	manifest := decisionManifestFixture(t, repo, facts, lease.Token, findings.P2)
	pendingManifest, pendingBar := writeDecisionPair(t, manifest, findings.BarPass)
	guard := forge.Guard{Repository: forge.Repository{Owner: facts.Owner, Name: facts.Repo}, PullRequest: 7, HeadSHA: facts.HeadSHA, TargetSHA: "target-1"}
	if _, _, result, err := reviewDecision(pendingManifest, pendingBar, repo, facts, guard, lease.Token); err != nil || result != product.ReviewIncomplete() {
		t.Fatalf("pending clean result=%#v err=%v", result, err)
	}
	receipt := deliveryReceiptFixture(t, manifest)
	if err := manifest.ConfirmDelivery(manifest.Findings[0].Finding.OccurrenceID, receipt); err != nil {
		t.Fatal(err)
	}
	confirmedManifest := filepath.Join(t.TempDir(), "confirmed.json")
	writeJSONFile(t, confirmedManifest, manifest)
	if _, _, _, err := reviewDecision(confirmedManifest, pendingBar, repo, facts, guard, lease.Token); err == nil {
		t.Fatal("pending attestation was accepted for the confirmed clean manifest")
	}
	confirmedManifest, confirmedBar := writeDecisionPair(t, manifest, findings.BarPass)
	if _, _, result, err := reviewDecision(confirmedManifest, confirmedBar, repo, facts, guard, lease.Token); err != nil || result != product.ReviewConverged() {
		t.Fatalf("confirmed clean result=%#v err=%v", result, err)
	}
}

func TestMaterialDecisionRemainsTheSinglePredeliveryInput(t *testing.T) {
	cfg, facts, lease := forgeCommandAttempt(t)
	repo, err := FindRepoConfig(cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	repo.FindingDisposition.Mode = findings.DestinationMode
	repo.FindingDisposition.Destination = "backlog"
	repo.FindingDisposition.Target = "owner/subject"
	manifest := decisionManifestFixture(t, repo, facts, lease.Token, findings.P1, findings.P2)
	initialManifest, initialBar := writeDecisionPair(t, manifest, findings.BarPass)
	guard := forge.Guard{Repository: forge.Repository{Owner: facts.Owner, Name: facts.Repo}, PullRequest: 7, HeadSHA: facts.HeadSHA, TargetSHA: "target-1"}
	if _, _, result, err := reviewDecision(initialManifest, initialBar, repo, facts, guard, lease.Token); err != nil || result != product.ReviewHasMaterialFindings() {
		t.Fatalf("initial material result=%#v err=%v", result, err)
	}
	quietOccurrence := manifest.Findings[1].Finding.OccurrenceID
	if err := manifest.ConfirmDelivery(quietOccurrence, deliveryReceiptFixtureFor(t, manifest, 1)); err != nil {
		t.Fatal(err)
	}
	confirmedManifest, confirmedBar := writeDecisionPair(t, manifest, findings.BarPass)
	if _, _, _, err := reviewDecision(confirmedManifest, confirmedBar, repo, facts, guard, lease.Token); err == nil || !strings.Contains(err.Error(), "delivery evidence") {
		t.Fatalf("confirmed material snapshot error=%v", err)
	}
	if _, _, result, err := reviewDecision(initialManifest, initialBar, repo, facts, guard, lease.Token); err != nil || result != product.ReviewHasMaterialFindings() {
		t.Fatalf("original material decision no longer usable: result=%#v err=%v", result, err)
	}
}

func decisionManifestFixture(t *testing.T, repo RepoConfig, facts Facts, token int64, priorities ...findings.Priority) findings.DispositionManifest {
	t.Helper()
	context := findings.ManifestContext{Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo, PullRequest: 7, HeadSHA: facts.HeadSHA, TargetSHA: "target-1", AttemptToken: token, GoverningIdentity: governingIdentity(repo)}
	var candidates []findings.CandidateDisposition
	var verified []findings.VerifiedFinding
	for ordinal, priority := range priorities {
		producer := findings.Producer{Family: "codex", ID: "review-" + strconv.Itoa(ordinal), Ordinal: ordinal}
		candidateID, err := findings.NewCandidateID(token, producer)
		if err != nil {
			t.Fatal(err)
		}
		category := findings.DefectCorrectness
		anchor := findings.NewEvidenceAnchor("example-"+strconv.Itoa(ordinal)+".go", findings.HeadSide, ordinal+1, "blob-"+strconv.Itoa(ordinal), "return wrong "+strconv.Itoa(ordinal))
		candidate := findings.CandidateFinding{SchemaVersion: 1, CandidateID: candidateID, Anchor: anchor, Criterion: findings.Criterion{Defect: &category}, ProposedPriority: priority, Assurance: findings.AgentJudgement, Title: "Wrong result " + strconv.Itoa(ordinal), Message: "Returns the wrong result.", Producer: producer}
		verifier := findings.Producer{Family: "claude", ID: "verify-" + strconv.Itoa(ordinal), Ordinal: ordinal}
		finding := findings.VerifiedFinding{SchemaVersion: 1, CandidateID: candidateID, LineageID: findings.LineageID("F-ABC" + strconv.Itoa(ordinal)), Anchor: anchor, Criterion: candidate.Criterion, ProposedPriority: priority, VerifierPriority: priority, VerifiedPriority: priority, PriorityValidation: findings.PriorityValidation{VerifierFamily: verifier.Family, VerifierID: verifier.ID, Agreement: findings.PriorityAgreed, Rationale: "Impact confirmed."}, Assurance: findings.AgentJudgement, Title: candidate.Title, Message: candidate.Message, Producer: producer, Verifier: verifier}
		finding.OccurrenceID, err = findings.NewOccurrenceID(context, finding)
		if err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, findings.CandidateDisposition{Candidate: candidate, Outcome: findings.CandidateVerified, OccurrenceID: finding.OccurrenceID, VerificationEvidence: findings.VerificationEvidence{Verifier: verifier, Rationale: "Confirmed independently."}})
		verified = append(verified, finding)
	}
	manifest, err := findings.NewDispositionManifest(context, repo.FindingPolicy(), candidates, verified)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writeDecisionPair(t *testing.T, manifest findings.DispositionManifest, verdict findings.BarVerdict) (string, string) {
	t.Helper()
	digest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	bar := findings.BarAttestation{SchemaVersion: 1, ManifestSHA256: digest, Verdict: verdict, Reasons: []string{}, ImplicatedBriefs: []findings.BriefReason{}, CandidateReconsiderations: []findings.CandidateReconsideration{}, Checker: findings.BarChecker{Family: "claude", ID: "bar"}}
	root := t.TempDir()
	manifestPath, barPath := filepath.Join(root, "manifest.json"), filepath.Join(root, "bar.json")
	writeJSONFile(t, manifestPath, manifest)
	writeJSONFile(t, barPath, bar)
	return manifestPath, barPath
}

func deliveryReceiptFixture(t *testing.T, manifest findings.DispositionManifest) findings.DeliveryReceipt {
	t.Helper()
	return deliveryReceiptFixtureFor(t, manifest, 0)
}

func deliveryReceiptFixtureFor(t *testing.T, manifest findings.DispositionManifest, index int) findings.DeliveryReceipt {
	t.Helper()
	payloadDigest := strings.Repeat(strconv.Itoa(index+1), 64)
	occurrenceID := manifest.Findings[index].Finding.OccurrenceID
	receiptID, err := findings.NewReceiptID(manifest.Policy.Destination, manifest.Policy.Target, occurrenceID, payloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	return findings.DeliveryReceipt{SchemaVersion: 1, ReceiptID: receiptID, Destination: manifest.Policy.Destination, Target: manifest.Policy.Target, OccurrenceID: occurrenceID, PayloadSHA256: payloadDigest}
}

func reviewDecisionFixture(t *testing.T, cfg ServiceConfig, facts Facts, priority findings.Priority, verdict findings.BarVerdict) (string, string, []byte) {
	t.Helper()
	repo, err := FindRepoConfig(cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	token, err := attemptToken()
	if err != nil {
		t.Fatal(err)
	}
	context := findings.ManifestContext{Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo, PullRequest: 7, HeadSHA: facts.HeadSHA, TargetSHA: "target-1", AttemptToken: token, GoverningIdentity: governingIdentity(repo)}
	producer := findings.Producer{Family: "codex", ID: "review-1", Ordinal: 0}
	candidateID, err := findings.NewCandidateID(token, producer)
	if err != nil {
		t.Fatal(err)
	}
	category := findings.DefectCorrectness
	anchor := findings.NewEvidenceAnchor("internal/example.go", findings.HeadSide, 3, "blob-1", "return wrong")
	candidate := findings.CandidateFinding{SchemaVersion: 1, CandidateID: candidateID, Anchor: anchor, Criterion: findings.Criterion{Defect: &category}, ProposedPriority: priority, Assurance: findings.AgentJudgement, Title: "Wrong result", Message: "Returns the wrong result.", Producer: producer}
	verifier := findings.Producer{Family: "claude", ID: "verify-1", Ordinal: 0}
	verified := findings.VerifiedFinding{SchemaVersion: 1, CandidateID: candidateID, LineageID: "F-ABCD", Anchor: anchor, Criterion: candidate.Criterion, ProposedPriority: priority, VerifierPriority: priority, VerifiedPriority: priority, PriorityValidation: findings.PriorityValidation{VerifierFamily: verifier.Family, VerifierID: verifier.ID, Agreement: findings.PriorityAgreed, Rationale: "Impact confirmed."}, Assurance: findings.AgentJudgement, Title: candidate.Title, Message: candidate.Message, Producer: producer, Verifier: verifier}
	verified.OccurrenceID, err = findings.NewOccurrenceID(context, verified)
	if err != nil {
		t.Fatal(err)
	}
	candidateDisposition := findings.CandidateDisposition{Candidate: candidate, Outcome: findings.CandidateVerified, OccurrenceID: verified.OccurrenceID, VerificationEvidence: findings.VerificationEvidence{Verifier: verifier, Rationale: "Confirmed independently."}}
	manifest, err := findings.NewDispositionManifest(context, repo.FindingPolicy(), []findings.CandidateDisposition{candidateDisposition}, []findings.VerifiedFinding{verified})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	bar := findings.BarAttestation{SchemaVersion: 1, ManifestSHA256: digest, Verdict: verdict, Reasons: []string{}, ImplicatedBriefs: []findings.BriefReason{}, CandidateReconsiderations: []findings.CandidateReconsideration{}, Checker: findings.BarChecker{Family: "claude", ID: "bar-1"}}
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	attestationPath := filepath.Join(root, "bar.json")
	manifestData, _ := json.Marshal(manifest)
	attestationData, _ := json.Marshal(bar)
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attestationPath, attestationData, 0o600); err != nil {
		t.Fatal(err)
	}
	comments, _ := json.Marshal([]manifestComment{{OccurrenceID: verified.OccurrenceID, NewPosition: 3}})
	return manifestPath, attestationPath, comments
}

func TestForgeCommandMergeReadsSnapshotAndAddsCleanupObligation(t *testing.T) {
	cfg, facts, _ := forgeCommandAttempt(t)
	mergeCalls := filepath.Join(t.TempDir(), "merge-calls")
	writeScript(t, filepath.Join(cfg.Forges["local"].Adaptation, "guarded-merge"), "#!/bin/sh\nprintf 'call\\n' >>"+strconv.Quote(mergeCalls)+"\nprintf '{\"outcome\":\"applied\"}\\n'\n")
	snapshot := forge.Snapshot{
		AuthenticatedUser: "Minos", Repository: "owner/subject", PullRequest: 7, State: "merged", Merged: true,
		Author: "alice", HeadSHA: facts.HeadSHA, HeadBranch: "change", HeadRepository: "owner/subject",
		TargetSHA: "merged-target", TargetBranch: "main", TargetRepository: "owner/subject", DefaultBranch: "main",
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(cfg.Forges["local"].Adaptation, "snapshot"), "#!/bin/sh\nprintf '%s\\n' "+strconv.Quote(string(data))+"\n")
	if err := ForgeCommand(t.Context(), []string{"merge", "squash"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(mergeCalls)
	if err != nil || strings.Count(string(calls), "call") != 1 {
		t.Fatalf("merge calls=%q err=%v", calls, err)
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cleanups, err := store.ListCleanup(t.Context())
	if err != nil || len(cleanups) != 1 || cleanups[0].MergedHead != facts.HeadSHA || cleanups[0].Branch != "change" {
		t.Fatalf("cleanup obligations=%#v err=%v", cleanups, err)
	}
}

func TestServiceAuthoredMergeAdvanceCarriesEveryFenceThroughTeardown(t *testing.T) {
	cfg, facts, lease := forgeCommandAttempt(t)
	reaction, removals := presenceProbe(t, cfg)
	if err := os.WriteFile(reaction, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	adaptation := cfg.Forges["local"].Adaptation
	writeScript(t, filepath.Join(adaptation, "guarded-merge"), "#!/bin/sh\nprintf '{\"outcome\":\"applied\"}\\n'\n")
	merged := forge.Snapshot{
		AuthenticatedUser: "Minos", Repository: "owner/subject", PullRequest: 7, State: "merged", Merged: true,
		Author: "alice", HeadSHA: facts.HeadSHA, HeadBranch: "change", HeadRepository: "owner/subject",
		TargetSHA: "target-2", TargetBranch: "main", TargetRepository: "owner/subject", DefaultBranch: "main",
	}
	data, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptation, "snapshot"), "#!/bin/sh\nprintf '%s\\n' "+strconv.Quote(string(data))+"\n")
	statusArgs := filepath.Join(t.TempDir(), "status-args")
	writeScript(t, filepath.Join(adaptation, "guarded-set-status"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >"+strconv.Quote(statusArgs)+"\nif [ \"$5\" = target-2 ]; then\n  printf '{\"outcome\":\"applied\"}\\n'\nelse\n  printf '{\"outcome\":\"rejected\",\"reason\":\"stale target\"}\\n'\nfi\n")
	writeScript(t, filepath.Join(adaptation, "delete-branch"), "#!/bin/sh\nif [ \"$5\" = target-2 ]; then\n  printf '{\"outcome\":\"applied\"}\\n'\nelse\n  printf '{\"outcome\":\"rejected\",\"reason\":\"stale target\"}\\n'\nfi\n")

	if err := ForgeCommand(t.Context(), []string{"merge", "squash"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := RunGuardCommand(t.Context(), []string{"--config", cfg.Root, "advance", facts.HeadSHA, "target-1", facts.HeadSHA, "target-2"}); err != nil {
		t.Fatal(err)
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	current, err := currentAttempt(t.Context(), cfg, facts, store, lease.Token)
	if err != nil || !current {
		t.Fatalf("advanced attempt current=%v err=%v", current, err)
	}
	owned, err := store.Owns(t.Context(), lease.Key, lease.Token)
	if err != nil || !owned {
		t.Fatalf("advanced token owns=%v err=%v", owned, err)
	}

	var failures []error
	if err := ForgeCommand(t.Context(), []string{"status", "merged"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		failures = append(failures, err)
	}
	if err := ForgeCommand(t.Context(), []string{"cleanup"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		failures = append(failures, err)
	}
	forgeConfig := cfg.Forges[facts.Forge]
	adapt, err := NewAdaptation(forgeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseRun(t.Context(), adapt, facts, store, lease.Token); err != nil {
		failures = append(failures, err)
	}
	if err := closeRunLease(t.Context(), cfg, facts, store, lease.Token); err != nil {
		failures = append(failures, err)
	}
	if len(failures) != 0 {
		t.Errorf("post-merge teardown failures: %v", errors.Join(failures...))
	}
	if _, found, err := store.Lease(t.Context(), lease.Key); err != nil || found {
		t.Errorf("lease remains found=%v err=%v", found, err)
	}
	if cleanup, found, err := store.Cleanup(t.Context(), lease.Key); err != nil || found {
		t.Errorf("cleanup remains=%#v found=%v err=%v", cleanup, found, err)
	}
	store.Close()
	assertPresenceClosed(t, reaction, removals)
	arguments, err := os.ReadFile(statusArgs)
	if err != nil || !strings.Contains(string(arguments), "head-1 target-2 Minos Minos success Merged") {
		t.Errorf("terminal status arguments=%q err=%v", arguments, err)
	}
}

func TestUnrelatedTargetMovementRemainsStaleWithoutOwnerAdvance(t *testing.T) {
	cfg, facts, lease := forgeCommandAttempt(t)
	adaptation := cfg.Forges["local"].Adaptation
	moved := forge.Snapshot{
		AuthenticatedUser: "Minos", Repository: "owner/subject", PullRequest: 7, State: "open",
		Author: "alice", HeadSHA: facts.HeadSHA, HeadBranch: "change", HeadRepository: "owner/subject",
		TargetSHA: "target-2", TargetBranch: "main", TargetRepository: "owner/subject", DefaultBranch: "main",
	}
	data, err := json.Marshal(moved)
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptation, "snapshot"), "#!/bin/sh\nprintf '%s\\n' "+strconv.Quote(string(data))+"\n")
	writeScript(t, filepath.Join(adaptation, "guarded-set-status"), "#!/bin/sh\nif [ \"$5\" = target-2 ]; then\n  printf '{\"outcome\":\"applied\"}\\n'\nelse\n  printf '{\"outcome\":\"rejected\",\"reason\":\"stale target\"}\\n'\nfi\n")
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	current, err := currentAttempt(t.Context(), cfg, facts, store, lease.Token)
	if err != nil || current {
		t.Fatalf("unadvanced attempt current=%v err=%v", current, err)
	}
	if err := ForgeCommand(t.Context(), []string{"status", "merged"}, strings.NewReader(""), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "stale target") {
		t.Fatalf("unrelated movement status error=%v, want stale target rejection", err)
	}
	currentLease, found, err := store.Lease(t.Context(), lease.Key)
	if err != nil || !found || currentLease.ObservedTarget != "target-1" {
		t.Fatalf("unrelated movement changed lease=%#v found=%v err=%v", currentLease, found, err)
	}
}

func TestRunGuardBeginClearsPriorWaitAndPublishesWorking(t *testing.T) {
	cfg, facts, lease := forgeCommandAttempt(t)
	adaptation := cfg.Forges["local"].Adaptation
	snapshot := forge.Snapshot{
		AuthenticatedUser: "Minos", Repository: "owner/subject", PullRequest: 7, State: "open",
		Author: "alice", HeadSHA: facts.HeadSHA, HeadBranch: "change", HeadRepository: "owner/subject",
		TargetSHA: "target-1", TargetBranch: "main", TargetRepository: "owner/subject", DefaultBranch: "main",
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptation, "snapshot"), "#!/bin/sh\nprintf '%s\\n' "+strconv.Quote(string(data))+"\n")
	captured := filepath.Join(t.TempDir(), "status")
	writeScript(t, filepath.Join(adaptation, "guarded-set-status"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >"+strconv.Quote(captured)+"\nprintf '{\"outcome\":\"applied\"}\\n'\n")
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if set, err := store.SetWait(t.Context(), ledger.Wait{Key: lease.Key, Fingerprint: "old"}, lease.Token); err != nil || !set {
		t.Fatalf("SetWait()=%v err=%v", set, err)
	}
	store.Close()

	if err := RunGuardCommand(t.Context(), []string{"--config", cfg.Root, "begin"}); err != nil {
		t.Fatal(err)
	}
	store, err = ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, found, err := store.Wait(t.Context(), lease.Key); err != nil || found {
		t.Fatalf("prior wait remains found=%v err=%v", found, err)
	}
	status, err := os.ReadFile(captured)
	if err != nil || !strings.Contains(string(status), "Minos pending Reviewing changes") {
		t.Fatalf("working status=%q err=%v", status, err)
	}
}

func TestRunGuardRevalidateChecksCurrentOwnershipAndLifecycleIndex(t *testing.T) {
	cfg, facts, _ := forgeCommandAttempt(t)
	adaptation := cfg.Forges["local"].Adaptation
	snapshot := forge.Snapshot{
		AuthenticatedUser: "Minos", Repository: "owner/subject", PullRequest: 7, State: "open",
		Author: "alice", HeadSHA: facts.HeadSHA, HeadBranch: "change", HeadRepository: "owner/subject",
		TargetSHA: "target-1", TargetBranch: "main", TargetRepository: "owner/subject", DefaultBranch: "main",
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptation, "snapshot"), "#!/bin/sh\nprintf '%s\\n' "+strconv.Quote(string(data))+"\n")
	runDir := filepath.Join(cfg.Runs.Dir, "attempt")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(runDir, "lifecycle-index.md")
	if err := os.WriteFile(index, []byte("# Current lifecycle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "resolved-lead.json"), []byte("[{\"resolved_model\":\"pinned\"}]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo, err := FindRepoConfig(cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINOS_RUN_DIR", runDir)
	t.Setenv("MINOS_GOVERNING_IDENTITY", governingIdentity(repo))
	t.Setenv("MINOS_DEPLOYMENT_PROFILE", deploymentIdentity(cfg, repo))
	if err := RunGuardCommand(t.Context(), []string{"--config", cfg.Root, "revalidate", index}); err != nil {
		t.Fatal(err)
	}
	if err := RunGuardCommand(t.Context(), []string{"--config", cfg.Root, "revalidate", filepath.Join(t.TempDir(), "outside")}); err == nil {
		t.Fatal("revalidation accepted an index outside the attempt directory")
	}
}
