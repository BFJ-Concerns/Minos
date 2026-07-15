package findings

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

type ManifestContext struct {
	Forge             string `json:"forge"`
	Owner             string `json:"owner"`
	Repo              string `json:"repo"`
	PullRequest       int64  `json:"pull_request"`
	HeadSHA           string `json:"head_sha"`
	TargetSHA         string `json:"target_sha"`
	AttemptToken      int64  `json:"attempt_token"`
	GoverningIdentity string `json:"governing_identity"`
}

// MaxDispositionIndexEncodedSize leaves room for the human review and trailing
// product record within a conservative 64 KiB review-body budget. The size
// proof below exercises 64 fully receipted occurrences; larger complete sets
// are still attempted and fail partial rather than being truncated.
const MaxDispositionIndexEncodedSize = 60 * 1024

type DispositionIndex struct {
	SchemaVersion  int               `json:"schema_version"`
	Context        ManifestContext   `json:"context"`
	Policy         ResolvedPolicy    `json:"policy"`
	ManifestSHA256 string            `json:"manifest_sha256"`
	Occurrences    []IndexOccurrence `json:"occurrences"`
}

type IndexOccurrence struct {
	OccurrenceID     OccurrenceID           `json:"occurrence_id"`
	LineageID        LineageID              `json:"lineage_id"`
	VerifiedPriority Priority               `json:"verified_priority"`
	Assurance        Assurance              `json:"assurance"`
	Publication      PublicationDisposition `json:"publication"`
	Repair           RepairDisposition      `json:"repair"`
	Delivery         DeliveryState          `json:"delivery"`
	Destination      string                 `json:"destination,omitempty"`
	Target           string                 `json:"target,omitempty"`
	ReceiptID        ReceiptID              `json:"receipt_id,omitempty"`
	PayloadSHA256    string                 `json:"payload_sha256,omitempty"`
}

func (manifest DispositionManifest) Index() (DispositionIndex, error) {
	digest, err := manifest.Digest()
	if err != nil {
		return DispositionIndex{}, err
	}
	index := DispositionIndex{SchemaVersion: 1, Context: manifest.Context, Policy: manifest.Policy, ManifestSHA256: digest, Occurrences: make([]IndexOccurrence, 0, len(manifest.Findings))}
	for _, entry := range manifest.Findings {
		occurrence := IndexOccurrence{
			OccurrenceID: entry.Finding.OccurrenceID, LineageID: entry.Finding.LineageID,
			VerifiedPriority: entry.Finding.VerifiedPriority, Assurance: entry.Finding.Assurance,
			Publication: entry.Publication, Repair: entry.Repair, Delivery: entry.Delivery,
		}
		if manifest.Policy.Mode == DestinationMode && !entry.Material {
			occurrence.Destination = manifest.Policy.Destination
			occurrence.Target = manifest.Policy.Target
		}
		if entry.Receipt != nil {
			occurrence.ReceiptID = entry.Receipt.ReceiptID
			occurrence.PayloadSHA256 = entry.Receipt.PayloadSHA256
		}
		index.Occurrences = append(index.Occurrences, occurrence)
	}
	return index, nil
}

func (index DispositionIndex) Validate() error {
	if index.SchemaVersion != 1 || validateContext(index.Context) != nil || index.Policy.Validate() != nil || !validSHA256(index.ManifestSHA256) {
		return fmt.Errorf("invalid disposition index")
	}
	seen := make(map[OccurrenceID]struct{}, len(index.Occurrences))
	for _, occurrence := range index.Occurrences {
		if !occurrence.OccurrenceID.Valid() || !occurrence.LineageID.Valid() || !occurrence.VerifiedPriority.Valid() || !occurrence.Assurance.Valid() || !occurrence.Publication.Valid() || !occurrence.Repair.Valid() || !occurrence.Delivery.Valid() {
			return fmt.Errorf("invalid disposition index occurrence %s", occurrence.OccurrenceID)
		}
		if _, duplicate := seen[occurrence.OccurrenceID]; duplicate {
			return fmt.Errorf("duplicate disposition index occurrence %s", occurrence.OccurrenceID)
		}
		seen[occurrence.OccurrenceID] = struct{}{}
		quietDestination := index.Policy.Mode == DestinationMode && occurrence.Publication == PublicationWithheld
		if quietDestination {
			if occurrence.Destination != index.Policy.Destination || occurrence.Target != index.Policy.Target || occurrence.Delivery == DeliveryNotRequired {
				return fmt.Errorf("destination index occurrence %s lacks complete routing identity", occurrence.OccurrenceID)
			}
		} else if occurrence.Destination != "" || occurrence.Target != "" {
			return fmt.Errorf("non-destination index occurrence %s carries routing identity", occurrence.OccurrenceID)
		}
		if occurrence.Delivery == DeliveryConfirmed {
			if occurrence.Destination == "" || occurrence.Target == "" || !occurrence.ReceiptID.Valid() || !validSHA256(occurrence.PayloadSHA256) {
				return fmt.Errorf("confirmed index occurrence %s lacks complete delivery evidence", occurrence.OccurrenceID)
			}
		} else if occurrence.ReceiptID != "" || occurrence.PayloadSHA256 != "" {
			return fmt.Errorf("unconfirmed index occurrence %s carries delivery evidence", occurrence.OccurrenceID)
		}
	}
	return nil
}

