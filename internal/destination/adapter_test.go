package destination

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bfj/minos/internal/findings"
)

func TestDeliverRediscoversExistingRecordWithoutCreate(t *testing.T) {
	record := destinationRecordFixture(t)
	adapter := destinationAdapterFixture(t)
	var operations []Operation
	adapter.run = func(_ context.Context, operation Operation, _ any, result any) error {
		operations = append(operations, operation)
		switch operation {
		case OperationDiscover:
			assignResult(result, DiscoverResult{SchemaVersion: 1, Outcome: OutcomeFound, Matches: []DiscoverMatch{{ProviderRecordID: "provider-1", OccurrenceID: record.Receipt.OccurrenceID, PayloadSHA256: record.Receipt.PayloadSHA256}}})
		case OperationRead:
			assignResult(result, ReadResult{SchemaVersion: 1, Outcome: OutcomeFound, Record: &record, AuthenticatedPrincipal: "minos-service", ObservedAt: "2026-07-15T12:00:00Z"})
		default:
			t.Fatalf("unexpected operation %s", operation)
		}
		return nil
	}
	proof, err := adapter.Deliver(t.Context(), record, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(operations, []Operation{OperationDiscover, OperationRead}) || proof.Receipt != record.Receipt || proof.AuthenticatedPrincipal != "minos-service" {
		t.Fatalf("operations=%v proof=%#v", operations, proof)
	}
}

func TestDeliverCreatesOnceThenRequiresAuthenticatedReadBack(t *testing.T) {
	record := destinationRecordFixture(t)
	adapter := destinationAdapterFixture(t)
	var operations []Operation
	discoveries := 0
	adapter.run = func(_ context.Context, operation Operation, _ any, result any) error {
		operations = append(operations, operation)
		switch operation {
		case OperationDiscover:
			discoveries++
			if discoveries == 1 {
				assignResult(result, DiscoverResult{SchemaVersion: 1, Outcome: OutcomeNotFound})
			} else {
				assignResult(result, DiscoverResult{SchemaVersion: 1, Outcome: OutcomeFound, Matches: []DiscoverMatch{{ProviderRecordID: "provider-1", OccurrenceID: record.Receipt.OccurrenceID, PayloadSHA256: record.Receipt.PayloadSHA256}}})
			}
		case OperationCreate:
			assignResult(result, CreateResult{SchemaVersion: 1, Outcome: OutcomeUncertain, ReasonCode: "connection-lost"})
		case OperationRead:
			assignResult(result, ReadResult{SchemaVersion: 1, Outcome: OutcomeFound, Record: &record, AuthenticatedPrincipal: "minos-service", ObservedAt: "2026-07-15T12:00:00+00:00"})
		}
		return nil
	}
	if _, err := adapter.Deliver(t.Context(), record, nil); err != nil {
		t.Fatal(err)
	}
	want := []Operation{OperationDiscover, OperationCreate, OperationDiscover, OperationRead}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("operations=%v want %v", operations, want)
	}
}

func TestDeliverRechecksAuthorityImmediatelyBeforeCreate(t *testing.T) {
	record := destinationRecordFixture(t)
	adapter := destinationAdapterFixture(t)
	var operations []Operation
	adapter.run = func(_ context.Context, operation Operation, _ any, result any) error {
		operations = append(operations, operation)
		if operation != OperationDiscover {
			t.Fatalf("unexpected operation %s", operation)
		}
		assignResult(result, DiscoverResult{SchemaVersion: 1, Outcome: OutcomeNotFound})
		return nil
	}
	guardErr := errors.New("lease changed")
	_, err := adapter.Deliver(t.Context(), record, func(context.Context) error { return guardErr })
	if !errors.Is(err, guardErr) {
		t.Fatalf("error=%v want %v", err, guardErr)
	}
	if !reflect.DeepEqual(operations, []Operation{OperationDiscover}) {
		t.Fatalf("operations=%v; create must not run after a failed authority check", operations)
	}
}

