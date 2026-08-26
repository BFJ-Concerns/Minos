package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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

// runMemoryEnvelopeGiB is the whole box's run budget, not one run's. Runs
// share it live inside runsSliceName rather than by static division: the
// slice unit (deploy/systemd/user/minos-runs.slice) holds MemoryHigh just
// under the envelope and MemoryMax at it, so no run feels any pressure until
// the runs *together* approach the envelope, reclaim then pushes them back,
// and only combined demand the envelope cannot hold kills — the kernel picks
// the biggest consumer, which is the ballooning run's compiler or worker.
// Each run also carries its own MemoryMax at the whole envelope as the
// backstop for a box where the slice unit is not installed, keeping the
// receiver and sweep (outside the slice) safe either way.
const runMemoryEnvelopeGiB = 22

const runsSliceName = "minos-runs.slice"

func runMemoryMax() string {
	return fmt.Sprintf("%dG", runMemoryEnvelopeGiB)
}

type RunClass string

const (
	RunClassReview      RunClass = "review"
	RunClassMaintenance RunClass = "maintenance"
)

func SpawnRun(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, admission AdmissionContext, runClass RunClass) (SpawnResult, error) {
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
		return stopStalledContinuation(ctx, cfg, unit, facts, handoffFile, handoff)
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
		if adopted {
			if reviewResultCarried && !reviewResultAlreadyCarried {
				_ = os.Rename(filepath.Join(runDir, "carried-review-result.json"), filepath.Join(runDir, "review-result.json"))
			}
			_ = os.Remove(filepath.Join(runDir, runOwnerMarker))
			return
		}
		if reviewResultCarried {
			_ = os.Rename(filepath.Join(runDir, "carried-review-result.json"), reviewResultPath)
		}
		_ = removeRunDir(runDir)
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
		if err := os.WriteFile(filepath.Join(runDir, "loop-record.json"), handoff.RunRecord, 0o600); err != nil {
			cleanupSpawnFailure()
			return SpawnResult{}, fmt.Errorf("seed continuation loop record: %w", err)
		}
		if err := os.Remove(handoffFile); err != nil {
			cleanupSpawnFailure()
			return SpawnResult{}, fmt.Errorf("consume continuation handoff: %w", err)
		}
	}
	forgeConfig := cfg.Forges[facts.Forge]
	maximumRounds := ""
	if repo.Review.MaximumRounds > 0 {
		maximumRounds = strconv.Itoa(repo.Review.MaximumRounds)
	}
	env := map[string]string{
		"MINOS_RUN_DIR":                 runDir,
		"MINOS_CONFIG":                  cfg.Root,
		"MINOS_FORGE":                   facts.Forge,
		"MINOS_WORKSPACE":               filepath.Join(runDir, "workspace"),
		"MINOS_ORIENTATION":             filepath.Join(runDir, "orientation.json"),
		"MINOS_HANDOFF":                 handoffFile,
		"MINOS_LOOP_RECORD":             filepath.Join(runDir, "loop-record.json"),
		"MINOS_OWNER":                   facts.Owner,
		"MINOS_REPO_NAME":               facts.Repo,
		"MINOS_PR":                      facts.PR,
		"MINOS_HEAD_SHA":                facts.HeadSHA,
		"MINOS_TARGET_SHA":              facts.BaseSHA,
		"MINOS_BASE_REF":                facts.BaseRef,
		"MINOS_HEAD_BRANCH":             facts.HeadRef,
		"MINOS_API_BASE":                forgeConfig.APIBase,
		"MINOS_CREDENTIAL_FILE":         forgeConfig.CredentialFile,
		"MINOS_BUILD_CMD":               repo.Adaptation.Build,
		"MINOS_TEST_CMD":                repo.Adaptation.Test,
		"MINOS_RUN_BODY":                repo.Adaptation.RunBody,
		"MINOS_AUTO_MERGE":              fmt.Sprintf("%t", repo.Policy.AutoMerge),
		"MINOS_REVIEW_THRESHOLD":        repo.Review.Threshold,
		"MINOS_MAX_ROUNDS":              maximumRounds,
		"MINOS_RUN_CLASS":               string(runClass),
		"MINOS_RELEASED_HOLD_HEAD":      admission.ReleasedHoldHead,
		"MINOS_RELEASED_HOLD_STAGE":     admission.ReleasedHoldStage,
		"MINOS_RELEASED_HOLD_DIAGNOSIS": admission.ReleasedHoldDiagnosis,
		"ENSEMBLE_CONCURRENCY_CLAUDE":   strconv.Itoa(cfg.Ensemble.ConcurrencyClaude),
		"ENSEMBLE_CONCURRENCY_CODEX":    strconv.Itoa(cfg.Ensemble.ConcurrencyCodex),
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
		"--property=RuntimeMaxSec=12h",
		"--slice=" + runsSliceName,
		"--property=MemoryMax=" + runMemoryMax(),
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

func stopStalledContinuation(ctx context.Context, cfg ServiceConfig, unit string, facts Facts, handoffFile string, handoff *runHandoff) (SpawnResult, error) {
	cause := fmt.Sprintf("successor made no progress beyond stage %q round %d and published no new head or review", handoff.Progress.Stage, handoff.Progress.Round)
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
	if result := adapter.RemoveReaction(ctx, guard, "eyes"); result.Outcome != forge.WriteApplied {
		return SpawnResult{}, fmt.Errorf("remove stalled continuation reaction: %s: %s", result.Outcome, result.Reason)
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
	return unitSafe.ReplaceAllString(fmt.Sprintf("minos-run-%s-%s-pr%s", facts.Owner, facts.Repo, facts.PR), "-")
}

func runBodyPath() string { return filepath.Clean(os.Getenv("MINOS_RUN_BODY")) }