func (index DispositionIndex) RecordLine() (string, error) {
	if err := index.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(index)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > MaxDispositionIndexEncodedSize {
		return "", fmt.Errorf("complete disposition index is %d bytes, maximum is %d", len(encoded), MaxDispositionIndexEncodedSize)
	}
	return "<!-- Minos-Disposition: " + encoded + " -->", nil
}

func ParseDispositionRecord(line string) (DispositionIndex, error) {
	const prefix = "<!-- Minos-Disposition: "
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, " -->") {
		return DispositionIndex{}, fmt.Errorf("invalid disposition index record")
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(line, prefix), " -->")
	if len(encoded) > MaxDispositionIndexEncodedSize {
		return DispositionIndex{}, fmt.Errorf("disposition index exceeds maximum size")
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return DispositionIndex{}, err
	}
	var index DispositionIndex
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return DispositionIndex{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return DispositionIndex{}, fmt.Errorf("trailing disposition index JSON")
	}
	if err := index.Validate(); err != nil {
		return DispositionIndex{}, err
	}
	return index, nil
}

func validateContext(context ManifestContext) error {
	if strings.TrimSpace(context.Forge) == "" || strings.TrimSpace(context.Owner) == "" || strings.TrimSpace(context.Repo) == "" || context.PullRequest <= 0 || strings.TrimSpace(context.HeadSHA) == "" || strings.TrimSpace(context.TargetSHA) == "" || context.AttemptToken <= 0 || strings.TrimSpace(context.GoverningIdentity) == "" {
		return fmt.Errorf("incomplete manifest context")
	}
	return nil
}

type ResolvedPolicy struct {
	PublishThreshold Priority        `json:"publish_threshold"`
	RepairThreshold  Priority        `json:"repair_threshold"`
	Mode             DispositionMode `json:"mode"`
	Destination      string          `json:"destination,omitempty"`
	Target           string          `json:"target,omitempty"`
}

func (policy ResolvedPolicy) Validate() error {
	if !policy.PublishThreshold.Valid() || !policy.RepairThreshold.Valid() || !policy.Mode.Valid() {
		return fmt.Errorf("invalid finding policy")
	}
	if !AtOrAbove(policy.PublishThreshold, policy.RepairThreshold) {
		return fmt.Errorf("repair threshold %s is narrower than publication threshold %s", policy.RepairThreshold, policy.PublishThreshold)
	}
	switch policy.Mode {
	case DestinationMode:
		if strings.TrimSpace(policy.Destination) == "" || strings.TrimSpace(policy.Target) == "" {
			return fmt.Errorf("destination mode requires destination and target")
		}
	case PublishThroughP3Mode:
		if policy.Destination != "" || policy.Target != "" {
			return fmt.Errorf("publish-through-p3 mode forbids destination and target")
		}
	}
	return nil
}

type DispositionManifest struct {
	SchemaVersion  int                    `json:"schema_version"`
	Context        ManifestContext        `json:"context"`
	Policy         ResolvedPolicy         `json:"policy"`
	Candidates     []CandidateDisposition `json:"candidates"`
	Findings       []FindingDisposition   `json:"findings"`
	RepairEvidence []RepairEvidence       `json:"repair_evidence"`
}

