package findings

import (
	"fmt"
	"strings"
	"testing"
)

func TestIdentityGoldenVectorsAndMovement(t *testing.T) {
	context, candidate, verified := findingFixture(t, P1, P2, 3)
	if got, want := candidate.CandidateID, CandidateID("C-eb4025117cc43cadb46d74dcbe66bfce0e3110c1732af4ff9f3825ac49d7140c"); got != want {
		t.Fatalf("candidate ID=%s want %s", got, want)
	}
	if got, want := verified.OccurrenceID, OccurrenceID("O-dd517a5395dc94f71a5937fa2455cfd41723422a3036e8c2d0b360d35b08d101"); got != want {
		t.Fatalf("occurrence ID=%s want %s", got, want)
	}

	movedLine := verified
	movedLine.Anchor.DisplayLine++
	lineID, err := NewOccurrenceID(context, movedLine)
	if err != nil || lineID != verified.OccurrenceID {
		t.Fatalf("line movement changed occurrence: id=%s err=%v", lineID, err)
	}

	movedHead := context
	movedHead.HeadSHA = "head-2"
	headID, err := NewOccurrenceID(movedHead, verified)
	if err != nil || headID == verified.OccurrenceID {
		t.Fatalf("head movement id=%s err=%v", headID, err)
	}
	movedTarget := context
	movedTarget.TargetSHA = "target-2"
	targetID, err := NewOccurrenceID(movedTarget, verified)
	if err != nil || targetID == verified.OccurrenceID {
		t.Fatalf("target movement id=%s err=%v", targetID, err)
	}
}

func TestDefaultPolicyCarriesDistinctPrioritiesToEveryDisposition(t *testing.T) {
	context := testContext()
	var candidates []CandidateDisposition
	var verified []VerifiedFinding
	for ordinal, priority := range []Priority{P0, P1, P2, P3} {
		_, candidate, finding := findingFixture(t, priority, priority, ordinal)
		candidates = append(candidates, verifiedCandidate(candidate, finding))
		verified = append(verified, finding)
	}
	manifest, err := NewDispositionManifest(context, ResolvedPolicy{PublishThreshold: P1, RepairThreshold: P3, Mode: DestinationMode, Destination: "backlog", Target: "owner/repo"}, candidates, verified)
	if err != nil {
		t.Fatal(err)
	}
	for index, entry := range manifest.Findings {
		wantMaterial := index <= 1
		wantPublication := PublicationWithheld
		wantDelivery := DeliveryPending
		if wantMaterial {
			wantPublication = PublicationPublished
			wantDelivery = DeliveryNotRequired
		}
		if entry.Material != wantMaterial || !entry.RepairEligible || entry.Publication != wantPublication || entry.Delivery != wantDelivery || entry.Repair != RepairSelected || entry.Finding.Assurance != AgentJudgement {
			t.Errorf("P%d disposition=%#v", index, entry)
		}
	}
}

func TestReducerHonoursEveryValidThresholdPair(t *testing.T) {
	priorities := []Priority{P0, P1, P2, P3}
	for publishIndex, publish := range priorities {
		for repairIndex := publishIndex; repairIndex < len(priorities); repairIndex++ {
			repair := priorities[repairIndex]
			for _, mode := range []DispositionMode{DestinationMode, PublishThroughP3Mode} {
				context := testContext()
				var candidates []CandidateDisposition
				var verified []VerifiedFinding
				for ordinal, priority := range priorities {
					_, candidate, finding := findingFixture(t, priority, priority, ordinal)
					candidates = append(candidates, verifiedCandidate(candidate, finding))
					verified = append(verified, finding)
				}
				policy := ResolvedPolicy{PublishThreshold: publish, RepairThreshold: repair, Mode: mode}
				if mode == DestinationMode {
					policy.Destination, policy.Target = "backlog", "owner/repo"
				}
				manifest, err := NewDispositionManifest(context, policy, candidates, verified)
				if err != nil {
					t.Fatalf("publish=%s repair=%s mode=%s: %v", publish, repair, mode, err)
				}
				for priorityIndex, entry := range manifest.Findings {
					material := priorityIndex <= publishIndex
					repairSelected := priorityIndex <= repairIndex
					if entry.Material != material || entry.RepairEligible != repairSelected || (entry.Repair == RepairSelected) != repairSelected {
						t.Errorf("publish=%s repair=%s mode=%s priority=%s disposition=%#v", publish, repair, mode, priorities[priorityIndex], entry)
					}
					if mode == DestinationMode && !material && (entry.Publication != PublicationWithheld || entry.Delivery != DeliveryPending) {
						t.Errorf("quiet destination priority=%s disposition=%#v", priorities[priorityIndex], entry)
					}
					if (mode == PublishThroughP3Mode || material) && (entry.Publication != PublicationDisclosed && entry.Publication != PublicationPublished || entry.Delivery != DeliveryNotRequired) {
						t.Errorf("published priority=%s disposition=%#v", priorities[priorityIndex], entry)
					}
				}
			}
		}
	}
}

