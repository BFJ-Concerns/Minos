package destination

import (
	"fmt"
	"strings"
	"time"

	"bfj/minos/internal/findings"
)

type Outcome string

const (
	OutcomeCreated       Outcome = "created"
	OutcomeAlreadyExists Outcome = "already-exists"
	OutcomeRejected      Outcome = "rejected"
	OutcomeRetryable     Outcome = "retryable"
	OutcomeUncertain     Outcome = "uncertain"
	OutcomeFound         Outcome = "found"
	OutcomeNotFound      Outcome = "not-found"
)

type DiscoverRequest struct {
	SchemaVersion int                   `json:"schema_version"`
	Destination   string                `json:"destination"`
	Target        string                `json:"target"`
	OccurrenceID  findings.OccurrenceID `json:"occurrence_id"`
}

type DiscoverMatch struct {
	ProviderRecordID string                `json:"provider_record_id"`
	OccurrenceID     findings.OccurrenceID `json:"occurrence_id"`
	PayloadSHA256    string                `json:"payload_sha256"`
}

type DiscoverResult struct {
	SchemaVersion int             `json:"schema_version"`
	Outcome       Outcome         `json:"outcome"`
	Matches       []DiscoverMatch `json:"matches,omitempty"`
	ReasonCode    string          `json:"reason_code,omitempty"`
}

type ReadRequest struct {
	SchemaVersion    int                   `json:"schema_version"`
	Destination      string                `json:"destination"`
	Target           string                `json:"target"`
	ProviderRecordID string                `json:"provider_record_id"`
	OccurrenceID     findings.OccurrenceID `json:"occurrence_id"`
}

type ReadResult struct {
	SchemaVersion          int            `json:"schema_version"`
	Outcome                Outcome        `json:"outcome"`
	Record                 *DurableRecord `json:"record,omitempty"`
	AuthenticatedPrincipal string         `json:"authenticated_principal,omitempty"`
	ObservedAt             string         `json:"observed_at,omitempty"`
	ReasonCode             string         `json:"reason_code,omitempty"`
}

type CreateRequest struct {
	SchemaVersion int           `json:"schema_version"`
	Destination   string        `json:"destination"`
	Target        string        `json:"target"`
	Record        DurableRecord `json:"record"`
}

type CreateResult struct {
	SchemaVersion    int     `json:"schema_version"`
	Outcome          Outcome `json:"outcome"`
	ProviderRecordID string  `json:"provider_record_id,omitempty"`
	ReasonCode       string  `json:"reason_code,omitempty"`
}

type DurablePayload struct {
	SchemaVersion  int                             `json:"schema_version"`
	Finding        findings.VerifiedFinding        `json:"finding"`
	Material       bool                            `json:"material"`
	RepairEligible bool                            `json:"repair_eligible"`
	Publication    findings.PublicationDisposition `json:"publication"`
	Repair         findings.RepairDisposition      `json:"repair"`
	Delivery       findings.DeliveryState          `json:"delivery"`
}

type DurableRecord struct {
	SchemaVersion int                      `json:"schema_version"`
	Payload       DurablePayload           `json:"payload"`
	Receipt       findings.DeliveryReceipt `json:"receipt"`
}

func NewDurableRecord(destinationName, target string, disposition findings.FindingDisposition) (DurableRecord, error) {
	if disposition.Material || disposition.Delivery != findings.DeliveryPending || disposition.Receipt != nil {
		return DurableRecord{}, fmt.Errorf("durable delivery requires a quiet pending finding")
	}
	payload := DurablePayload{
		SchemaVersion: 1, Finding: disposition.Finding, Material: disposition.Material,
		RepairEligible: disposition.RepairEligible, Publication: disposition.Publication,
		Repair: disposition.Repair, Delivery: findings.DeliveryConfirmed,
	}
	digest, err := findings.CanonicalSHA256(payload)
	if err != nil {
		return DurableRecord{}, err
	}
	receiptID, err := findings.NewReceiptID(destinationName, target, disposition.Finding.OccurrenceID, digest)
	if err != nil {
		return DurableRecord{}, err
	}
	record := DurableRecord{SchemaVersion: 1, Payload: payload, Receipt: findings.DeliveryReceipt{
		SchemaVersion: 1, ReceiptID: receiptID, Destination: destinationName, Target: target,
		OccurrenceID: disposition.Finding.OccurrenceID, PayloadSHA256: digest,
	}}
	if err := record.Validate(); err != nil {
		return DurableRecord{}, err
	}
	return record, nil
}

func (record DurableRecord) Validate() error {
	if record.SchemaVersion != 1 || record.Payload.SchemaVersion != 1 || record.Payload.Material || record.Payload.Delivery != findings.DeliveryConfirmed {
		return fmt.Errorf("invalid durable finding record")
	}
	if err := findings.ValidateVerifiedFinding(record.Payload.Finding); err != nil {
		return err
	}
	digest, err := findings.CanonicalSHA256(record.Payload)
	if err != nil {
		return err
	}
	if record.Receipt.OccurrenceID != record.Payload.Finding.OccurrenceID || record.Receipt.PayloadSHA256 != digest {
		return fmt.Errorf("durable record receipt does not bind its complete payload")
	}
	expected, err := findings.NewReceiptID(record.Receipt.Destination, record.Receipt.Target, record.Receipt.OccurrenceID, digest)
	if err != nil || record.Receipt.ReceiptID != expected {
		return fmt.Errorf("durable record receipt identity mismatch")
	}
	return nil
}

type DeliveryProof struct {
	ProviderRecordID       string
	AuthenticatedPrincipal string
	ObservedAt             time.Time
	Receipt                findings.DeliveryReceipt
}

type ErrorKind string

const (
	ErrorConfiguration ErrorKind = "configuration"
	ErrorRejected      ErrorKind = "rejected"
	ErrorRetryable     ErrorKind = "retryable"
	ErrorUncertain     ErrorKind = "uncertain"
	ErrorIntegrity     ErrorKind = "integrity"
)

type Operation string

const (
	OperationDiscover Operation = "discover"
	OperationRead     Operation = "read"
	OperationCreate   Operation = "create"
)

type DestinationError struct {
	SchemaVersion int                   `json:"schema_version"`
	Kind          ErrorKind             `json:"kind"`
	Operation     Operation             `json:"operation"`
	OccurrenceID  findings.OccurrenceID `json:"occurrence_id,omitempty"`
	Code          string                `json:"code"`
	Message       string                `json:"message"`
}

func (err *DestinationError) Error() string {
	return fmt.Sprintf("finding destination %s %s: %s", err.Operation, err.Kind, err.Message)
}

func sanitiseMessage(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "destination adaptation did not complete the operation"
	}
	if len(value) > 240 {
		value = value[:240]
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 {
			return ' '
		}
		return r
	}, value)
}