func NewDispositionManifest(context ManifestContext, policy ResolvedPolicy, candidates []CandidateDisposition, verified []VerifiedFinding) (DispositionManifest, error) {
	if err := validateContext(context); err != nil {
		return DispositionManifest{}, err
	}
	if err := policy.Validate(); err != nil {
		return DispositionManifest{}, err
	}
	manifest := DispositionManifest{SchemaVersion: 1, Context: context, Policy: policy, Candidates: candidates, Findings: make([]FindingDisposition, 0, len(verified)), RepairEvidence: []RepairEvidence{}}
	material := false
	for _, finding := range verified {
		if AtOrAbove(finding.VerifiedPriority, policy.PublishThreshold) {
			material = true
		}
	}
	for _, finding := range verified {
		isMaterial := AtOrAbove(finding.VerifiedPriority, policy.PublishThreshold)
		eligible := AtOrAbove(finding.VerifiedPriority, policy.RepairThreshold)
		disposition := FindingDisposition{Finding: finding, Material: isMaterial, RepairEligible: eligible}
		if policy.Mode == DestinationMode {
			if isMaterial {
				disposition.Publication = PublicationPublished
				disposition.Delivery = DeliveryNotRequired
			} else {
				disposition.Publication = PublicationWithheld
				disposition.Delivery = DeliveryPending
			}
		} else {
			if isMaterial {
				disposition.Publication = PublicationPublished
			} else {
				disposition.Publication = PublicationDisclosed
			}
			disposition.Delivery = DeliveryNotRequired
		}
		switch {
		case !eligible:
			disposition.Repair = RepairNotEligible
		case !material:
			disposition.Repair = RepairNotTriggered
		default:
			disposition.Repair = RepairSelected
		}
		manifest.Findings = append(manifest.Findings, disposition)
	}
	if err := manifest.Validate(); err != nil {
		return DispositionManifest{}, err
	}
	return manifest, nil
}

