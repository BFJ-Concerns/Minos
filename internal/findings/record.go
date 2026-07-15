package findings

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
)

type EvidenceSide string

const (
	HeadSide EvidenceSide = "head"
	BaseSide EvidenceSide = "base"
)

type EvidenceAnchor struct {
	Path        string       `json:"path"`
	Side        EvidenceSide `json:"side"`
	DisplayLine int          `json:"display_line"`
	BlobSHA     string       `json:"blob_sha"`
	CodeQuote   string       `json:"code_quote"`
	QuoteSHA256 string       `json:"quote_sha256"`
}

func NewEvidenceAnchor(filePath string, side EvidenceSide, displayLine int, blobSHA, quote string) EvidenceAnchor {
	digest := sha256.Sum256([]byte(quote))
	return EvidenceAnchor{Path: filePath, Side: side, DisplayLine: displayLine, BlobSHA: blobSHA, CodeQuote: quote, QuoteSHA256: hex.EncodeToString(digest[:])}
}

func (anchor EvidenceAnchor) Validate() error {
	if anchor.Path == "" || path.IsAbs(anchor.Path) || path.Clean(anchor.Path) != anchor.Path || anchor.Path == "." || strings.HasPrefix(anchor.Path, "../") {
		return fmt.Errorf("unsafe evidence path %q", anchor.Path)
	}
	if anchor.Side != HeadSide && anchor.Side != BaseSide {
		return fmt.Errorf("invalid evidence side %q", anchor.Side)
	}
	if anchor.DisplayLine <= 0 || strings.TrimSpace(anchor.BlobSHA) == "" || anchor.CodeQuote == "" {
		return fmt.Errorf("incomplete evidence anchor")
	}
	digest := sha256.Sum256([]byte(anchor.CodeQuote))
	if anchor.QuoteSHA256 != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("evidence quote digest mismatch")
	}
	return nil
}

type DefectCategory string

const (
	DefectCorrectness        DefectCategory = "correctness"
	DefectSecurity           DefectCategory = "security"
	DefectErrorHandling      DefectCategory = "error-handling"
	DefectTests              DefectCategory = "tests"
	DefectTypes              DefectCategory = "types"
	DefectComments           DefectCategory = "comments"
	DefectSimplification     DefectCategory = "simplification"
	DefectGeneralCorrectness DefectCategory = "general-correctness"
)

func (category DefectCategory) Valid() bool {
	switch category {
	case DefectCorrectness, DefectSecurity, DefectErrorHandling, DefectTests, DefectTypes, DefectComments, DefectSimplification, DefectGeneralCorrectness:
		return true
	default:
		return false
	}
}

type BriefCriterion struct {
	TrustedTargetSHA string `json:"trusted_target_sha"`
	BriefPath        string `json:"brief_path"`
	BriefBlobSHA     string `json:"brief_blob_sha"`
}

type Criterion struct {
	Brief  *BriefCriterion `json:"brief,omitempty"`
	Defect *DefectCategory `json:"defect,omitempty"`
}

func (criterion Criterion) Validate() error {
	if (criterion.Brief == nil) == (criterion.Defect == nil) {
		return fmt.Errorf("criterion must select exactly one of brief or defect")
	}
	if criterion.Brief != nil {
		if strings.TrimSpace(criterion.Brief.TrustedTargetSHA) == "" || strings.TrimSpace(criterion.Brief.BriefBlobSHA) == "" || criterion.Brief.BriefPath == "" || path.IsAbs(criterion.Brief.BriefPath) || path.Clean(criterion.Brief.BriefPath) != criterion.Brief.BriefPath {
			return fmt.Errorf("invalid brief criterion")
		}
	}
	if criterion.Defect != nil && !criterion.Defect.Valid() {
		return fmt.Errorf("invalid defect category %q", *criterion.Defect)
	}
	return nil
}

type Producer struct {
	Family  string `json:"family"`
	ID      string `json:"id"`
	Ordinal int    `json:"ordinal"`
}

func errProducer(producer Producer) error {
	if strings.TrimSpace(producer.Family) == "" || strings.TrimSpace(producer.ID) == "" || producer.Ordinal < 0 {
		return fmt.Errorf("invalid producer identity")
	}
	return nil
}

type CandidateFinding struct {
	SchemaVersion    int            `json:"schema_version"`
	CandidateID      CandidateID    `json:"candidate_id"`
	Anchor           EvidenceAnchor `json:"anchor"`
	Criterion        Criterion      `json:"criterion"`
	ProposedPriority Priority       `json:"proposed_priority"`
	Assurance        Assurance      `json:"assurance"`
	Title            string         `json:"title"`
	Message          string         `json:"message"`
	Suggestion       string         `json:"suggestion,omitempty"`
	Producer         Producer       `json:"producer"`
}

func ValidateCandidate(candidate CandidateFinding) error {
	if candidate.SchemaVersion != 1 || !candidate.CandidateID.Valid() || !candidate.ProposedPriority.Valid() || !candidate.Assurance.Valid() {
		return fmt.Errorf("invalid candidate schema or closed field")
	}
	if err := candidate.Anchor.Validate(); err != nil {
		return err
	}
	if err := candidate.Criterion.Validate(); err != nil {
		return err
	}
	if err := errProducer(candidate.Producer); err != nil {
		return err
	}
	if strings.TrimSpace(candidate.Title) == "" || strings.TrimSpace(candidate.Message) == "" {
		return fmt.Errorf("candidate title and message are required")
	}
	return nil
}

