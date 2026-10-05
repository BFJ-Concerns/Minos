package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
)

var unitSafe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)
var commandCombinedOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type SpawnResult struct {
	Outcome      ReconcileDecision
	BlockingUnit string
	Detail       string
}

const runOwnerMarker = ".runwrap-owner"

// staleReviewResultName preserves a complete predecessor verdict whose head no
// longer matches the admitted one. The lead judges its reuse from the
// workspace's history; an exact carry always supersedes it.
const staleReviewResultName = "stale-review-result.json"

func preserveStaleReviewResult(path, stalePath string) error {
	if _, ok := completeReviewResult(path); ok {
		if err := os.Rename(path, stalePath); err != nil {
			return fmt.Errorf("preserve stale predecessor review result: %w", err)
		}
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("discard untrusted predecessor review result: %w", err)
	}
	return nil
}

// Runs share the configured envelope live through this slice; each run's
// MemoryMax is also the backstop on a box without the slice installed.
const runsSliceName = "minos-runs.slice"

func SpawnRun(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts) (SpawnResult, error) {
	unit := UnitName(facts)
	unlock, err := lockAdmission(cfg.Runs.Dir)
	if err != nil {
		return SpawnResult{}, err
	}
	defer unlock()

	active, err := activeRunUnitNames(ctx)
	if err != nil {
		return SpawnResult{}, err
	}
	// This pull request's own live unit is refused before the continuation
	// handoff below is consumed: systemd-run would reject the duplicate unit
	// name anyway, and by then the handoff would already be gone.
	if own := slices.IndexFunc(active, func(name string) bool {
		return strings.TrimSuffix(name, ".service") == unit
	}); own >= 0 {
		return SpawnResult{Outcome: SpawnSuppressed, BlockingUnit: active[own]}, nil
	}
	if len(active) >= cfg.MaxConcurrentRuns() {
		return SpawnResult{
			Outcome:      SpawnSuppressed,
			BlockingUnit: active[0],
			Detail: fmt.Sprintf("all %d/%d run slots are occupied by active units %s",
				len(active), cfg.MaxConcurrentRuns(), strings.Join(active, ", ")),
		}, nil
	}
	if err := os.MkdirAll(filepath.Join(cfg.Runs.Dir, ".handoffs"), 0o700); err != nil {
		return SpawnResult{}, fmt.Errorf("create continuation handoff directory: %w", err)
	}
	handoffFile := handoffPath(cfg.Runs.Dir, unit)
	var handoff *runHandoff
	handoffRejected := false
	if _, statErr := os.Stat(handoffFile); statErr == nil {
		handoff, err = readRunHandoff(handoffFile, facts)
		if err != nil {
			rejectRunHandoff(handoffFile, err)
			handoff = nil
			handoffRejected = true
		}
	} else if !os.IsNotExist(statErr) {
		return SpawnResult{}, fmt.Errorf("inspect continuation handoff: %w", statErr)
	}
	progressDecision := continuationProgressUnknown
	if handoff != nil {
		progressDecision = compareContinuationProgress(handoff)
		if handoff.Head != facts.HeadSHA {
			progressDecision = continuationProgressAdvanced
		}
	}
	runDir := ""
	adopted := false
	reviewResultPath := ""
	reviewResultAlreadyCarried := false
	if handoff != nil {
		if reason, valid := adoptableRunDirectory(cfg, unit, facts, handoff); valid {
			runDir = handoff.RunDir
			adopted = true
		} else {
			fmt.Fprintf(os.Stderr, "minos: continuation workspace not reused for %s: %s; starting fresh\n", unit, reason)
		}
	}
	if adopted {
		staleResult := filepath.Join(runDir, staleReviewResultName)
		carriedResult := filepath.Join(runDir, "carried-review-result.json")
		if adoptableReviewResult(carriedResult, facts.HeadSHA) {
			reviewResultPath = carriedResult
			reviewResultAlreadyCarried = true
		} else {
			if err := preserveStaleReviewResult(carriedResult, staleResult); err != nil {
				return SpawnResult{}, err
			}
			ordinaryResult := filepath.Join(runDir, "review-result.json")
			if adoptableReviewResult(ordinaryResult, facts.HeadSHA) {
				reviewResultPath = ordinaryResult
			} else if err := preserveStaleReviewResult(ordinaryResult, staleResult); err != nil {
				return SpawnResult{}, err
			}
		}
		if reviewResultPath != "" {
			if err := os.Remove(staleResult); err != nil && !os.IsNotExist(err) {
				return SpawnResult{}, fmt.Errorf("discard superseded stale review result: %w", err)
			}
		}
	} else if handoffRejected {
		reviewResultPath = containedPredecessorReviewResult(cfg, unit, facts.HeadSHA)
	}
	if progressDecision == continuationProgressStalled {
		return stopStalledContinuation(ctx, cfg, repo, unit, facts, handoffFile, handoff)
	}
	if !adopted {
		progressDecision = continuationProgressUnknown
	}
	if adopted {
		for _, stale := range []string{"lead-complete", "memory-pressure"} {
			if err := os.Remove(filepath.Join(runDir, stale)); err != nil && !os.IsNotExist(err) {
				return SpawnResult{}, fmt.Errorf("clear predecessor %s before continuation: %w", stale, err)
			}
		}
	}
	if !adopted {
		runDir, err = os.MkdirTemp(cfg.Runs.Dir, unit+"-")
		if err != nil {
			return SpawnResult{}, err
		}
	}
	reviewResultCarried := false
	cleanupSpawnFailure := func() {
		report := func(action, path string, err error) {
			if err != nil && !os.IsNotExist(err) {
				log.Printf("spawn rollback: %s %s: %v", action, path, err)
			}
		}
		restore := func(destination string) {
			source := filepath.Join(runDir, "carried-review-result.json")
			// A missing carried verdict is also a failed restoration.
			if err := os.Rename(source, destination); err != nil {
				log.Printf("spawn rollback: restore %s to %s: %v", source, destination, err)
			}
		}
		if adopted {
			if reviewResultCarried && !reviewResultAlreadyCarried {
				restore(filepath.Join(runDir, "review-result.json"))
			}
			path := filepath.Join(runDir, runOwnerMarker)
			report("remove ownership marker", path, os.Remove(path))
			return
		}
		if reviewResultCarried {
			restore(reviewResultPath)
		}
		report("remove run directory", runDir, removeRunDir(runDir))
	}
	if reviewResultPath != "" {
		carriedResult := filepath.Join(runDir, "carried-review-result.json")
		if !reviewResultAlreadyCarried {
			if err := os.Rename(reviewResultPath, carriedResult); err != nil {
				cleanupSpawnFailure()
				return SpawnResult{}, fmt.Errorf("mark predecessor review result for one-time carry: %w", err)
			}
			reviewResultCarried = true
		}
		fmt.Fprintf(os.Stderr, "minos: adopted completed review verdict for %s at head %s\n", unit, facts.HeadSHA)
	}
	if err := os.WriteFile(filepath.Join(runDir, runOwnerMarker), nil, 0o600); err != nil {
		cleanupSpawnFailure()
		return SpawnResult{}, fmt.Errorf("create run ownership marker: %w", err)
	}
	if handoff != nil {
		if err := os.Remove(handoffFile); err != nil {
			cleanupSpawnFailure()
			return SpawnResult{}, fmt.Errorf("consume continuation handoff: %w", err)
		}
	}
	forgeConfig := cfg.Forges[facts.Forge]
	env := map[string]string{
		"MINOS_PRESSURE_THRESHOLD_PERCENT": strconv.Itoa(cfg.runPressureThresholdPercent()),
		"MINOS_RUN_DIR":                    runDir,
		"MINOS_CONFIG":                     cfg.Root,
		"MINOS_FORGE":                      facts.Forge,
		"MINOS_WORKSPACE":                  filepath.Join(runDir, "workspace"),
		"MINOS_ORIENTATION":                filepath.Join(runDir, "orientation.json"),
		"MINOS_HANDOFF":                    handoffFile,
		"MINOS_OWNER":                      facts.Owner,
		"MINOS_REPO_NAME":                  facts.Repo,
		"MINOS_PR":                         facts.PR,
		"MINOS_HEAD_SHA":                   facts.HeadSHA,
		"MINOS_TARGET_SHA":                 facts.BaseSHA,
		"MINOS_BASE_REF":                   facts.BaseRef,
		"MINOS_HEAD_BRANCH":                facts.HeadRef,
		"MINOS_API_BASE":                   forgeConfig.APIBase,
		"MINOS_WEB_BASE":                   forgeConfig.WebBase,
		"MINOS_CREDENTIAL_FILE":            forgeConfig.CredentialFile,
		"MINOS_RUN_BODY":                   repo.Adaptation.RunBody,
		"MINOS_REVIEW_THRESHOLD":           repo.Review.Threshold,
		"MINOS_COMMIT_AUTHOR_NAME":         cfg.Service.CommitAuthorName,
		"MINOS_COMMIT_AUTHOR_EMAIL":        cfg.Service.CommitAuthorEmail,
		"MINOS_STATUS_CONTEXT":             cfg.Service.StatusContext,
		"ENSEMBLE_CONCURRENCY_CLAUDE":      strconv.Itoa(cfg.Ensemble.ConcurrencyClaude),
		"ENSEMBLE_CONCURRENCY_CODEX":       strconv.Itoa(cfg.Ensemble.ConcurrencyCodex),
	}
	// Structured knobs cross into the run as one JSON value each, always
	// present: an empty guidance list is exported as [] so the run script
	// reads "configured empty" rather than guessing from an absent variable.
	for key, value := range map[string]any{
		"MINOS_GUIDANCE_SOURCES":   nonNilSources(repo.GuidanceSources),
		"MINOS_FILING_DESTINATION": repo.FilingDestination,
		"MINOS_ROUTING":            cfg.Routing.Roles(),
		"MINOS_MARKERS":            repo.Markers,
	} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			cleanupSpawnFailure()
			return SpawnResult{}, fmt.Errorf("encode %s: %w", key, marshalErr)
		}
		env[key] = string(encoded)
	}
	if cfg.Ensemble.AgentCeiling > 0 {
		env["ENSEMBLE_AGENT_CEILING"] = strconv.Itoa(cfg.Ensemble.AgentCeiling)
	}
	if handoff != nil && handoff.Progress != nil {
		progress, marshalErr := json.Marshal(handoff.Progress)
		if marshalErr != nil {
			cleanupSpawnFailure()
			return SpawnResult{}, fmt.Errorf("encode predecessor continuation progress: %w", marshalErr)
		}
		env["MINOS_PREDECESSOR_PROGRESS"] = string(progress)
	}
	if adopted {
		env["MINOS_RESUME"] = "true"
	}
	exe, err := os.Executable()
	if err != nil {
		cleanupSpawnFailure()
		return SpawnResult{}, err
	}
	args := []string{
		"--user", "--collect", "--unit", unit,
		"--property=ExitType=main",
		"--property=KillMode=control-group",
		"--property=RuntimeMaxSec=" + cfg.runDurationCeiling(),
		"--slice=" + runsSliceName,
		"--property=MemoryMax=" + fmt.Sprintf("%dG", cfg.runMemoryEnvelopeGiB()),
		"--property=OnSuccess=minos-sweep.service",
	}
	for key, value := range env {
		args = append(args, "--setenv", key+"="+value)
	}
	args = append(args, exe, "run", "--config", cfg.Root)
	out, err := commandCombinedOutput(ctx, "systemd-run", args...)
	if err != nil {
		cleanupSpawnFailure()
		if strings.Contains(string(out), "already exists") {
			return SpawnResult{Outcome: SpawnSuppressed, BlockingUnit: unit + ".service"}, nil
		}
		return SpawnResult{}, fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if progressDecision == continuationProgressAdvanced {
		return SpawnResult{Outcome: SpawnContinued}, nil
	}
	return SpawnResult{Outcome: SpawnStarted}, nil
}