func TestUncertainDiscoveryNeverCreatesBlindly(t *testing.T) {
	record := destinationRecordFixture(t)
	adapter := destinationAdapterFixture(t)
	var operations []Operation
	adapter.run = func(_ context.Context, operation Operation, _ any, result any) error {
		operations = append(operations, operation)
		assignResult(result, DiscoverResult{SchemaVersion: 1, Outcome: OutcomeUncertain, ReasonCode: "provider-uncertain"})
		return nil
	}
	_, err := adapter.Deliver(t.Context(), record, nil)
	assertDestinationError(t, err, ErrorUncertain, OperationDiscover)
	if !reflect.DeepEqual(operations, []Operation{OperationDiscover}) {
		t.Fatalf("operations=%v", operations)
	}
}

func TestDeliveryRejectsDuplicateAndMismatchingAuthenticatedRecords(t *testing.T) {
	record := destinationRecordFixture(t)
	for _, test := range []struct {
		name string
		run  func(context.Context, Operation, any, any) error
	}{
		{
			name: "duplicates",
			run: func(_ context.Context, _ Operation, _ any, result any) error {
				match := DiscoverMatch{ProviderRecordID: "provider-1", OccurrenceID: record.Receipt.OccurrenceID, PayloadSHA256: record.Receipt.PayloadSHA256}
				assignResult(result, DiscoverResult{SchemaVersion: 1, Outcome: OutcomeFound, Matches: []DiscoverMatch{match, match}})
				return nil
			},
		},
		{
			name: "wrong principal",
			run: func(_ context.Context, operation Operation, _ any, result any) error {
				if operation == OperationDiscover {
					assignResult(result, DiscoverResult{SchemaVersion: 1, Outcome: OutcomeFound, Matches: []DiscoverMatch{{ProviderRecordID: "provider-1", OccurrenceID: record.Receipt.OccurrenceID, PayloadSHA256: record.Receipt.PayloadSHA256}}})
				} else {
					assignResult(result, ReadResult{SchemaVersion: 1, Outcome: OutcomeFound, Record: &record, AuthenticatedPrincipal: "somebody-else", ObservedAt: "2026-07-15T12:00:00Z"})
				}
				return nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := destinationAdapterFixture(t)
			adapter.run = test.run
			_, err := adapter.Deliver(t.Context(), record, nil)
			assertDestinationError(t, err, ErrorIntegrity, "")
		})
	}
}