type PriorityAgreement string

const (
	PriorityAgreed   PriorityAgreement = "agreed"
	PriorityDisputed PriorityAgreement = "disputed"
)

type PriorityValidation struct {
	VerifierFamily string            `json:"verifier_family"`
	VerifierID     string            `json:"verifier_id"`
	Agreement      PriorityAgreement `json:"agreement"`
	Rationale      string            `json:"rationale"`
}

type VerifiedFinding struct {
	SchemaVersion      int                `json:"schema_version"`
	CandidateID        CandidateID        `json:"candidate_id"`
	OccurrenceID       OccurrenceID       `json:"occurrence_id"`
	LineageID          LineageID          `json:"lineage_id"`
	Anchor             EvidenceAnchor     `json:"anchor"`
	Criterion          Criterion          `json:"criterion"`
	ProposedPriority   Priority           `json:"proposed_priority"`
	VerifierPriority   Priority           `json:"verifier_priority"`
	VerifiedPriority   Priority           `json:"verified_priority"`
	PriorityValidation PriorityValidation `json:"priority_validation"`
	Assurance          Assurance          `json:"assurance"`
	Title              string             `json:"title"`
	Message            string             `json:"message"`
	Suggestion         string             `json:"suggestion,omitempty"`
	Producer           Producer           `json:"producer"`
	Verifier           Producer           `json:"verifier"`
}

func ValidateVerifiedFinding(finding VerifiedFinding) error {
	if finding.SchemaVersion != 1 || !finding.CandidateID.Valid() || !finding.LineageID.Valid() || !finding.Assurance.Valid() {
		return fmt.Errorf("invalid verified finding schema or closed field")
	}
	if err := finding.Anchor.Validate(); err != nil {
		return err
	}
	if err := finding.Criterion.Validate(); err != nil {
		return err
	}
	if err := errProducer(finding.Producer); err != nil {
		return err
	}
	if err := errProducer(finding.Verifier); err != nil {
		return err
	}
	severe, err := MoreSevere(finding.ProposedPriority, finding.VerifierPriority)
	if err != nil || finding.VerifiedPriority != severe {
		return fmt.Errorf("verified priority is not the conservative producer/verifier result")
	}
	wantAgreement := PriorityAgreed
	if finding.ProposedPriority != finding.VerifierPriority {
		wantAgreement = PriorityDisputed
	}
	if finding.PriorityValidation.Agreement != wantAgreement || finding.PriorityValidation.VerifierFamily != finding.Verifier.Family || finding.PriorityValidation.VerifierID != finding.Verifier.ID || strings.TrimSpace(finding.PriorityValidation.Rationale) == "" {
		return fmt.Errorf("invalid priority validation evidence")
	}
	if strings.TrimSpace(finding.Title) == "" || strings.TrimSpace(finding.Message) == "" {
		return fmt.Errorf("verified finding title and message are required")
	}
	return nil
}

type VerificationEvidence struct {
	Verifier  Producer `json:"verifier"`
	Rationale string   `json:"rationale"`
}

type CandidateDisposition struct {
	Candidate            CandidateFinding     `json:"candidate"`
	Outcome              CandidateOutcome     `json:"outcome"`
	OccurrenceID         OccurrenceID         `json:"occurrence_id,omitempty"`
	VerificationEvidence VerificationEvidence `json:"verification_evidence"`
}

type DeliveryReceipt struct {
	SchemaVersion int          `json:"schema_version"`
	ReceiptID     ReceiptID    `json:"receipt_id"`
	Destination   string       `json:"destination"`
	Target        string       `json:"target"`
	OccurrenceID  OccurrenceID `json:"occurrence_id"`
	PayloadSHA256 string       `json:"payload_sha256"`
}

func (receipt DeliveryReceipt) Validate() error {
	if receipt.SchemaVersion != 1 || !receipt.ReceiptID.Valid() || receipt.Destination == "" || receipt.Target == "" || !receipt.OccurrenceID.Valid() || !validSHA256(receipt.PayloadSHA256) {
		return fmt.Errorf("invalid delivery receipt")
	}
	expected, err := NewReceiptID(receipt.Destination, receipt.Target, receipt.OccurrenceID, receipt.PayloadSHA256)
	if err != nil || receipt.ReceiptID != expected {
		return fmt.Errorf("delivery receipt identity mismatch")
	}
	return nil
}

type FindingDisposition struct {
	Finding        VerifiedFinding        `json:"finding"`
	Material       bool                   `json:"material"`
	RepairEligible bool                   `json:"repair_eligible"`
	Publication    PublicationDisposition `json:"publication"`
	Repair         RepairDisposition      `json:"repair"`
	Delivery       DeliveryState          `json:"delivery"`
	Receipt        *DeliveryReceipt       `json:"receipt,omitempty"`
}

type RepairEvidence struct {
	PriorOccurrenceID OccurrenceID `json:"prior_occurrence_id"`
	LineageID         LineageID    `json:"lineage_id"`
	RepairCommitSHA   string       `json:"repair_commit_sha"`
}
