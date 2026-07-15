package shell

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"bfj/minos/internal/findings"
	"bfj/minos/internal/forge"
	"bfj/minos/internal/ledger"
	"bfj/minos/internal/product"
)

func TestAssembleDispositionManifestAdmitsExactDiffBlobAndBriefEvidence(t *testing.T) {
	command, verificationPath := findingAssemblyFixture(t)
	manifest, err := assembleDispositionManifest(command, verificationPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Candidates) != 1 || len(manifest.Findings) != 1 {
		t.Fatalf("manifest candidates=%d findings=%d", len(manifest.Candidates), len(manifest.Findings))
	}
	entry := manifest.Findings[0]
	if entry.Finding.Anchor.Side != findings.HeadSide || entry.Finding.Anchor.Path != "example.go" || entry.Finding.Anchor.CodeQuote != "return 2" || entry.Finding.Criterion.Brief == nil || entry.Finding.Criterion.Brief.TrustedTargetSHA != command.Lease.ObservedTarget || !entry.Finding.LineageID.Valid() {
		t.Fatalf("assembled finding=%#v", entry.Finding)
	}
	if entry.Material || entry.Publication != findings.PublicationDisclosed || entry.Delivery != findings.DeliveryNotRequired {
		t.Fatalf("policy disposition=%#v", entry)
	}
}

func TestAssembleDispositionManifestRejectsNonUniqueQuote(t *testing.T) {
	command, verificationPath := findingAssemblyFixture(t)
	data, err := os.ReadFile(verificationPath)
	if err != nil {
		t.Fatal(err)
	}
	var result panelVerificationResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	result.Candidates[0].Candidate.CodeQuote = "e"
	result.Candidates[0].VerifiedFinding.CodeQuote = "e"
	writeJSONFile(t, verificationPath, result)
	if _, err := assembleDispositionManifest(command, verificationPath, ""); err == nil {
		t.Fatal("assembly accepted an ambiguous code quote")
	}
}

func TestAssembleDispositionManifestRejectsInconsistentPanelProducer(t *testing.T) {
	command, verificationPath := findingAssemblyFixture(t)
	var result panelVerificationResult
	readJSONFile(t, verificationPath, &result)
	result.Candidates[0].Producer.Family = "codex"
	writeJSONFile(t, verificationPath, result)
	if _, err := assembleDispositionManifest(command, verificationPath, ""); err == nil {
		t.Fatal("assembly accepted a producer family inconsistent with the panel identity")
	}
}

func TestAssembleDispositionManifestRejectsChangedVerifiedProposalIdentity(t *testing.T) {
	command, verificationPath := findingAssemblyFixture(t)
	var result panelVerificationResult
	readJSONFile(t, verificationPath, &result)
	result.Candidates[0].VerifiedFinding.Side = "LEFT"
	writeJSONFile(t, verificationPath, result)
	if _, err := assembleDispositionManifest(command, verificationPath, ""); err == nil {
		t.Fatal("assembly accepted a verified finding with changed proposal identity")
	}
}