func (manifest DispositionManifest) Validate() error {
	if manifest.SchemaVersion != 1 {
		return fmt.Errorf("unsupported disposition manifest schema %d", manifest.SchemaVersion)
	}
	if err := validateContext(manifest.Context); err != nil {
		return err
	}
	if err := manifest.Policy.Validate(); err != nil {
		return err
	}
	candidates := make(map[CandidateID]CandidateDisposition, len(manifest.Candidates))
	verifiedCandidates := make(map[CandidateID]OccurrenceID)
	for _, entry := range manifest.Candidates {
		if err := ValidateCandidate(entry.Candidate); err != nil {
			return fmt.Errorf("candidate %s: %w", entry.Candidate.CandidateID, err)
		}
		expectedID, err := NewCandidateID(manifest.Context.AttemptToken, entry.Candidate.Producer)
		if err != nil || entry.Candidate.CandidateID != expectedID {
			return fmt.Errorf("candidate %s identity does not match manifest attempt and producer", entry.Candidate.CandidateID)
		}
		if _, duplicate := candidates[entry.Candidate.CandidateID]; duplicate {
			return fmt.Errorf("duplicate candidate %s", entry.Candidate.CandidateID)
		}
		if !entry.Outcome.Valid() || strings.TrimSpace(entry.VerificationEvidence.Rationale) == "" {
			return fmt.Errorf("candidate %s has invalid verification outcome or evidence", entry.Candidate.CandidateID)
		}
		switch entry.Outcome {
		case CandidateVerified:
			if errProducer(entry.VerificationEvidence.Verifier) != nil || !entry.OccurrenceID.Valid() {
				return fmt.Errorf("verified candidate %s lacks occurrence identity", entry.Candidate.CandidateID)
			}
			verifiedCandidates[entry.Candidate.CandidateID] = entry.OccurrenceID
		case CandidateSuppressed:
			if errProducer(entry.VerificationEvidence.Verifier) != nil {
				return fmt.Errorf("suppressed candidate %s lacks verifier rejection evidence", entry.Candidate.CandidateID)
			}
			if entry.OccurrenceID != "" {
				return fmt.Errorf("non-verified candidate %s has occurrence identity", entry.Candidate.CandidateID)
			}
		case CandidateVerificationUnresolved:
			if entry.OccurrenceID != "" {
				return fmt.Errorf("non-verified candidate %s has occurrence identity", entry.Candidate.CandidateID)
			}
		}
		candidates[entry.Candidate.CandidateID] = entry
	}
	occurrences := make(map[OccurrenceID]struct{}, len(manifest.Findings))
	materialPresent := false
	for _, entry := range manifest.Findings {
		finding := entry.Finding
		if err := ValidateVerifiedFinding(finding); err != nil {
			return fmt.Errorf("verified finding: %w", err)
		}
		candidate, exists := candidates[finding.CandidateID]
		if !exists || candidate.Outcome != CandidateVerified || candidate.OccurrenceID != finding.OccurrenceID {
			return fmt.Errorf("finding %s has no matching verified candidate", finding.OccurrenceID)
		}
		if finding.Anchor != candidate.Candidate.Anchor || !reflect.DeepEqual(finding.Criterion, candidate.Candidate.Criterion) || finding.ProposedPriority != candidate.Candidate.ProposedPriority || finding.Assurance != candidate.Candidate.Assurance || finding.Title != candidate.Candidate.Title || finding.Message != candidate.Candidate.Message || finding.Suggestion != candidate.Candidate.Suggestion || finding.Producer != candidate.Candidate.Producer {
			return fmt.Errorf("finding %s changed immutable candidate fields", finding.OccurrenceID)
		}
		expectedOccurrence, err := NewOccurrenceID(manifest.Context, finding)
		if err != nil || expectedOccurrence != finding.OccurrenceID {
			return fmt.Errorf("finding occurrence identity mismatch")
		}
		if _, duplicate := occurrences[finding.OccurrenceID]; duplicate {
			return fmt.Errorf("duplicate occurrence %s", finding.OccurrenceID)
		}
		occurrences[finding.OccurrenceID] = struct{}{}
		wantMaterial := AtOrAbove(finding.VerifiedPriority, manifest.Policy.PublishThreshold)
		wantEligible := AtOrAbove(finding.VerifiedPriority, manifest.Policy.RepairThreshold)
		if entry.Material != wantMaterial || entry.RepairEligible != wantEligible {
			return fmt.Errorf("finding %s has incorrect derived policy fields", finding.OccurrenceID)
		}
		if wantMaterial {
			materialPresent = true
		}
		if err := validateDisposition(manifest.Policy, entry); err != nil {
			return fmt.Errorf("finding %s: %w", finding.OccurrenceID, err)
		}
	}
	if len(occurrences) != len(verifiedCandidates) {
		return fmt.Errorf("verified candidate/finding set is not exhaustive")
	}
	for candidateID, occurrenceID := range verifiedCandidates {
		if _, ok := occurrences[occurrenceID]; !ok {
			return fmt.Errorf("verified candidate %s is omitted from findings", candidateID)
		}
	}
	for _, entry := range manifest.Findings {
		if !entry.RepairEligible && entry.Repair != RepairNotEligible {
			return fmt.Errorf("ineligible finding has repair disposition %s", entry.Repair)
		}
		if entry.RepairEligible && !materialPresent && entry.Repair != RepairNotTriggered {
			return fmt.Errorf("quiet-only manifest has repair disposition %s", entry.Repair)
		}
		if entry.RepairEligible && materialPresent {
			if entry.Material && entry.Repair != RepairSelected && entry.Repair != RepairRepaired && entry.Repair != RepairBlocked && entry.Repair != RepairFruitless {
				return fmt.Errorf("material repair has invalid disposition %s", entry.Repair)
			}
			if !entry.Material && entry.Repair != RepairSelected && entry.Repair != RepairRepaired && entry.Repair != RepairAnnexeRouted && entry.Repair != RepairDeferred {
				return fmt.Errorf("quiet selected repair has invalid disposition %s", entry.Repair)
			}
		}
	}
	repairEvidence := make(map[OccurrenceID]struct{}, len(manifest.RepairEvidence))
	currentLineages := make(map[LineageID]struct{}, len(manifest.Findings))
	for _, entry := range manifest.Findings {
		currentLineages[entry.Finding.LineageID] = struct{}{}
	}
	for _, evidence := range manifest.RepairEvidence {
		if !evidence.PriorOccurrenceID.Valid() || !evidence.LineageID.Valid() || strings.TrimSpace(evidence.RepairCommitSHA) == "" {
			return fmt.Errorf("invalid repair evidence")
		}
		if _, duplicate := repairEvidence[evidence.PriorOccurrenceID]; duplicate {
			return fmt.Errorf("duplicate repair evidence for %s", evidence.PriorOccurrenceID)
		}
		repairEvidence[evidence.PriorOccurrenceID] = struct{}{}
		if _, exists := currentLineages[evidence.LineageID]; !exists {
			return fmt.Errorf("repair evidence lineage %s has no current finding", evidence.LineageID)
		}
	}
	return nil
}