func nonNilSources(sources []GuidanceSource) []GuidanceSource {
	if sources == nil {
		return []GuidanceSource{}
	}
	return sources
}

func stopStalledContinuation(ctx context.Context, cfg ServiceConfig, repo RepoConfig, unit string, facts Facts, handoffFile string, handoff *runHandoff) (SpawnResult, error) {
	cause := fmt.Sprintf("successor made no progress beyond stage %q and published no new head or review", handoff.Progress.Stage)
	failureLine := fmt.Sprintf("timestamp=%s pull_request=%s/%s#%s head=%s stage=continuation-progress cause=%s\n",
		time.Now().UTC().Format(time.RFC3339), facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, cause)
	appendStalledContinuationFailure(cfg.Runs.FailureLog, failureLine)

	adapter, err := newBehaviouralForge(cfg, facts.Forge)
	if err != nil {
		return SpawnResult{}, err
	}
	pullRequest, err := strconv.ParseInt(facts.PR, 10, 64)
	if err != nil {
		return SpawnResult{}, err
	}
	guard := forge.Guard{
		Repository:  forge.Repository{Owner: facts.Owner, Name: facts.Repo},
		PullRequest: pullRequest, HeadSHA: facts.HeadSHA, TargetSHA: facts.BaseSHA,
	}
	if result := adapter.SetProductStatus(ctx, guard, product.Attention()); result.Outcome != forge.WriteApplied {
		return SpawnResult{}, fmt.Errorf("set stalled continuation attention: %s: %s", result.Outcome, result.Reason)
	}
	if inFlight := repo.Markers.InFlight; inFlight != nil {
		if result := adapter.RemoveMarker(ctx, guard, *inFlight); result.Outcome != forge.WriteApplied {
			return SpawnResult{}, fmt.Errorf("remove stalled continuation in-flight marker: %s: %s", result.Outcome, result.Reason)
		}
	}
	if runDir, contained := containedRunDirectory(cfg, unit, handoff.RunDir); contained {
		if err := os.WriteFile(filepath.Join(runDir, "lead-complete"), []byte("non-clean\n"), 0o600); err != nil && !os.IsNotExist(err) {
			return SpawnResult{}, fmt.Errorf("mark stalled continuation terminal: %w", err)
		}
	}
	if err := os.Remove(handoffFile); err != nil {
		return SpawnResult{}, fmt.Errorf("consume stalled continuation handoff: %w", err)
	}
	return SpawnResult{Outcome: SpawnAttention, Detail: cause}, nil
}

func appendStalledContinuationFailure(path, line string) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "minos: runs.failure-log (%s) parent is not writable: %v\n", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "minos: runs.failure-log (%s) is not writable: %v\n", path, err)
		return
	}
	if _, err := file.WriteString(line); err != nil {
		fmt.Fprintf(os.Stderr, "minos: runs.failure-log (%s) is not writable: %v\n", path, err)
	}
	if err := file.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "minos: runs.failure-log (%s) could not be closed: %v\n", path, err)
	}
}

func lockAdmission(runsDir string) (func(), error) {
	lock, err := os.OpenFile(filepath.Join(runsDir, ".admission.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open admission lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("lock admission: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}, nil
}

// UnitName carries the minos-run- prefix so the admission check's unit glob
// matches only run units, never the long-lived receiver or sweep services.
func UnitName(facts Facts) string {
	return unitSafe.ReplaceAllString(fmt.Sprintf("minos-run-%s-%s-%s-pr%s", facts.Forge, facts.Owner, facts.Repo, facts.PR), "-")
}

func runBodyPath() string { return filepath.Clean(os.Getenv("MINOS_RUN_BODY")) }
