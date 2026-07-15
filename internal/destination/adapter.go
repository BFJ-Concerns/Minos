package destination

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"bfj/minos/internal/findings"
)

type Config struct {
	Name              string
	Target            string
	Adaptation        string
	Endpoint          string
	CredentialFile    string
	ExpectedPrincipal string
}

type Adapter struct {
	config Config
	run    func(context.Context, Operation, any, any) error
}

func NewAdapter(config Config) (*Adapter, error) {
	for name, value := range map[string]string{
		"name": config.Name, "target": config.Target, "adaptation": config.Adaptation,
		"endpoint": config.Endpoint, "credential file": config.CredentialFile,
		"expected principal": config.ExpectedPrincipal,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("finding destination %s is required", name)
		}
	}
	adapter := &Adapter{config: config}
	adapter.run = adapter.runOperation
	return adapter, nil
}

func (adapter *Adapter) Deliver(ctx context.Context, record DurableRecord, beforeCreate func(context.Context) error) (DeliveryProof, error) {
	if err := record.Validate(); err != nil {
		return DeliveryProof{}, destinationError(ErrorIntegrity, OperationCreate, record.Receipt.OccurrenceID, "invalid-record", err.Error())
	}
	match, found, err := adapter.discover(ctx, record)
	if err != nil {
		return DeliveryProof{}, err
	}
	if found {
		return adapter.readAndCompare(ctx, match, record)
	}
	if beforeCreate != nil {
		if err := beforeCreate(ctx); err != nil {
			return DeliveryProof{}, err
		}
	}
	var created CreateResult
	request := CreateRequest{SchemaVersion: 1, Destination: adapter.config.Name, Target: adapter.config.Target, Record: record}
	if err := adapter.run(ctx, OperationCreate, request, &created); err != nil {
		return DeliveryProof{}, err
	}
	if err := validateCreateResult(created, record.Receipt.OccurrenceID); err != nil {
		return DeliveryProof{}, err
	}
	switch created.Outcome {
	case OutcomeRejected:
		return DeliveryProof{}, destinationError(ErrorRejected, OperationCreate, record.Receipt.OccurrenceID, codeOr(created.ReasonCode, "create-rejected"), "destination rejected the finding record")
	case OutcomeRetryable:
		return DeliveryProof{}, destinationError(ErrorRetryable, OperationCreate, record.Receipt.OccurrenceID, codeOr(created.ReasonCode, "create-retryable"), "destination asked for a later retry")
	}
	match, found, err = adapter.discover(ctx, record)
	if err != nil {
		return DeliveryProof{}, err
	}
	if !found {
		return DeliveryProof{}, destinationError(ErrorUncertain, OperationCreate, record.Receipt.OccurrenceID, "create-not-readable", "created finding record could not be discovered and read back")
	}
	return adapter.readAndCompare(ctx, match, record)
}

func (adapter *Adapter) discover(ctx context.Context, record DurableRecord) (DiscoverMatch, bool, error) {
	request := DiscoverRequest{SchemaVersion: 1, Destination: adapter.config.Name, Target: adapter.config.Target, OccurrenceID: record.Receipt.OccurrenceID}
	var result DiscoverResult
	if err := adapter.run(ctx, OperationDiscover, request, &result); err != nil {
		return DiscoverMatch{}, false, err
	}
	if err := validateDiscoverResult(result, record.Receipt.OccurrenceID); err != nil {
		return DiscoverMatch{}, false, err
	}
	switch result.Outcome {
	case OutcomeFound:
		match := result.Matches[0]
		if match.OccurrenceID != record.Receipt.OccurrenceID || match.PayloadSHA256 != record.Receipt.PayloadSHA256 {
			return DiscoverMatch{}, false, destinationError(ErrorIntegrity, OperationDiscover, record.Receipt.OccurrenceID, "discovery-mismatch", "discovered record does not match occurrence and payload")
		}
		return match, true, nil
	case OutcomeNotFound:
		return DiscoverMatch{}, false, nil
	case OutcomeRetryable:
		return DiscoverMatch{}, false, destinationError(ErrorRetryable, OperationDiscover, record.Receipt.OccurrenceID, codeOr(result.ReasonCode, "discover-retryable"), "destination asked for a later discovery retry")
	case OutcomeUncertain:
		return DiscoverMatch{}, false, destinationError(ErrorUncertain, OperationDiscover, record.Receipt.OccurrenceID, codeOr(result.ReasonCode, "discover-uncertain"), "destination could not establish whether the finding exists")
	default:
		return DiscoverMatch{}, false, destinationError(ErrorIntegrity, OperationDiscover, record.Receipt.OccurrenceID, "discover-outcome", "destination returned an invalid discovery outcome")
	}
}

