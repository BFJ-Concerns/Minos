package findings

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

var (
	candidateIDPattern  = regexp.MustCompile(`^C-[0-9a-f]{64}$`)
	occurrenceIDPattern = regexp.MustCompile(`^O-[0-9a-f]{64}$`)
	receiptIDPattern    = regexp.MustCompile(`^R-[0-9a-f]{64}$`)
	lineageIDPattern    = regexp.MustCompile(`^F-[0-9A-HJKMNP-TV-Z]{4}$`)
	sha256Pattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type CandidateID string
type OccurrenceID string
type ReceiptID string
type LineageID string

func (id CandidateID) Valid() bool  { return candidateIDPattern.MatchString(string(id)) }
func (id OccurrenceID) Valid() bool { return occurrenceIDPattern.MatchString(string(id)) }
func (id ReceiptID) Valid() bool    { return receiptIDPattern.MatchString(string(id)) }
func (id LineageID) Valid() bool    { return lineageIDPattern.MatchString(string(id)) }

func ParseLineageID(value string) (LineageID, error) {
	id := LineageID(value)
	if !id.Valid() {
		return "", fmt.Errorf("invalid lineage identity %q", value)
	}
	return id, nil
}

type candidateIdentityInput struct {
	SchemaVersion   int    `json:"schema_version"`
	AttemptToken    int64  `json:"attempt_token"`
	ProducerFamily  string `json:"producer_family"`
	ProducerID      string `json:"producer_id"`
	ProducerOrdinal int    `json:"producer_ordinal"`
}

func NewCandidateID(attemptToken int64, producer Producer) (CandidateID, error) {
	if attemptToken <= 0 || errProducer(producer) != nil {
		return "", fmt.Errorf("invalid candidate identity input")
	}
	input := candidateIdentityInput{1, attemptToken, producer.Family, producer.ID, producer.Ordinal}
	digest, err := canonicalDigest(input)
	return CandidateID("C-" + digest), err
}

type occurrenceIdentityInput struct {
	SchemaVersion int            `json:"schema_version"`
	Forge         string         `json:"forge"`
	Owner         string         `json:"owner"`
	Repo          string         `json:"repo"`
	PullRequest   int64          `json:"pull_request"`
	HeadSHA       string         `json:"head_sha"`
	TargetSHA     string         `json:"target_sha"`
	Anchor        identityAnchor `json:"anchor"`
	Criterion     Criterion      `json:"criterion"`
	Priority      Priority       `json:"verified_priority"`
}

type identityAnchor struct {
	Path        string       `json:"path"`
	Side        EvidenceSide `json:"side"`
	BlobSHA     string       `json:"blob_sha"`
	QuoteSHA256 string       `json:"quote_sha256"`
}

func NewOccurrenceID(context ManifestContext, finding VerifiedFinding) (OccurrenceID, error) {
	if err := validateContext(context); err != nil {
		return "", err
	}
	if err := ValidateVerifiedFinding(finding); err != nil {
		return "", err
	}
	input := occurrenceIdentityInput{
		SchemaVersion: 1, Forge: context.Forge, Owner: context.Owner, Repo: context.Repo,
		PullRequest: context.PullRequest, HeadSHA: context.HeadSHA, TargetSHA: context.TargetSHA,
		Anchor:    identityAnchor{finding.Anchor.Path, finding.Anchor.Side, finding.Anchor.BlobSHA, finding.Anchor.QuoteSHA256},
		Criterion: finding.Criterion, Priority: finding.VerifiedPriority,
	}
	digest, err := canonicalDigest(input)
	return OccurrenceID("O-" + digest), err
}

type receiptIdentityInput struct {
	SchemaVersion int          `json:"schema_version"`
	Destination   string       `json:"destination"`
	Target        string       `json:"target"`
	OccurrenceID  OccurrenceID `json:"occurrence_id"`
	PayloadSHA256 string       `json:"payload_sha256"`
}

func NewReceiptID(destination, target string, occurrenceID OccurrenceID, payloadSHA256 string) (ReceiptID, error) {
	if destination == "" || target == "" || !occurrenceID.Valid() || !validSHA256(payloadSHA256) {
		return "", fmt.Errorf("invalid receipt identity input")
	}
	digest, err := canonicalDigest(receiptIdentityInput{1, destination, target, occurrenceID, payloadSHA256})
	return ReceiptID("R-" + digest), err
}

func canonicalDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func validSHA256(value string) bool {
	if !sha256Pattern.MatchString(value) || len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