func TestResolveLineageUsesOnlyExactEvidenceOrDiscoverableClaim(t *testing.T) {
	_, candidate, verified := findingRecordsForLineage(t)
	prior := []priorOccurrence{{Context: testFindingContext(), Finding: verified, RepairCommitSHA: "repair-commit"}}
	existingID := product.MustParseFindingID(string(verified.LineageID))
	existing := map[product.FindingID]struct{}{existingID: {}}

	candidate.Anchor.BlobSHA = "new-blob"
	reused, err := resolveLineage(candidate, "", prior, existing)
	if err != nil || reused != verified.LineageID {
		t.Fatalf("exact quote lineage=%s err=%v", reused, err)
	}

	candidate.Anchor = findings.NewEvidenceAnchor("moved.go", findings.HeadSide, 8, "moved-blob", "return fixed")
	claimed, err := resolveLineage(candidate, string(verified.LineageID), prior, existing)
	if err != nil || claimed != verified.LineageID {
		t.Fatalf("claimed successor lineage=%s err=%v", claimed, err)
	}

	fresh, err := resolveLineage(candidate, "F-ZZZZ", prior, existing)
	if err != nil || !fresh.Valid() || fresh == verified.LineageID {
		t.Fatalf("fresh lineage=%s err=%v", fresh, err)
	}

	ambiguousPrior := append(prior, prior[0])
	ambiguous, err := resolveLineage(findings.CandidateFinding{Anchor: verified.Anchor, Criterion: verified.Criterion}, "", ambiguousPrior, existing)
	if err != nil || !ambiguous.Valid() || ambiguous == verified.LineageID {
		t.Fatalf("ambiguous lineage=%s err=%v", ambiguous, err)
	}
	ambiguousClaim, err := resolveLineage(candidate, string(verified.LineageID), ambiguousPrior, existing)
	if err != nil || !ambiguousClaim.Valid() || ambiguousClaim == verified.LineageID {
		t.Fatalf("ambiguous claimed lineage=%s err=%v", ambiguousClaim, err)
	}
	if _, _, err := successorRepairEvidence(verified, ambiguousPrior); err == nil {
		t.Fatal("ambiguous repair evidence was accepted")
	}
}