func TestQuietOnlySetCannotInventMaterialIntervention(t *testing.T) {
	context, candidate, verified := findingFixture(t, P2, P2, 0)
	manifest, err := NewDispositionManifest(context, ResolvedPolicy{PublishThreshold: P1, RepairThreshold: P3, Mode: DestinationMode, Destination: "backlog", Target: "owner/repo"}, []CandidateDisposition{verifiedCandidate(candidate, verified)}, []VerifiedFinding{verified})
	if err != nil {
		t.Fatal(err)
	}
	entry := manifest.Findings[0]
	if entry.Material || entry.Repair != RepairNotTriggered || entry.Publication != PublicationWithheld || entry.Delivery != DeliveryPending {
		t.Fatalf("quiet disposition=%#v", entry)
	}

	manifest.Findings[0].Repair = RepairSelected
	if err := manifest.Validate(); err == nil {
		t.Fatal("quiet-only manifest accepted caller-selected intervention")
	}
}

func TestManifestIsExhaustiveAndSuppressionNeverNamesVerifiedFinding(t *testing.T) {
	context, candidate, verified := findingFixture(t, P1, P1, 0)
	entry := verifiedCandidate(candidate, verified)
	policy := ResolvedPolicy{PublishThreshold: P1, RepairThreshold: P3, Mode: PublishThroughP3Mode}
	if _, err := NewDispositionManifest(context, policy, []CandidateDisposition{entry}, nil); err == nil {
		t.Fatal("verified candidate was accepted without finding")
	}

	entry.Outcome = CandidateSuppressed
	entry.OccurrenceID = ""
	if _, err := NewDispositionManifest(context, policy, []CandidateDisposition{entry}, []VerifiedFinding{verified}); err == nil {
		t.Fatal("suppressed candidate was accepted with verified finding")
	}
}