func (adapter *Adapter) readAndCompare(ctx context.Context, match DiscoverMatch, expected DurableRecord) (DeliveryProof, error) {
	request := ReadRequest{SchemaVersion: 1, Destination: adapter.config.Name, Target: adapter.config.Target, ProviderRecordID: match.ProviderRecordID, OccurrenceID: expected.Receipt.OccurrenceID}
	var result ReadResult
	if err := adapter.run(ctx, OperationRead, request, &result); err != nil {
		return DeliveryProof{}, err
	}
	if err := validateReadResult(result, expected.Receipt.OccurrenceID); err != nil {
		return DeliveryProof{}, err
	}
	switch result.Outcome {
	case OutcomeNotFound:
		return DeliveryProof{}, destinationError(ErrorUncertain, OperationRead, expected.Receipt.OccurrenceID, codeOr(result.ReasonCode, "read-not-found"), "discovered finding disappeared before authenticated read-back")
	case OutcomeRetryable:
		return DeliveryProof{}, destinationError(ErrorRetryable, OperationRead, expected.Receipt.OccurrenceID, codeOr(result.ReasonCode, "read-retryable"), "destination asked for a later read retry")
	case OutcomeUncertain:
		return DeliveryProof{}, destinationError(ErrorUncertain, OperationRead, expected.Receipt.OccurrenceID, codeOr(result.ReasonCode, "read-uncertain"), "destination could not authenticate the finding read")
	case OutcomeFound:
		if result.AuthenticatedPrincipal != adapter.config.ExpectedPrincipal {
			return DeliveryProof{}, destinationError(ErrorIntegrity, OperationRead, expected.Receipt.OccurrenceID, "principal-mismatch", "authenticated read-back principal does not match deployment policy")
		}
		if result.Record == nil || !reflect.DeepEqual(*result.Record, expected) {
			return DeliveryProof{}, destinationError(ErrorIntegrity, OperationRead, expected.Receipt.OccurrenceID, "record-mismatch", "authenticated read-back does not match the complete finding record")
		}
		observedAt, err := time.Parse(time.RFC3339, result.ObservedAt)
		_, offset := observedAt.Zone()
		if err != nil || offset != 0 {
			return DeliveryProof{}, destinationError(ErrorIntegrity, OperationRead, expected.Receipt.OccurrenceID, "observed-at-invalid", "authenticated read-back lacks an RFC 3339 UTC observation time")
		}
		return DeliveryProof{ProviderRecordID: match.ProviderRecordID, AuthenticatedPrincipal: result.AuthenticatedPrincipal, ObservedAt: observedAt, Receipt: expected.Receipt}, nil
	default:
		return DeliveryProof{}, destinationError(ErrorIntegrity, OperationRead, expected.Receipt.OccurrenceID, "read-outcome", "destination returned an invalid read outcome")
	}
}

func (adapter *Adapter) runOperation(ctx context.Context, operation Operation, request, result any) error {
	executable := filepath.Join(adapter.config.Adaptation, string(operation))
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return destinationError(ErrorConfiguration, operation, occurrenceFromRequest(request), "adaptation-unavailable", "destination adaptation operation is not an executable file")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return destinationError(ErrorIntegrity, operation, occurrenceFromRequest(request), "request-encoding", err.Error())
	}
	command := exec.CommandContext(ctx, executable)
	command.Dir = adapter.config.Adaptation
	command.Env = []string{
		"MINOS_FINDING_DESTINATION=" + adapter.config.Name,
		"MINOS_FINDING_TARGET=" + adapter.config.Target,
		"MINOS_FINDING_ENDPOINT=" + adapter.config.Endpoint,
		"MINOS_FINDING_CREDENTIAL_FILE=" + adapter.config.CredentialFile,
	}
	command.Stdin = bytes.NewReader(append(payload, '\n'))
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		kind := ErrorRetryable
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			kind = ErrorUncertain
		}
		return destinationError(kind, operation, occurrenceFromRequest(request), "adaptation-exit", sanitiseMessage(stderr.String()))
	}
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return destinationError(ErrorIntegrity, operation, occurrenceFromRequest(request), "malformed-output", sanitiseMessage(err.Error()))
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return destinationError(ErrorIntegrity, operation, occurrenceFromRequest(request), "trailing-output", "destination adaptation returned trailing JSON")
	}
	return nil
}