func TestDeliverDispositionManifestCreatesOnceAndRequiresAuthenticatedReadBack(t *testing.T) {
	cfg, facts, lease := forgeCommandAttempt(t)
	destinationRoot := filepath.Join(cfg.Root, "destination")
	if err := os.MkdirAll(destinationRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	servicePath := filepath.Join(cfg.Root, "service.toml")
	service, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	service = append(service, []byte("\n[finding-destinations.backlog]\nadaptation="+strconv.Quote(destinationRoot)+"\nendpoint=\"fixture://backlog\"\ncredential-file="+strconv.Quote(filepath.Join(cfg.Root, "secret"))+"\nexpected-principal=\"minos-service\"\n")...)
	if err := os.WriteFile(servicePath, service, 0o600); err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.Join(cfg.Root, "repos", "subject.toml")
	repoConfig := "forge=\"local\"\nowner=\"owner\"\nrepo=\"subject\"\n[adaptation]\nbuild=\"true\"\ntest=\"true\"\nskill=\"skill\"\n[finding-disposition]\nmode=\"destination\"\ndestination=\"backlog\"\ntarget=\"owner/subject\"\n[[eligibility]]\nauthors=[\"*\"]\n"
	if err := os.WriteFile(repoPath, []byte(repoConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(destinationRoot, "record.json")
	createCount := filepath.Join(destinationRoot, "create-count")
	writeScript(t, filepath.Join(destinationRoot, "discover"), "#!/usr/bin/python3\nimport json, os, sys\nr=json.load(sys.stdin)\np="+strconv.Quote(statePath)+"\nif not os.path.exists(p):\n print(json.dumps({'schema_version':1,'outcome':'not-found'}))\nelse:\n d=json.load(open(p))\n print(json.dumps({'schema_version':1,'outcome':'found','matches':[{'provider_record_id':'provider-1','occurrence_id':d['receipt']['occurrence_id'],'payload_sha256':d['receipt']['payload_sha256']}]}))\n")
	writeScript(t, filepath.Join(destinationRoot, "create"), "#!/usr/bin/python3\nimport json, sys\nr=json.load(sys.stdin)\njson.dump(r['record'],open("+strconv.Quote(statePath)+",'w'))\nopen("+strconv.Quote(createCount)+",'a').write('create\\n')\nprint(json.dumps({'schema_version':1,'outcome':'created','provider_record_id':'provider-1'}))\n")
	writeScript(t, filepath.Join(destinationRoot, "read"), "#!/usr/bin/python3\nimport json, sys\njson.load(sys.stdin)\nd=json.load(open("+strconv.Quote(statePath)+"))\nprint(json.dumps({'schema_version':1,'outcome':'found','record':d,'authenticated_principal':'minos-service','observed_at':'2026-07-15T12:00:00Z'}))\n")

	loaded, err := LoadServiceConfig(cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := FindRepoConfig(loaded, facts)
	if err != nil {
		t.Fatal(err)
	}
	store, err := ledger.Open(ledgerPath(loaded))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	command := findingCommandContext{Config: loaded, Repo: repo, Facts: facts, Store: store, Token: lease.Token, Lease: lease, Workspace: lease.Workspace}
	manifest := decisionManifestFixture(t, repo, facts, lease.Token, findings.P2)
	manifestPath, barPath := writeDecisionPair(t, manifest, findings.BarPass)
	pendingDigest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := deliverDispositionManifest(t.Context(), command, manifestPath, barPath)
	if err != nil {
		t.Fatal(err)
	}
	confirmedDigest, err := confirmed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Findings[0].Delivery != findings.DeliveryConfirmed || confirmed.Findings[0].Receipt == nil || confirmedDigest == pendingDigest {
		t.Fatalf("confirmed manifest=%#v pending=%s confirmed=%s", confirmed, pendingDigest, confirmedDigest)
	}
	if _, err := deliverDispositionManifest(t.Context(), command, manifestPath, barPath); err != nil {
		t.Fatalf("idempotent rediscovery failed: %v", err)
	}
	created, err := os.ReadFile(createCount)
	if err != nil || bytes.Count(created, []byte("create\n")) != 1 {
		t.Fatalf("create calls=%q err=%v", created, err)
	}
}

func TestMaterialDeliveryRequiresTheExactCompletePublishedIndex(t *testing.T) {
	cfg, facts, lease := forgeCommandAttempt(t)
	repo, err := FindRepoConfig(cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	repo.FindingDisposition.Mode = findings.DestinationMode
	repo.FindingDisposition.Destination = "backlog"
	repo.FindingDisposition.Target = "owner/subject"
	manifest := decisionManifestFixture(t, repo, facts, lease.Token, findings.P1, findings.P2)
	index, err := manifest.Index()
	if err != nil {
		t.Fatal(err)
	}
	line, err := index.RecordLine()
	if err != nil {
		t.Fatal(err)
	}
	command := findingCommandContext{Config: cfg, Repo: repo, Facts: facts, Token: lease.Token, Lease: lease, Workspace: lease.Workspace}
	writeSnapshot := func(body string) {
		snapshot := forge.Snapshot{AuthenticatedUser: "Minos", Repository: "owner/subject", PullRequest: 7, State: "open", Author: "alice", HeadSHA: facts.HeadSHA, HeadBranch: "change", HeadRepository: "owner/subject", TargetSHA: "target-1", TargetBranch: "main", TargetRepository: "owner/subject", DefaultBranch: "main", Reviews: []forge.Review{{ID: 1, CommitID: facts.HeadSHA, Body: body, User: "Minos"}}}
		data, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		writeScript(t, filepath.Join(cfg.Forges["local"].Adaptation, "snapshot"), "#!/bin/sh\nprintf '%s\\n' "+strconv.Quote(string(data))+"\n")
	}
	writeSnapshot("Material review.\n\n" + line)
	if err := requirePublishedManifest(t.Context(), command, manifest); err != nil {
		t.Fatal(err)
	}
	decisionPath, barPath := writeDecisionPair(t, manifest, findings.BarPass)
	delivery := manifest
	delivery.Candidates = append([]findings.CandidateDisposition(nil), manifest.Candidates...)
	delivery.Findings = append([]findings.FindingDisposition(nil), manifest.Findings...)
	if err := delivery.ConfirmDelivery(delivery.Findings[1].Finding.OccurrenceID, deliveryReceiptFixtureFor(t, delivery, 1)); err != nil {
		t.Fatal(err)
	}
	deliveryPath := filepath.Join(t.TempDir(), "delivery.json")
	writeJSONFile(t, deliveryPath, delivery)
	plan, err := buildRepairPlan(t.Context(), command, decisionPath, barPath, deliveryPath)
	if err != nil || len(plan.Findings) != 2 || plan.DecisionManifestSHA256 == "" || plan.DeliveryManifestSHA256 == "" {
		t.Fatalf("repair plan=%#v err=%v", plan, err)
	}
	if _, err := buildRepairPlan(t.Context(), command, decisionPath, barPath, ""); err == nil {
		t.Fatal("material repair plan omitted required quiet delivery evidence")
	}
	index.Occurrences = index.Occurrences[:1]
	incompleteLine, err := index.RecordLine()
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot("Incomplete review.\n\n" + incompleteLine)
	if err := requirePublishedManifest(t.Context(), command, manifest); err == nil {
		t.Fatal("material delivery accepted an index that omitted a quiet occurrence")
	}
}

func TestRepairPlanDeliverySnapshotMayChangeOnlyReceiptState(t *testing.T) {
	cfg, facts, lease := forgeCommandAttempt(t)
	repo, err := FindRepoConfig(cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	repo.FindingDisposition.Mode = findings.DestinationMode
	repo.FindingDisposition.Destination = "backlog"
	repo.FindingDisposition.Target = "owner/subject"
	decision := decisionManifestFixture(t, repo, facts, lease.Token, findings.P1, findings.P2)
	delivery := decision
	delivery.Candidates = append([]findings.CandidateDisposition(nil), decision.Candidates...)
	delivery.Findings = append([]findings.FindingDisposition(nil), decision.Findings...)
	if err := delivery.ConfirmDelivery(delivery.Findings[1].Finding.OccurrenceID, deliveryReceiptFixtureFor(t, delivery, 1)); err != nil {
		t.Fatal(err)
	}
	if err := validateDeliverySnapshot(decision, delivery); err != nil {
		t.Fatal(err)
	}
	delivery.Findings[0].Repair = findings.RepairRepaired
	if err := validateDeliverySnapshot(decision, delivery); err == nil {
		t.Fatal("delivery evidence was allowed to rewrite the repair decision")
	}
}

func findingAssemblyFixture(t *testing.T) (findingCommandContext, string) {
	t.Helper()
	workspace := t.TempDir()
	gitAt(t, workspace, "init", "-q")
	gitAt(t, workspace, "config", "user.name", "Fixture")
	gitAt(t, workspace, "config", "user.email", "fixture@example.invalid")
	if err := os.MkdirAll(filepath.Join(workspace, ".review"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".review", "tone.md"), []byte("# Tone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "example.go"), []byte("package example\n\nfunc value() int {\n\treturn 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, workspace, "add", ".")
	gitAt(t, workspace, "commit", "-qm", "base")
	target := gitAt(t, workspace, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(workspace, "example.go"), []byte("package example\n\nfunc value() int {\n\treturn 2\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, workspace, "add", "example.go")
	gitAt(t, workspace, "commit", "-qm", "change")
	head := gitAt(t, workspace, "rev-parse", "HEAD")
	diff := filepath.Join(t.TempDir(), "change.diff")
	if err := os.WriteFile(diff, []byte(gitAt(t, workspace, "diff", target, head)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINOS_DIFF", diff)
	t.Setenv("MINOS_RUN_DIR", t.TempDir())

	var repo RepoConfig
	repo.Path = "/config/repos/subject.toml"
	repo.Forge, repo.Owner, repo.Repo = "local", "owner", "subject"
	repo.Policy.PublishThreshold, repo.Policy.RepairThreshold = findings.P1, findings.P3
	repo.FindingDisposition.Mode = findings.PublishThroughP3Mode
	repo.Adaptation.Skill = filepath.Join(mustWorkingDirectory(t), "skills", "foundry", "review-panel", "SKILL.md")
	facts := Facts{Forge: "local", Owner: "owner", Repo: "subject", PR: "7", HeadSHA: head, BaseSHA: target}
	command := findingCommandContext{Repo: repo, Facts: facts, Token: 17, Lease: ledger.Lease{Key: coordinationKey(facts), Token: 17, ObservedHead: head, ObservedTarget: target, Workspace: workspace}, Workspace: workspace}

	line := 4
	producerID := "tone[1/1]@claude"
	checkerFamily, checkerID, rationale := "codex", "check:tone[1/1]@claude:0@codex", "Impact confirmed."
	reviewTitle, confidence := "Tone", 0.91
	raw := panelFinding{Brief: "tone", ReviewTitle: &reviewTitle, Extent: "diff", File: "example.go", Line: &line, Side: "RIGHT", Priority: "P2", Title: "Return the intended value", Message: "The changed return value is incorrect.", CodeQuote: "return 2", Confidence: &confidence, Producer: producerID, ProducerIdentity: producerID, ProducerOrdinal: 0, Assurance: findings.AgentJudgement.String()}
	verified := raw
	verified.CheckedBy = checkerFamily
	verified.ProposedPriority = "P2"
	verified.VerifierPriority = "P2"
	verified.VerificationState = "verified"
	verified.PriorityValidation.Agreement = "agreed"
	verified.PriorityValidation.Rationale = rationale
	result := panelVerificationResult{SchemaVersion: 1, Criteria: []panelCriterion{{Name: "tone", Path: ".review/tone.md"}}, Candidates: []panelCandidate{{Candidate: raw, Producer: findings.Producer{Family: "claude", ID: producerID, Ordinal: 0}, Assurance: findings.AgentJudgement.String(), Outcome: findings.CandidateVerified, ProposedPriority: "P2", VerifierPriority: stringPointer("P2"), VerifiedPriority: stringPointer("P2"), VerificationEvidence: panelVerificationEvidence{CheckerFamily: &checkerFamily, CheckerID: &checkerID, Rationale: &rationale}, VerifiedFinding: &verified}}}
	verificationPath := filepath.Join(t.TempDir(), "verification.json")
	writeJSONFile(t, verificationPath, result)
	return command, verificationPath
}

func findingRecordsForLineage(t *testing.T) (findings.ManifestContext, findings.CandidateFinding, findings.VerifiedFinding) {
	t.Helper()
	context := testFindingContext()
	producer := findings.Producer{Family: "claude", ID: "review", Ordinal: 0}
	candidateID, err := findings.NewCandidateID(context.AttemptToken, producer)
	if err != nil {
		t.Fatal(err)
	}
	category := findings.DefectCorrectness
	anchor := findings.NewEvidenceAnchor("example.go", findings.HeadSide, 4, "blob", "return wrong")
	candidate := findings.CandidateFinding{SchemaVersion: 1, CandidateID: candidateID, Anchor: anchor, Criterion: findings.Criterion{Defect: &category}, ProposedPriority: findings.P1, Assurance: findings.AgentJudgement, Title: "Wrong result", Message: "The result is wrong.", Producer: producer}
	verifier := findings.Producer{Family: "codex", ID: "checker", Ordinal: 0}
	verified := findings.VerifiedFinding{SchemaVersion: 1, CandidateID: candidateID, LineageID: "F-ABCD", Anchor: anchor, Criterion: candidate.Criterion, ProposedPriority: findings.P1, VerifierPriority: findings.P1, VerifiedPriority: findings.P1, PriorityValidation: findings.PriorityValidation{VerifierFamily: verifier.Family, VerifierID: verifier.ID, Agreement: findings.PriorityAgreed, Rationale: "Confirmed."}, Assurance: findings.AgentJudgement, Title: candidate.Title, Message: candidate.Message, Producer: producer, Verifier: verifier}
	verified.OccurrenceID, err = findings.NewOccurrenceID(context, verified)
	if err != nil {
		t.Fatal(err)
	}
	return context, candidate, verified
}

func testFindingContext() findings.ManifestContext {
	return findings.ManifestContext{Forge: "local", Owner: "owner", Repo: "subject", PullRequest: 7, HeadSHA: "head", TargetSHA: "target", AttemptToken: 17, GoverningIdentity: "governing"}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

func stringPointer(value string) *string { return &value }

func mustWorkingDirectory(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for filepath.Base(root) != "finding-policy-disposition" {
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("could not locate repository root")
		}
		root = parent
	}
	return root
}