func validateDisposition(policy ResolvedPolicy, entry FindingDisposition) error {
	if !entry.Publication.Valid() || !entry.Repair.Valid() || !entry.Delivery.Valid() {
		return fmt.Errorf("unknown disposition value")
	}
	wantPublication := PublicationPublished
	wantDelivery := DeliveryNotRequired
	if !entry.Material {
		if policy.Mode == DestinationMode {
			wantPublication = PublicationWithheld
			if entry.Delivery != DeliveryConfirmed {
				wantDelivery = DeliveryPending
			} else {
				wantDelivery = DeliveryConfirmed
			}
		} else {
			wantPublication = PublicationDisclosed
		}
	}
	if entry.Publication != wantPublication || entry.Delivery != wantDelivery {
		return fmt.Errorf("disposition disagrees with policy")
	}
	if entry.Delivery == DeliveryConfirmed {
		if entry.Receipt == nil || entry.Receipt.OccurrenceID != entry.Finding.OccurrenceID || entry.Receipt.Destination != policy.Destination || entry.Receipt.Target != policy.Target {
			return fmt.Errorf("confirmed delivery lacks matching receipt")
		}
		if err := entry.Receipt.Validate(); err != nil {
			return err
		}
	} else if entry.Receipt != nil {
		return fmt.Errorf("non-confirmed delivery carries receipt")
	}
	if entry.Material && (entry.Repair == RepairAnnexeRouted || entry.Repair == RepairDeferred) {
		return fmt.Errorf("material finding cannot be routed or deferred")
	}
	if !entry.Material && (entry.Repair == RepairBlocked || entry.Repair == RepairFruitless) {
		return fmt.Errorf("quiet finding cannot gain blocking repair disposition")
	}
	return nil
}

func (manifest DispositionManifest) Digest() (string, error) {
	if err := manifest.Validate(); err != nil {
		return "", err
	}
	return canonicalDigest(manifest)
}

func (manifest *DispositionManifest) ConfirmDelivery(occurrenceID OccurrenceID, receipt DeliveryReceipt) error {
	if manifest == nil {
		return fmt.Errorf("disposition manifest is required")
	}
	for index := range manifest.Findings {
		entry := &manifest.Findings[index]
		if entry.Finding.OccurrenceID != occurrenceID {
			continue
		}
		if entry.Material || entry.Delivery != DeliveryPending || entry.Receipt != nil {
			return fmt.Errorf("finding %s is not awaiting durable delivery", occurrenceID)
		}
		entry.Delivery = DeliveryConfirmed
		entry.Receipt = &receipt
		if err := manifest.Validate(); err != nil {
			entry.Delivery = DeliveryPending
			entry.Receipt = nil
			return err
		}
		return nil
	}
	return fmt.Errorf("finding %s is absent from the disposition manifest", occurrenceID)
}

func DecodeManifest(data []byte) (DispositionManifest, error) {
	var manifest DispositionManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return DispositionManifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return DispositionManifest{}, fmt.Errorf("trailing manifest JSON")
	}
	if err := manifest.Validate(); err != nil {
		return DispositionManifest{}, err
	}
	return manifest, nil
}

type BarVerdict string

const (
	BarPass       BarVerdict = "pass"
	BarFail       BarVerdict = "fail"
	BarUnresolved BarVerdict = "unresolved"
)

type BriefReason struct {
	Brief   string   `json:"brief"`
	Reasons []string `json:"reasons"`
}

type CandidateReconsideration struct {
	CandidateID CandidateID `json:"candidate_id"`
	Reasons     []string    `json:"reasons"`
}

type BarChecker struct {
	Family          string `json:"family"`
	ID              string `json:"id"`
	DegradedPairing bool   `json:"degraded_pairing"`
}

