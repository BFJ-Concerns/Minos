package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/product"
)

var unitSafe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)
var commandCombinedOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type SpawnOutcome string

const (
	SpawnStarted    SpawnOutcome = "started"
	SpawnContinued  SpawnOutcome = "continued"
	SpawnSuppressed SpawnOutcome = "suppressed"
	SpawnAttention  SpawnOutcome = "attention"
	runOwnerMarker               = ".runwrap-owner"
)

func SpawnRun(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts) (SpawnOutcome, error) {
	unit := UnitName(facts)
	unlock, err := lockAdmission(cfg.Runs.Dir)
	if err != nil {
		return "", err
	}
	defer unlock()

	out, err := commandCombinedOutput(ctx, "systemctl", "--user", "list-units",
		"--type=service", "--state=activating,active", "--no-legend", "--plain", "--full", "--no-pager", "minos-run-*.service")
	if err != nil {
		return "", fmt.Errorf("inspect active Minos units: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) != "" {
		return SpawnSuppressed, nil
	}
	if err := os.MkdirAll(filepath.Join(cfg.Runs.Dir, ".handoffs"), 0o700); err != nil {
		return "", fmt.Errorf("create continuation handoff directory: %w", err)
	}
	handoffFile := handoffPath(cfg.Runs.Dir, unit)
	var handoff *runHandoff
	if _, statErr := os.Stat(handoffFile); statErr == nil {
		handoff, err = readRunHandoff(handoffFile, facts)
		if err != nil {
			rejectRunHandoff(handoffFile, err)
			handoff = nil
		}
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("inspect continuation handoff: %w", statErr)
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
	if handoff != nil {
		if reason, valid := adoptableRunDirectory(cfg, unit, facts, handoff); valid {
			runDir = handoff.RunDir
			adopted = true
		} else {
			fmt.Fprintf(os.Stderr, "minos: continuation workspace not reused for %s: %s; starting fresh\n", unit, reason)
		}
	}
	if progressDecision == continuationProgressStalled {
		return stopStalledContinuation(ctx, cfg, unit, facts, handoffFile, handoff)
	}
	if !adopted {
		progressDecision = continuationProgressUnknown
	}
	if adopted {
		for _, stale := range []string{"lead-complete", "memory-pressure"} {
			if err := os.Remove(filepath.Join(runDir, stale)); err != nil && !os.IsNotExist(err) {
				return "", fmt.Errorf("clear predecessor %s before continuation: %w", stale, err)
			}
		}
	}
	if !adopted {
		runDir, err = os.MkdirTemp(cfg.Runs.Dir, unit+"-")
		if err != nil {
			return "", err
		}
	}
	cleanupSpawnFailure := func() {
		if adopted {
			_ = os.Remove(filepath.Join(runDir, runOwnerMarker))
			return
		}
		_ = removeRunDir(runDir)
	}
	if err := os.WriteFile(filepath.Join(runDir, runOwnerMarker), nil, 0o600); err != nil {
		cleanupSpawnFailure()
		return "", fmt.Errorf("create run ownership marker: %w", err)
	}
	if handoff != nil {
		if err := os.WriteFile(filepath.Join(runDir, "loop-record.json"), handoff.RunRecord, 0o600); err != nil {
			cleanupSpawnFailure()
			return "", fmt.Errorf("seed continuation loop record: %w", err)
		}
		if err := os.Remove(handoffFile); err != nil {
			cleanupSpawnFailure()
			return "", fmt.Errorf("consume continuation handoff: %w", err)
		}
	}
	forgeConfig := cfg.Forges[facts.Forge]
	maximumRounds := ""
	if repo.Review.MaximumRounds > 0 {
		maximumRounds = strconv.Itoa(repo.Review.MaximumRounds)
	}
	env := map[string]string{
		"MINOS_RUN_DIR":               runDir,
		"MINOS_CONFIG":                cfg.Root,
		"MINOS_FORGE":                 facts.Forge,
		"MINOS_WORKSPACE":             filepath.Join(runDir, "workspace"),
		"MINOS_ORIENTATION":           filepath.Join(runDir, "orientation.json"),
		"MINOS_HANDOFF":               handoffFile,
		"MINOS_LOOP_RECORD":           filepath.Join(runDir, "loop-record.json"),
		"MINOS_OWNER":                 facts.Owner,
		"MINOS_REPO_NAME":             facts.Repo,
		"MINOS_PR":                    facts.PR,
		"MINOS_HEAD_SHA":              facts.HeadSHA,
		"MINOS_TARGET_SHA":            facts.BaseSHA,
		"MINOS_BASE_REF":              facts.BaseRef,
		"MINOS_HEAD_BRANCH":           facts.HeadRef,
		"MINOS_API_BASE":              forgeConfig.APIBase,
		"MINOS_CREDENTIAL_FILE":       forgeConfig.CredentialFile,
		"MINOS_BUILD_CMD":             repo.Adaptation.Build,
		"MINOS_TEST_CMD":              repo.Adaptation.Test,
		"MINOS_RUN_BODY":              repo.Adaptation.RunBody,
		"MINOS_AUTO_MERGE":            fmt.Sprintf("%t", repo.Policy.AutoMerge),
		"MINOS_REVIEW_THRESHOLD":      repo.Review.Threshold,
		"MINOS_MAX_ROUNDS":            maximumRounds,
		"ENSEMBLE_CONCURRENCY_CLAUDE": strconv.Itoa(cfg.Ensemble.ConcurrencyClaude),
		"ENSEMBLE_CONCURRENCY_CODEX":  strconv.Itoa(cfg.Ensemble.ConcurrencyCodex),
	}
	if handoff != nil && handoff.Progress != nil {
		progress, marshalErr := json.Marshal(handoff.Progress)
		if marshalErr != nil {
			cleanupSpawnFailure()
			return "", fmt.Errorf("encode predecessor continuation progress: %w", marshalErr)
		}
		env["MINOS_PREDECESSOR_PROGRESS"] = string(progress)
	}
	if adopted {
		env["MINOS_RESUME"] = "true"
	}
	exe, err := os.Executable()
	if err != nil {
		cleanupSpawnFailure()
		return "", err
	}
	args := []string{
		"--user", "--collect", "--unit", unit,
		"--property=ExitType=main",
		"--property=KillMode=control-group",
		"--property=RuntimeMaxSec=12h",
		"--property=MemoryMax=14G",
	}
	for key, value := range env {
		args = append(args, "--setenv", key+"="+value)
	}
	args = append(args, exe, "run", "--config", cfg.Root)
	out, err = commandCombinedOutput(ctx, "systemd-run", args...)
	if err != nil {
		cleanupSpawnFailure()
		if strings.Contains(string(out), "already exists") {
			return SpawnSuppressed, nil
		}
		return "", fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if progressDecision == continuationProgressAdvanced {
		return SpawnContinued, nil
	}
	return SpawnStarted, nil
}

func stopStalledContinuation(ctx context.Context, cfg ServiceConfig, unit string, facts Facts, handoffFile string, handoff *runHandoff) (SpawnOutcome, error) {
	cause := fmt.Sprintf("successor made no progress beyond stage %q round %d and published no new head or review", handoff.Progress.Stage, handoff.Progress.Round)
	failureLine := fmt.Sprintf("timestamp=%s pull_request=%s/%s#%s head=%s stage=continuation-progress cause=%s\n",
		time.Now().UTC().Format(time.RFC3339), facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, cause)
	appendStalledContinuationFailure(cfg.Runs.FailureLog, failureLine)

	adapter, err := newBehaviouralForge(cfg, facts.Forge)
	if err != nil {
		return "", err
	}
	pullRequest, err := strconv.ParseInt(facts.PR, 10, 64)
	if err != nil {
		return "", err
	}
	guard := forge.Guard{
		Repository:  forge.Repository{Owner: facts.Owner, Name: facts.Repo},
		PullRequest: pullRequest, HeadSHA: facts.HeadSHA, TargetSHA: facts.BaseSHA,
	}
	if result := adapter.SetProductStatus(ctx, guard, product.Attention()); result.Outcome != forge.WriteApplied {
		return "", fmt.Errorf("set stalled continuation attention: %s: %s", result.Outcome, result.Reason)
	}
	if result := adapter.RemoveReaction(ctx, guard, "eyes"); result.Outcome != forge.WriteApplied {
		return "", fmt.Errorf("remove stalled continuation reaction: %s: %s", result.Outcome, result.Reason)
	}
	if runDir, contained := containedRunDirectory(cfg, unit, handoff.RunDir); contained {
		if err := os.WriteFile(filepath.Join(runDir, "lead-complete"), []byte("non-clean\n"), 0o600); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("mark stalled continuation terminal: %w", err)
		}
	}
	if err := os.Remove(handoffFile); err != nil {
		return "", fmt.Errorf("consume stalled continuation handoff: %w", err)
	}
	return SpawnAttention, nil
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
	return unitSafe.ReplaceAllString(fmt.Sprintf("minos-run-%s-%s-pr%s", facts.Owner, facts.Repo, facts.PR), "-")
}

func runBodyPath() string { return filepath.Clean(os.Getenv("MINOS_RUN_BODY")) }