func validateDiscoverResult(result DiscoverResult, occurrenceID findings.OccurrenceID) error {
	if result.SchemaVersion != 1 {
		return destinationError(ErrorIntegrity, OperationDiscover, occurrenceID, "schema-version", "destination returned an unsupported discovery schema")
	}
	switch result.Outcome {
	case OutcomeFound:
		if len(result.Matches) != 1 || result.ReasonCode != "" || strings.TrimSpace(result.Matches[0].ProviderRecordID) == "" {
			return destinationError(ErrorIntegrity, OperationDiscover, occurrenceID, "discovery-cardinality", "found discovery must return exactly one complete match")
		}
	case OutcomeNotFound:
		if len(result.Matches) != 0 || result.ReasonCode != "" {
			return destinationError(ErrorIntegrity, OperationDiscover, occurrenceID, "not-found-fields", "not-found discovery returned forbidden fields")
		}
	case OutcomeRetryable, OutcomeUncertain:
		if len(result.Matches) != 0 || strings.TrimSpace(result.ReasonCode) == "" {
			return destinationError(ErrorIntegrity, OperationDiscover, occurrenceID, "discovery-error-fields", "discovery error outcome has invalid fields")
		}
	default:
		return destinationError(ErrorIntegrity, OperationDiscover, occurrenceID, "discovery-outcome", "destination returned an invalid discovery outcome")
	}
	return nil
}

func validateReadResult(result ReadResult, occurrenceID findings.OccurrenceID) error {
	if result.SchemaVersion != 1 {
		return destinationError(ErrorIntegrity, OperationRead, occurrenceID, "schema-version", "destination returned an unsupported read schema")
	}
	if result.Outcome == OutcomeFound {
		if result.Record == nil || result.AuthenticatedPrincipal == "" || result.ObservedAt == "" || result.ReasonCode != "" {
			return destinationError(ErrorIntegrity, OperationRead, occurrenceID, "read-found-fields", "found read lacks authenticated full-record evidence")
		}
		if err := result.Record.Validate(); err != nil {
			return destinationError(ErrorIntegrity, OperationRead, occurrenceID, "record-invalid", err.Error())
		}
		return nil
	}
	if result.Record != nil || result.AuthenticatedPrincipal != "" || result.ObservedAt != "" {
		return destinationError(ErrorIntegrity, OperationRead, occurrenceID, "read-forbidden-fields", "non-found read returned record evidence")
	}
	switch result.Outcome {
	case OutcomeNotFound:
		if result.ReasonCode != "" {
			return destinationError(ErrorIntegrity, OperationRead, occurrenceID, "read-not-found-fields", "not-found read returned a reason code")
		}
	case OutcomeRetryable, OutcomeUncertain:
		if result.ReasonCode == "" {
			return destinationError(ErrorIntegrity, OperationRead, occurrenceID, "read-error-fields", "read error outcome lacks a reason code")
		}
	default:
		return destinationError(ErrorIntegrity, OperationRead, occurrenceID, "read-outcome", "destination returned an invalid read outcome")
	}
	return nil
}

func validateCreateResult(result CreateResult, occurrenceID findings.OccurrenceID) error {
	if result.SchemaVersion != 1 {
		return destinationError(ErrorIntegrity, OperationCreate, occurrenceID, "schema-version", "destination returned an unsupported create schema")
	}
	switch result.Outcome {
	case OutcomeCreated, OutcomeAlreadyExists:
		if result.ProviderRecordID == "" || result.ReasonCode != "" {
			return destinationError(ErrorIntegrity, OperationCreate, occurrenceID, "create-success-fields", "create success lacks provider identity or has forbidden fields")
		}
	case OutcomeRejected, OutcomeRetryable, OutcomeUncertain:
		if result.ProviderRecordID != "" || result.ReasonCode == "" {
			return destinationError(ErrorIntegrity, OperationCreate, occurrenceID, "create-error-fields", "create error outcome has invalid fields")
		}
	default:
		return destinationError(ErrorIntegrity, OperationCreate, occurrenceID, "create-outcome", "destination returned an invalid create outcome")
	}
	return nil
}

func occurrenceFromRequest(request any) findings.OccurrenceID {
	switch value := request.(type) {
	case DiscoverRequest:
		return value.OccurrenceID
	case ReadRequest:
		return value.OccurrenceID
	case CreateRequest:
		return value.Record.Receipt.OccurrenceID
	default:
		return ""
	}
}

func destinationError(kind ErrorKind, operation Operation, occurrenceID findings.OccurrenceID, code, message string) *DestinationError {
	return &DestinationError{SchemaVersion: 1, Kind: kind, Operation: operation, OccurrenceID: occurrenceID, Code: code, Message: sanitiseMessage(message)}
}

func codeOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