func TestConfirmedDeliveryChangesDigestAndRequiresFreshAttestation(t *testing.T) {
	context, candidate, verified := findingFixture(t, P2, P2, 0)
	policy := ResolvedPolicy{PublishThreshold: P1, RepairThreshold: P3, Mode: DestinationMode, Destination: "backlog", Target: "owner/repo"}
	manifest, err := NewDispositionManifest(context, policy, []CandidateDisposition{verifiedCandidate(candidate, verified)}, []VerifiedFinding{verified})
	if err != nil {
		t.Fatal(err)
	}
	pendingDigest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	pendingAttestation := BarAttestation{SchemaVersion: 1, ManifestSHA256: pendingDigest, Verdict: BarPass, Reasons: []string{}, ImplicatedBriefs: []BriefReason{}, Checker: BarChecker{Family: "claude", ID: "bar-1"}}
	if err := pendingAttestation.ValidateFor(manifest); err != nil {
		t.Fatal(err)
	}

	payloadDigest := strings.Repeat("a", 64)
	receiptID, err := NewReceiptID(policy.Destination, policy.Target, verified.OccurrenceID, payloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.ConfirmDelivery(verified.OccurrenceID, DeliveryReceipt{SchemaVersion: 1, ReceiptID: receiptID, Destination: policy.Destination, Target: policy.Target, OccurrenceID: verified.OccurrenceID, PayloadSHA256: payloadDigest}); err != nil {
		t.Fatal(err)
	}
	confirmedDigest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if confirmedDigest == pendingDigest {
		t.Fatal("delivery confirmation did not change full manifest digest")
	}
	if err := pendingAttestation.ValidateFor(manifest); err == nil {
		t.Fatal("pending-delivery attestation authorised confirmed manifest")
	}
	fresh := pendingAttestation
	fresh.ManifestSHA256 = confirmedDigest
	if err := manifest.ConvergenceReady(fresh); err != nil {
		t.Fatalf("confirmed manifest did not converge with fresh attestation: %v", err)
	}
}

func TestFallbackDisclosureNeverChangesMateriality(t *testing.T) {
	context, candidate, verified := findingFixture(t, P3, P3, 0)
	manifest, err := NewDispositionManifest(context, ResolvedPolicy{PublishThreshold: P1, RepairThreshold: P3, Mode: PublishThroughP3Mode}, []CandidateDisposition{verifiedCandidate(candidate, verified)}, []VerifiedFinding{verified})
	if err != nil {
		t.Fatal(err)
	}
	entry := manifest.Findings[0]
	if entry.Material || entry.Publication != PublicationDisclosed || entry.Delivery != DeliveryNotRequired || entry.Repair != RepairNotTriggered {
		t.Fatalf("fallback disposition=%#v", entry)
	}
}

func TestDispositionIndexRoundTripsEveryOccurrenceAndReceipt(t *testing.T) {
	context, candidate, verified := findingFixture(t, P2, P2, 0)
	policy := ResolvedPolicy{PublishThreshold: P1, RepairThreshold: P3, Mode: DestinationMode, Destination: "backlog", Target: "owner/repo"}
	manifest, err := NewDispositionManifest(context, policy, []CandidateDisposition{verifiedCandidate(candidate, verified)}, []VerifiedFinding{verified})
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest := strings.Repeat("a", 64)
	receiptID, err := NewReceiptID(policy.Destination, policy.Target, verified.OccurrenceID, payloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.ConfirmDelivery(verified.OccurrenceID, DeliveryReceipt{SchemaVersion: 1, ReceiptID: receiptID, Destination: policy.Destination, Target: policy.Target, OccurrenceID: verified.OccurrenceID, PayloadSHA256: payloadDigest}); err != nil {
		t.Fatal(err)
	}
	index, err := manifest.Index()
	if err != nil {
		t.Fatal(err)
	}
	line, err := index.RecordLine()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseDispositionRecord(line)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Occurrences) != 1 || parsed.Occurrences[0].OccurrenceID != verified.OccurrenceID || parsed.Occurrences[0].ReceiptID != receiptID || parsed.Occurrences[0].PayloadSHA256 != payloadDigest || parsed.ManifestSHA256 == "" {
		t.Fatalf("parsed index=%#v", parsed)
	}
}

func TestUnresolvedCandidateRetainsEvidenceWithoutInventingVerifier(t *testing.T) {
	context, candidate, _ := findingFixture(t, P2, P2, 0)
	entry := CandidateDisposition{Candidate: candidate, Outcome: CandidateVerificationUnresolved, VerificationEvidence: VerificationEvidence{Rationale: "No independent checker returned a determinate verdict."}}
	manifest, err := NewDispositionManifest(context, ResolvedPolicy{PublishThreshold: P1, RepairThreshold: P3, Mode: PublishThroughP3Mode}, []CandidateDisposition{entry}, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.Digest()
	if err != nil || digest == "" {
		t.Fatalf("unresolved digest=%q err=%v", digest, err)
	}
	bar := BarAttestation{SchemaVersion: 1, ManifestSHA256: digest, Verdict: BarPass, Reasons: []string{}, ImplicatedBriefs: []BriefReason{}, Checker: BarChecker{Family: "claude", ID: "bar"}}
	if err := manifest.ConvergenceReady(bar); err == nil {
		t.Fatal("unresolved candidate was allowed to converge")
	}
}

func TestDispositionIndexRejectsIncompleteOrDuplicateRecords(t *testing.T) {
	context, candidate, verified := findingFixture(t, P2, P2, 0)
	manifest, err := NewDispositionManifest(context, ResolvedPolicy{PublishThreshold: P1, RepairThreshold: P3, Mode: PublishThroughP3Mode}, []CandidateDisposition{verifiedCandidate(candidate, verified)}, []VerifiedFinding{verified})
	if err != nil {
		t.Fatal(err)
	}
	index, err := manifest.Index()
	if err != nil {
		t.Fatal(err)
	}
	index.Occurrences = append(index.Occurrences, index.Occurrences[0])
	if _, err := index.RecordLine(); err == nil {
		t.Fatal("duplicate occurrence was accepted in compact index")
	}
	index.Occurrences = index.Occurrences[:1]
	index.Occurrences[0].Delivery = DeliveryConfirmed
	if _, err := index.RecordLine(); err == nil {
		t.Fatal("confirmed index occurrence without receipt evidence was accepted")
	}
}

func TestRepairEvidenceMustReferenceAUniquePriorOccurrenceAndCurrentLineage(t *testing.T) {
	context, candidate, verified := findingFixture(t, P1, P1, 0)
	manifest, err := NewDispositionManifest(context, ResolvedPolicy{PublishThreshold: P1, RepairThreshold: P3, Mode: PublishThroughP3Mode}, []CandidateDisposition{verifiedCandidate(candidate, verified)}, []VerifiedFinding{verified})
	if err != nil {
		t.Fatal(err)
	}
	prior := OccurrenceID("O-" + strings.Repeat("a", 64))
	manifest.RepairEvidence = []RepairEvidence{{PriorOccurrenceID: prior, LineageID: verified.LineageID, RepairCommitSHA: "repair-sha"}}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	manifest.RepairEvidence = append(manifest.RepairEvidence, manifest.RepairEvidence[0])
	if err := manifest.Validate(); err == nil {
		t.Fatal("duplicate prior repair occurrence was accepted")
	}
	manifest.RepairEvidence = manifest.RepairEvidence[:1]
	manifest.RepairEvidence[0].LineageID = "F-WXYZ"
	if err := manifest.Validate(); err == nil {
		t.Fatal("repair evidence for an absent current lineage was accepted")
	}
}

func TestCompactIndexFitsSixtyFourFullyReceiptedOccurrencesAndRejectsOversize(t *testing.T) {
	makeIndex := func(count int) DispositionIndex {
		index := DispositionIndex{SchemaVersion: 1, Context: testContext(), Policy: ResolvedPolicy{PublishThreshold: P1, RepairThreshold: P3, Mode: DestinationMode, Destination: "engineering-backlog", Target: "owner/repository/project"}, ManifestSHA256: strings.Repeat("a", 64)}
		for number := 1; number <= count; number++ {
			index.Occurrences = append(index.Occurrences, IndexOccurrence{
				OccurrenceID: OccurrenceID("O-" + fmt.Sprintf("%064x", number)), LineageID: "F-ABCD",
				VerifiedPriority: P3, Assurance: AgentJudgement, Publication: PublicationWithheld,
				Repair: RepairSelected, Delivery: DeliveryConfirmed, Destination: index.Policy.Destination,
				Target: index.Policy.Target, ReceiptID: ReceiptID("R-" + fmt.Sprintf("%064x", number)),
				PayloadSHA256: fmt.Sprintf("%064x", number),
			})
		}
		return index
	}
	line, err := makeIndex(64).RecordLine()
	if err != nil {
		t.Fatal(err)
	}
	if encoded := len(strings.TrimSuffix(strings.TrimPrefix(line, "<!-- Minos-Disposition: "), " -->")); encoded >= MaxDispositionIndexEncodedSize {
		t.Fatalf("64-occurrence proof encoded %d bytes", encoded)
	}
	if _, err := makeIndex(256).RecordLine(); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("oversized complete index error=%v", err)
	}
}

func findingFixture(t *testing.T, proposed, verifierPriority Priority, ordinal int) (ManifestContext, CandidateFinding, VerifiedFinding) {
	t.Helper()
	context := testContext()
	producer := Producer{Family: "codex", ID: "review-2", Ordinal: ordinal}
	candidateID, err := NewCandidateID(context.AttemptToken, producer)
	if err != nil {
		t.Fatal(err)
	}
	category := DefectCorrectness
	anchor := NewEvidenceAnchor("internal/example.go", HeadSide, 17, "blob-1", "return wrong")
	candidate := CandidateFinding{SchemaVersion: 1, CandidateID: candidateID, Anchor: anchor, Criterion: Criterion{Defect: &category}, ProposedPriority: proposed, Assurance: AgentJudgement, Title: "Wrong result", Message: "This path returns the wrong result.", Producer: producer}
	verifier := Producer{Family: "claude", ID: "verify-2", Ordinal: ordinal}
	severe, err := MoreSevere(proposed, verifierPriority)
	if err != nil {
		t.Fatal(err)
	}
	agreement := PriorityAgreed
	if proposed != verifierPriority {
		agreement = PriorityDisputed
	}
	verified := VerifiedFinding{SchemaVersion: 1, CandidateID: candidateID, LineageID: "F-ABCD", Anchor: anchor, Criterion: candidate.Criterion, ProposedPriority: proposed, VerifierPriority: verifierPriority, VerifiedPriority: severe, PriorityValidation: PriorityValidation{VerifierFamily: verifier.Family, VerifierID: verifier.ID, Agreement: agreement, Rationale: "Evidence and impact checked."}, Assurance: AgentJudgement, Title: candidate.Title, Message: candidate.Message, Producer: producer, Verifier: verifier}
	verified.OccurrenceID, err = NewOccurrenceID(context, verified)
	if err != nil {
		t.Fatal(err)
	}
	return context, candidate, verified
}

func verifiedCandidate(candidate CandidateFinding, verified VerifiedFinding) CandidateDisposition {
	return CandidateDisposition{Candidate: candidate, Outcome: CandidateVerified, OccurrenceID: verified.OccurrenceID, VerificationEvidence: VerificationEvidence{Verifier: verified.Verifier, Rationale: "Confirmed independently."}}
}

func testContext() ManifestContext {
	return ManifestContext{Forge: "forgejo", Owner: "owner", Repo: "repo", PullRequest: 7, HeadSHA: "head-1", TargetSHA: "target-1", AttemptToken: 17, GoverningIdentity: "governing-1"}
}