type BarAttestation struct {
	SchemaVersion             int                        `json:"schema_version"`
	ManifestSHA256            string                     `json:"manifest_sha256"`
	Verdict                   BarVerdict                 `json:"verdict"`
	Reasons                   []string                   `json:"reasons"`
	ImplicatedBriefs          []BriefReason              `json:"implicated_briefs"`
	CandidateReconsiderations []CandidateReconsideration `json:"candidate_reconsiderations"`
	Checker                   BarChecker                 `json:"checker"`
}

func (attestation BarAttestation) ValidateFor(manifest DispositionManifest) error {
	if attestation.SchemaVersion != 1 || !validSHA256(attestation.ManifestSHA256) || (attestation.Verdict != BarPass && attestation.Verdict != BarFail && attestation.Verdict != BarUnresolved) || strings.TrimSpace(attestation.Checker.Family) == "" || strings.TrimSpace(attestation.Checker.ID) == "" || attestation.CandidateReconsiderations == nil {
		return fmt.Errorf("invalid bar attestation")
	}
	digest, err := manifest.Digest()
	if err != nil {
		return err
	}
	if attestation.ManifestSHA256 != digest {
		return fmt.Errorf("bar attestation names manifest %s, want %s", attestation.ManifestSHA256, digest)
	}
	if len(attestation.CandidateReconsiderations) > 0 && attestation.Verdict != BarFail {
		return fmt.Errorf("only a failed bar may reconsider a suppressed candidate")
	}
	candidates := make(map[CandidateID]CandidateOutcome, len(manifest.Candidates))
	for _, entry := range manifest.Candidates {
		candidates[entry.Candidate.CandidateID] = entry.Outcome
	}
	reasons := make(map[string]struct{}, len(attestation.Reasons))
	for _, reason := range attestation.Reasons {
		reasons[reason] = struct{}{}
	}
	seen := make(map[CandidateID]struct{}, len(attestation.CandidateReconsiderations))
	for _, reconsideration := range attestation.CandidateReconsiderations {
		if !reconsideration.CandidateID.Valid() || len(reconsideration.Reasons) == 0 {
			return fmt.Errorf("invalid candidate reconsideration")
		}
		if _, duplicate := seen[reconsideration.CandidateID]; duplicate {
			return fmt.Errorf("duplicate candidate reconsideration %s", reconsideration.CandidateID)
		}
		seen[reconsideration.CandidateID] = struct{}{}
		if candidates[reconsideration.CandidateID] != CandidateSuppressed {
			return fmt.Errorf("candidate reconsideration %s does not name a suppressed manifest candidate", reconsideration.CandidateID)
		}
		for _, reason := range reconsideration.Reasons {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("candidate reconsideration %s has an empty reason", reconsideration.CandidateID)
			}
			if _, present := reasons[reason]; !present {
				return fmt.Errorf("candidate reconsideration %s is not bound to a bar reason", reconsideration.CandidateID)
			}
		}
	}
	return nil
}

func DecodeBarAttestation(data []byte, manifest DispositionManifest) (BarAttestation, error) {
	var attestation BarAttestation
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&attestation); err != nil {
		return BarAttestation{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return BarAttestation{}, fmt.Errorf("trailing bar attestation JSON")
	}
	if err := attestation.ValidateFor(manifest); err != nil {
		return BarAttestation{}, err
	}
	return attestation, nil
}

func (manifest DispositionManifest) HasMaterialFindings() bool {
	for _, finding := range manifest.Findings {
		if finding.Material {
			return true
		}
	}
	return false
}

func (manifest DispositionManifest) HasConfirmedQuietDelivery() bool {
	for _, finding := range manifest.Findings {
		if !finding.Material && finding.Delivery == DeliveryConfirmed {
			return true
		}
	}
	return false
}

func (manifest DispositionManifest) ConvergenceReady(attestation BarAttestation) error {
	if err := attestation.ValidateFor(manifest); err != nil {
		return err
	}
	if attestation.Verdict != BarPass || manifest.HasMaterialFindings() {
		return fmt.Errorf("manifest has not converged")
	}
	for _, candidate := range manifest.Candidates {
		if candidate.Outcome == CandidateVerificationUnresolved {
			return fmt.Errorf("candidate verification unresolved")
		}
	}
	for _, finding := range manifest.Findings {
		if finding.Delivery == DeliveryPending || !finding.Repair.Terminal() {
			return fmt.Errorf("finding %s has incomplete disposition", finding.Finding.OccurrenceID)
		}
	}
	return nil
}
