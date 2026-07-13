package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"bfj/minos/internal/atomicreplace"
	"bfj/minos/internal/incidents"
	"bfj/minos/internal/ledger"
)

const (
	retryableExitMarkerName   = "retryable-exit.json"
	retryableExitMarkerSchema = 1
	retryableExitDiagnostic   = "lead declared a retryable operational failure"
	retryableExitDisposition  = "retry after operational backoff"
)

var ErrRetryableExit = errors.New("lifecycle declared a retryable operational failure")

type retryableExitRecord struct {
	Schema   int    `json:"schema"`
	Category string `json:"category"`
}

func declareRetryableExit(ctx context.Context, adaptation Adaptation, facts Facts, store *ledger.Store, token int64, category string) error {
	owned, err := store.Owns(ctx, coordinationKey(facts), token)
	if err != nil {
		return err
	}
	if !owned {
		return ledger.ErrNotOwner
	}
	if err := writeRetryableExitRecord(os.Getenv("MINOS_RUN_DIR"), category); err != nil {
		return err
	}
	// The declaration is durable before presence is removed. If presence closure
	// fails, the wrapper still sees an operational failure and retries the common
	// close without ever presenting the attempt as successful.
	return releaseRun(ctx, adaptation, facts, store, token)
}

func writeRetryableExitRecord(runDir, category string) error {
	category = strings.TrimSpace(category)
	if !validRetryableExitCategory(category) {
		return fmt.Errorf("unsupported retryable-exit category %q", category)
	}
	if strings.TrimSpace(runDir) == "" {
		return errors.New("MINOS_RUN_DIR is required")
	}
	data, err := json.Marshal(retryableExitRecord{Schema: retryableExitMarkerSchema, Category: category})
	if err != nil {
		return err
	}
	return atomicreplace.Write(filepath.Join(runDir, retryableExitMarkerName), append(data, '\n'), 0o644)
}

func readRetryableExitRecord(runDir string) (retryableExitRecord, bool, error) {
	data, err := os.ReadFile(filepath.Join(runDir, retryableExitMarkerName))
	if errors.Is(err, os.ErrNotExist) {
		return retryableExitRecord{}, false, nil
	}
	if err != nil {
		return retryableExitRecord{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record retryableExitRecord
	if err := decoder.Decode(&record); err != nil {
		return retryableExitRecord{}, false, fmt.Errorf("decode retryable-exit record: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return retryableExitRecord{}, false, errors.New("decode retryable-exit record: trailing content")
	}
	if record.Schema != retryableExitMarkerSchema {
		return retryableExitRecord{}, false, fmt.Errorf("unsupported retryable-exit schema %d", record.Schema)
	}
	if !validRetryableExitCategory(record.Category) {
		return retryableExitRecord{}, false, fmt.Errorf("unsupported retryable-exit category %q", record.Category)
	}
	return record, true, nil
}

func validRetryableExitCategory(category string) bool {
	switch category {
	case "host-capacity", "engine-unavailable", "stale-oauth", "forge-unavailable", "network-unavailable":
		return true
	default:
		return false
	}
}

func recordRetryableExit(ctx context.Context, cfg ServiceConfig, facts Facts, store *ledger.Store, token int64, record retryableExitRecord) error {
	key := coordinationKey(facts)
	lease, leaseErr := ownedLease(ctx, store, key, token)
	if leaseErr != nil {
		return errors.Join(ErrRetryableExit, leaseErr)
	}
	backoff, backoffErr := store.RecordFailure(ctx, key, token)
	disposition := retryableExitDisposition
	if backoffErr == nil {
		disposition = fmt.Sprintf("retry after %s", backoff.NextDueAt.UTC().Format("2006-01-02T15:04:05Z07:00"))
	}
	logPath := filepath.Join(os.Getenv("MINOS_RUN_DIR"), "run.log")
	incidentErr := recordOperationalIncident(ctx, cfg, store, ledger.Incident{
		Key: key, Category: record.Category, ObservedHead: lease.ObservedHead,
		ObservedTarget: lease.ObservedTarget, LogLocation: logPath,
	}, incidents.Event{
		Key: incidents.Key{
			Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo,
			PullRequest: facts.PR, Category: record.Category,
		},
		Diagnostic: retryableExitDiagnostic, LogPath: logPath, Attempt: int(token),
		ObservedHead: lease.ObservedHead, ObservedTarget: lease.ObservedTarget,
		RetryDisposition: disposition,
	})
	return errors.Join(ErrRetryableExit, backoffErr, incidentErr)
}
