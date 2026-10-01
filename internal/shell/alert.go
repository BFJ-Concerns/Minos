package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"bfj/minos/internal/forge"
)

// A repo skipped this many consecutive sweep passes earns an operator alert:
// at the ten-minute sweep cadence this is roughly an hour of quiet failure.
const repoSkipAlertThreshold = 6

// operatorAlert files an alert issue on the configured alert repository, or
// logs when alerting is not configured. Overridable for tests.
var operatorAlert = sendOperatorAlert

func sendOperatorAlert(ctx context.Context, cfg ServiceConfig, title, body string) {
	service := cfg.Service
	if service.AlertForge == "" || service.AlertOwner == "" || service.AlertRepo == "" {
		log.Printf("attention: %s (no alert repository configured): %s", title, body)
		return
	}
	adapter, err := newBehaviouralForge(cfg, service.AlertForge)
	if err != nil {
		log.Printf("attention: %s (alert forge unavailable: %v): %s", title, err, body)
		return
	}
	result := adapter.Alert(ctx, forge.Repository{Owner: service.AlertOwner, Name: service.AlertRepo}, title, body)
	if result.Outcome != forge.WriteApplied {
		log.Printf("attention: %s (alert not confirmed: %s): %s", title, result.Reason, body)
	}
}

type repoSkipRecord struct {
	Count     int    `json:"count"`
	Since     string `json:"since"`
	LastError string `json:"last_error"`
}

// writeServiceStateAtomically writes complete service-local state to a
// temporary file before replacing its destination with the supplied rename.
func writeServiceStateAtomically(temporary *os.File, temporaryPath, destination string, content []byte, rename func(string, string) error) error {
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write service state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close service state: %w", err)
	}
	if err := rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("rename service state: %w", err)
	}
	return nil
}

func sweepSkipStatePath(cfg ServiceConfig) string {
	return filepath.Join(cfg.Runs.Dir, ".sweep-skips.json")
}

// recordRepoSkips advances the per-repo consecutive-skip counters: skipped
// repos increment, and every other repo resets by absence from the persisted
// state. A repo crossing the alert threshold this pass files one operator
// alert per outage; recovery resets the counter, so a fresh outage alerts
// again.
func recordRepoSkips(ctx context.Context, cfg ServiceConfig, skipped map[string]string) {
	state := make(map[string]repoSkipRecord)
	if content, err := os.ReadFile(sweepSkipStatePath(cfg)); err == nil {
		if err := json.Unmarshal(content, &state); err != nil {
			state = make(map[string]repoSkipRecord)
		}
	}
	next := make(map[string]repoSkipRecord, len(skipped))
	for slug, reason := range skipped {
		record := state[slug]
		record.Count++
		record.LastError = reason
		if record.Since == "" {
			record.Since = time.Now().UTC().Format(time.RFC3339)
		}
		next[slug] = record
		if record.Count == repoSkipAlertThreshold {
			operatorAlert(ctx, cfg,
				"Sweep attention: a configured repo is not being swept",
				fmt.Sprintf(
					"`%s` has been skipped for %d consecutive sweep passes (since %s).\n\n"+
						"Latest error:\n```\n%s\n```\n\n"+
						"Its pull requests are not being reconciled. This alert fires once per outage;"+
						" the sweep keeps skipping the repo and logging until it becomes readable again.",
					slug, record.Count, record.Since, reason,
				),
			)
		}
	}
	content, err := json.Marshal(next)
	if err != nil {
		return
	}
	if err := os.MkdirAll(cfg.Runs.Dir, 0o755); err != nil {
		log.Printf("sweep skip state: %v", err)
		return
	}
	temp := sweepSkipStatePath(cfg) + ".tmp"
	temporary, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		log.Printf("sweep skip state: %v", err)
		return
	}
	if err := writeServiceStateAtomically(temporary, temp, sweepSkipStatePath(cfg), content, os.Rename); err != nil {
		log.Printf("sweep skip state: %v", err)
	}
}

// AlertCommand lets systemd failure hooks file the same operator alert the
// sweep uses, so a sweep too broken to run its own reporting still surfaces.
func AlertCommand(ctx context.Context, args []string) error {
	if len(args) < 3 || args[0] != "--config" {
		return fmt.Errorf("usage: minos alert --config ROOT TITLE BODY...")
	}
	cfg, err := LoadServiceConfig(args[1])
	if err != nil {
		return err
	}
	title := args[2]
	body := "(no detail supplied)"
	if len(args) > 3 {
		body = args[3]
	}
	sendOperatorAlert(ctx, cfg, title, body)
	return nil
}