func TestOperationRunnerScrubsEnvironmentAndStaysOutsideWorkspace(t *testing.T) {
	record := destinationRecordFixture(t)
	adaptation := t.TempDir()
	capture := filepath.Join(t.TempDir(), "capture")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$PWD|${AMBIENT_SECRET-unset}|$MINOS_FINDING_ENDPOINT|$MINOS_FINDING_CREDENTIAL_FILE\" > " + capture + "\n" +
		"printf '%s\\n' '{\"schema_version\":1,\"outcome\":\"uncertain\",\"reason_code\":\"probe\"}'\n"
	if err := os.WriteFile(filepath.Join(adaptation, "discover"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AMBIENT_SECRET", "must-not-leak")
	t.Setenv("MINOS_WORKSPACE", filepath.Join(t.TempDir(), "untrusted-workspace"))
	adapter, err := NewAdapter(Config{Name: "backlog", Target: "owner/repo", Adaptation: adaptation, Endpoint: "fixture://backlog", CredentialFile: "/run/secret", ExpectedPrincipal: "minos-service"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Deliver(t.Context(), record, nil)
	assertDestinationError(t, err, ErrorUncertain, OperationDiscover)
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	want := adaptation + "|unset|fixture://backlog|/run/secret\n"
	if string(data) != want || strings.Contains(string(data), os.Getenv("MINOS_WORKSPACE")) {
		t.Fatalf("captured environment=%q want %q", data, want)
	}
}

func TestOperationRunnerRejectsMalformedAndTrailingOutput(t *testing.T) {
	record := destinationRecordFixture(t)
	for _, output := range []string{"not-json\n", "{\"schema_version\":1,\"outcome\":\"not-found\"}\n{}\n", "{\"schema_version\":1,\"outcome\":\"not-found\",\"unknown\":true}\n"} {
		adaptation := t.TempDir()
		script := "#!/bin/sh\nprintf '%s' " + shellQuote(output) + "\n"
		if err := os.WriteFile(filepath.Join(adaptation, "discover"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		adapter, err := NewAdapter(Config{Name: "backlog", Target: "owner/repo", Adaptation: adaptation, Endpoint: "fixture://backlog", CredentialFile: "/run/secret", ExpectedPrincipal: "minos-service"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = adapter.Deliver(t.Context(), record, nil)
		assertDestinationError(t, err, ErrorIntegrity, OperationDiscover)
	}
}

func destinationAdapterFixture(t *testing.T) *Adapter {
	t.Helper()
	adapter, err := NewAdapter(Config{Name: "backlog", Target: "owner/repo", Adaptation: t.TempDir(), Endpoint: "fixture://backlog", CredentialFile: "/run/secret", ExpectedPrincipal: "minos-service"})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func destinationRecordFixture(t *testing.T) DurableRecord {
	t.Helper()
	context := findings.ManifestContext{Forge: "forgejo", Owner: "owner", Repo: "repo", PullRequest: 7, HeadSHA: "head-1", TargetSHA: "target-1", AttemptToken: 17, GoverningIdentity: "governing-1"}
	producer := findings.Producer{Family: "codex", ID: "review-1", Ordinal: 0}
	candidateID, err := findings.NewCandidateID(context.AttemptToken, producer)
	if err != nil {
		t.Fatal(err)
	}
	category := findings.DefectCorrectness
	anchor := findings.NewEvidenceAnchor("internal/example.go", findings.HeadSide, 5, "blob-1", "return wrong")
	candidate := findings.CandidateFinding{SchemaVersion: 1, CandidateID: candidateID, Anchor: anchor, Criterion: findings.Criterion{Defect: &category}, ProposedPriority: findings.P2, Assurance: findings.AgentJudgement, Title: "Wrong result", Message: "Returns the wrong result.", Producer: producer}
	verifier := findings.Producer{Family: "claude", ID: "verify-1", Ordinal: 0}
	verified := findings.VerifiedFinding{SchemaVersion: 1, CandidateID: candidateID, LineageID: "F-ABCD", Anchor: anchor, Criterion: candidate.Criterion, ProposedPriority: findings.P2, VerifierPriority: findings.P2, VerifiedPriority: findings.P2, PriorityValidation: findings.PriorityValidation{VerifierFamily: verifier.Family, VerifierID: verifier.ID, Agreement: findings.PriorityAgreed, Rationale: "Impact confirmed."}, Assurance: findings.AgentJudgement, Title: candidate.Title, Message: candidate.Message, Producer: producer, Verifier: verifier}
	verified.OccurrenceID, err = findings.NewOccurrenceID(context, verified)
	if err != nil {
		t.Fatal(err)
	}
	candidateDisposition := findings.CandidateDisposition{Candidate: candidate, Outcome: findings.CandidateVerified, OccurrenceID: verified.OccurrenceID, VerificationEvidence: findings.VerificationEvidence{Verifier: verifier, Rationale: "Confirmed independently."}}
	manifest, err := findings.NewDispositionManifest(context, findings.ResolvedPolicy{PublishThreshold: findings.P1, RepairThreshold: findings.P3, Mode: findings.DestinationMode, Destination: "backlog", Target: "owner/repo"}, []findings.CandidateDisposition{candidateDisposition}, []findings.VerifiedFinding{verified})
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewDurableRecord("backlog", "owner/repo", manifest.Findings[0])
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func assignResult(target, value any) {
	data, _ := json.Marshal(value)
	_ = json.Unmarshal(data, target)
}

func assertDestinationError(t *testing.T, err error, kind ErrorKind, operation Operation) {
	t.Helper()
	var destinationErr *DestinationError
	if !errors.As(err, &destinationErr) || destinationErr.Kind != kind || (operation != "" && destinationErr.Operation != operation) {
		t.Fatalf("error=%#v want kind=%s operation=%s", err, kind, operation)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
