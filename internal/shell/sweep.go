package shell

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxRetryableRunWrapRetries = 1

func SweepCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	configRoot := fs.String("config", DefaultConfigRoot, "configuration root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := LoadServiceConfig(*configRoot)
	if err != nil {
		return err
	}
	repos, err := LoadRepoConfigs(cfg.Root)
	if err != nil {
		return err
	}
	sweepLog, closeLog, err := openSweepLog(cfg)
	if err != nil {
		return err
	}
	defer closeLog()
	for _, repo := range repos {
		forge, ok := cfg.Forges[repo.Forge]
		if !ok {
			fmt.Fprintf(sweepLog, "repo %s: unknown forge %s\n", repo.Path, repo.Forge)
			continue
		}
		adaptation, err := NewAdaptation(forge)
		if err != nil {
			return err
		}
		prs, err := adaptation.ListOpenPRs(ctx, repo.Forge, repo.Owner, repo.Repo)
		if err != nil {
			return err
		}
		for _, facts := range prs {
			if err := sweepPR(ctx, cfg, repo, adaptation, facts, sweepLog); err != nil {
				fmt.Fprintf(sweepLog, "sweep %s#%s: %v\n", facts.RepoSlug(), facts.PR, err)
			}
		}
	}
	return nil
}

func sweepPR(ctx context.Context, cfg ServiceConfig, repo RepoConfig, adaptation Adaptation, facts Facts, logw *os.File) error {
	statuses, err := adaptation.GetStatuses(ctx, facts.Owner, facts.Repo, facts.HeadSHA)
	if err != nil {
		return err
	}
	for _, kind := range runKinds {
		label, _ := InFlightLabel(kind)
		if !facts.HasLabel(label) {
			continue
		}
		runDir, alive, err := liveRunState(cfg.Runs.Dir, facts, kind, cfg.Sweep.LivenessThreshold.Duration)
		if err != nil {
			return err
		}
		if runDir == "" {
			fmt.Fprintf(logw, "clear orphan %s on %s#%s\n", label, facts.RepoSlug(), facts.PR)
			if err := adaptation.RemoveLabel(ctx, facts.Owner, facts.Repo, facts.PR, label); err != nil {
				return err
			}
			continue
		}
		if alive {
			fmt.Fprintf(logw, "alive %s for %s#%s at %s\n", kind, facts.RepoSlug(), facts.PR, runDir)
			return nil
		}
		var handledRetry, releasedRetry bool
		statuses, handledRetry, releasedRetry, err = handleRetryableClaim(ctx, adaptation, facts, kind, runDir, statuses, logw)
		if err != nil {
			return err
		}
		if handledRetry {
			if releasedRetry {
				if err := adaptation.RemoveLabel(ctx, facts.Owner, facts.Repo, facts.PR, label); err != nil {
					return err
				}
				facts.Labels = removeFactLabel(facts.Labels, label)
			}
			continue
		}
		if err := reapRunDir(ctx, runDir); err != nil {
			fmt.Fprintf(logw, "reap failed closed for %s: %v\n", runDir, err)
			return nil
		}
		fmt.Fprintf(logw, "reaped %s; clear %s on %s#%s\n", runDir, label, facts.RepoSlug(), facts.PR)
		if err := adaptation.RemoveLabel(ctx, facts.Owner, facts.Repo, facts.PR, label); err != nil {
			return err
		}
	}
	statuses, err = reapLabelLessClaims(ctx, cfg, adaptation, facts, statuses, logw)
	if err != nil {
		return err
	}
	facts, err = clearUnauthorisedReady(ctx, repo, adaptation, facts, logw)
	if err != nil {
		return err
	}
	decision, ok, err := reconcileDecision(ctx, repo, adaptation, facts, statuses)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	fmt.Fprintf(logw, "reconcile fires %s for %s#%s %s\n", decision, facts.RepoSlug(), facts.PR, facts.HeadSHA)
	return SpawnRun(ctx, cfg, repo, facts, decision, "reconcile")
}

func openSweepLog(cfg ServiceConfig) (*os.File, func(), error) {
	if cfg.Sweep.Log == "" {
		return os.Stdout, func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Sweep.Log), 0o755); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(cfg.Sweep.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return file, func() { _ = file.Close() }, nil
}

func liveRunState(root string, facts Facts, kind RunKind, threshold time.Duration) (string, bool, error) {
	runDir, err := newestLiveRunDir(root, facts, kind)
	if err != nil || runDir == "" {
		return runDir, false, err
	}
	_, alive, err := runState(runDir, threshold)
	return runDir, alive, err
}

func runState(runDir string, threshold time.Duration) (string, bool, error) {
	statPath := filepath.Join(runDir, "run.log")
	stat, err := os.Stat(statPath)
	if errors.Is(err, os.ErrNotExist) {
		stat, err = os.Stat(runDir)
	}
	if err != nil {
		return "", false, err
	}
	return runDir, time.Since(stat.ModTime()) < threshold, nil
}

func newestLiveRunDir(root string, facts Facts, kind RunKind) (string, error) {
	dir := filepath.Join(root, facts.Forge+"--"+facts.Owner+"--"+facts.Repo, "pr"+facts.PR)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	pattern := regexp.MustCompile(`^[0-9A-Za-z]{1,12}-` + regexp.QuoteMeta(string(kind)) + `$`)
	var newest runClaim
	for _, entry := range entries {
		if !entry.IsDir() || isReapedRunDir(entry.Name()) || isRetryEvidenceDir(entry.Name()) || !pattern.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		claim, err := readRunClaim(path)
		if err != nil {
			return "", err
		}
		if claim.metaErr != nil {
			continue
		}
		if newest.path == "" || runClaimAfter(claim, newest) {
			newest = claim
		}
	}
	return newest.path, nil
}

type runClaim struct {
	path        string
	kind        RunKind
	sha         string
	headSHA     string
	startedAt   time.Time
	metaMissing bool
	metaErr     error
}

func labelLessClaims(root string, facts Facts) ([]runClaim, error) {
	dir := filepath.Join(root, facts.Forge+"--"+facts.Owner+"--"+facts.Repo, "pr"+facts.PR)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pattern := regexp.MustCompile(`^([0-9A-Za-z]{1,12})-(review|fix|finish)$`)
	var claims []runClaim
	for _, entry := range entries {
		if !entry.IsDir() || isReapedRunDir(entry.Name()) || isRetryEvidenceDir(entry.Name()) {
			continue
		}
		matches := pattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		claim, err := readRunClaim(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		claims = append(claims, claim)
	}
	return claims, nil
}

func readRunClaim(path string) (runClaim, error) {
	name := filepath.Base(path)
	matches := regexp.MustCompile(`^([0-9A-Za-z]{1,12})-(review|fix|finish)$`).FindStringSubmatch(name)
	if matches == nil {
		return runClaim{}, fmt.Errorf("invalid run claim name: %s", name)
	}
	kind, err := ParseRunKind(matches[2])
	if err != nil {
		return runClaim{}, err
	}
	meta, metaErr := readMetaFile(filepath.Join(path, "meta.env"))
	claim := runClaim{
		path:        path,
		kind:        kind,
		sha:         matches[1],
		headSHA:     meta["PUMP19_HEAD_SHA"],
		metaMissing: errors.Is(metaErr, os.ErrNotExist),
	}
	if metaErr != nil && !claim.metaMissing {
		claim.metaErr = metaErr
		return claim, nil
	}
	if claim.headSHA == "" {
		claim.headSHA = claim.sha
	}
	if started := meta["PUMP19_STARTED_AT"]; started != "" {
		parsed, err := time.Parse(time.RFC3339Nano, started)
		if err != nil {
			claim.metaErr = fmt.Errorf("parse PUMP19_STARTED_AT: %w", err)
			return claim, nil
		}
		claim.startedAt = parsed
	}
	return claim, nil
}

func runClaimAfter(left, right runClaim) bool {
	if left.metaMissing != right.metaMissing {
		return left.metaMissing
	}
	if !left.startedAt.Equal(right.startedAt) {
		return left.startedAt.After(right.startedAt)
	}
	return left.path > right.path
}

func reapLabelLessClaims(ctx context.Context, cfg ServiceConfig, adaptation Adaptation, facts Facts, currentStatuses []Status, logw *os.File) ([]Status, error) {
	claims, err := labelLessClaims(cfg.Runs.Dir, facts)
	if err != nil {
		return currentStatuses, err
	}
	for _, claim := range claims {
		if claim.metaErr != nil {
			fmt.Fprintf(logw, "skip claim with unreadable meta %s: %v\n", claim.path, claim.metaErr)
			continue
		}
		label, _ := InFlightLabel(claim.kind)
		if facts.HasLabel(label) {
			continue
		}
		contextName, _ := StatusContext(claim.kind)
		currentHead := claim.headSHA == facts.HeadSHA || strings.HasPrefix(facts.HeadSHA, claim.sha)
		if currentHead {
			if _, ok := statusForContext(currentStatuses, contextName); ok {
				continue
			}
		}
		_, alive, err := runState(claim.path, cfg.Sweep.LivenessThreshold.Duration)
		if err != nil {
			return currentStatuses, err
		}
		if alive {
			continue
		}
		if currentHead && hasRetryableFailure(claim.path) {
			var handledRetry bool
			currentStatuses, handledRetry, _, err = handleRetryableClaim(ctx, adaptation, facts, claim.kind, claim.path, currentStatuses, logw)
			if err != nil {
				return currentStatuses, err
			}
			if handledRetry {
				continue
			}
		}
		if err := reapRunDir(ctx, claim.path); err != nil {
			fmt.Fprintf(logw, "label-less reap failed closed for %s: %v\n", claim.path, err)
			continue
		}
		fmt.Fprintf(logw, "reaped label-less %s for %s#%s\n", claim.path, facts.RepoSlug(), facts.PR)
	}
	return currentStatuses, nil
}

func handleRetryableClaim(ctx context.Context, adaptation Adaptation, facts Facts, kind RunKind, runDir string, currentStatuses []Status, logw *os.File) ([]Status, bool, bool, error) {
	if !hasRetryableFailure(runDir) {
		return currentStatuses, false, false, nil
	}
	contextName, _ := StatusContext(kind)
	if _, ok := statusForContext(currentStatuses, contextName); ok {
		return currentStatuses, false, false, nil
	}
	retryCount, err := retryEvidenceCount(runDir)
	if err != nil {
		return currentStatuses, false, false, err
	}
	if retryCount < maxRetryableRunWrapRetries {
		if err := releaseRetryableClaim(ctx, runDir, retryCount+1); err != nil {
			fmt.Fprintf(logw, "retry release failed closed for %s: %v\n", runDir, err)
			return currentStatuses, true, false, nil
		}
		fmt.Fprintf(logw, "released retryable %s attempt %d for %s#%s\n", runDir, retryCount+1, facts.RepoSlug(), facts.PR)
		return currentStatuses, true, true, nil
	}
	if err := adaptation.SetStatus(ctx, facts.Owner, facts.Repo, facts.HeadSHA, contextName, "error", "Pump-19 wrapper failure retry exhausted"); err != nil {
		return currentStatuses, false, false, err
	}
	currentStatuses = append(currentStatuses, Status{Context: contextName, State: "error", CreatedUnix: time.Now().Unix()})
	if err := discardRunDir(ctx, runDir); err != nil {
		fmt.Fprintf(logw, "retry exhaustion cleanup failed for %s: %v\n", runDir, err)
		return currentStatuses, true, false, nil
	}
	fmt.Fprintf(logw, "retry exhausted for %s; wrote %s error on %s#%s\n", runDir, contextName, facts.RepoSlug(), facts.PR)
	return currentStatuses, true, true, nil
}

func reapRunDir(ctx context.Context, runDir string) error {
	if err := cleanupRunDirResources(ctx, runDir); err != nil {
		return err
	}
	target := fmt.Sprintf("%s.reaped-%d", runDir, reapedSuffix())
	// os.Rename can clobber an empty directory on Unix. The nanosecond suffix
	// makes a natural collision vanishingly unlikely; this explicit Lstat turns
	// a forced collision into a hard fail so evidence is not replaced.
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("reap target already exists: %s", target)
	}
	return os.Rename(runDir, target)
}

func releaseRetryableClaim(ctx context.Context, runDir string, attempt int) error {
	if err := cleanupRunDirResources(ctx, runDir); err != nil {
		return err
	}
	target := fmt.Sprintf("%s.retry-%d", runDir, attempt)
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("retry evidence already exists: %s", target)
	}
	return os.Rename(runDir, target)
}

func discardRunDir(ctx context.Context, runDir string) error {
	if err := cleanupRunDirResources(ctx, runDir); err != nil {
		return err
	}
	return os.RemoveAll(runDir)
}

func cleanupRunDirResources(ctx context.Context, runDir string) error {
	meta := readMeta(filepath.Join(runDir, "meta.env"))
	unit := meta["PUMP19_UNIT"]
	if unit != "" {
		if err := stopAndVerifyUnitGone(ctx, unit); err != nil {
			return err
		}
	}
	if workspace := meta["PUMP19_WORKSPACE"]; workspace != "" {
		if err := os.RemoveAll(workspace); err != nil {
			return err
		}
	}
	return nil
}

func isRetryEvidenceDir(name string) bool {
	return strings.Contains(name, ".retry-")
}

func hasRetryableFailure(runDir string) bool {
	values, err := readMetaFile(filepath.Join(runDir, "retry.env"))
	return err == nil && values["PUMP19_RETRYABLE_FAILURE"] == "1"
}

func retryEvidenceCount(runDir string) (int, error) {
	matches, err := filepath.Glob(runDir + ".retry-*")
	if err != nil {
		return 0, err
	}
	return len(matches), nil
}

func clearUnauthorisedReady(ctx context.Context, repo RepoConfig, adaptation Adaptation, facts Facts, logw *os.File) (Facts, error) {
	if !facts.HasLabel(LabelReady) || !hasRunTrigger(repo, RunFinish) {
		return facts, nil
	}
	actor, err := adaptation.LabelActor(ctx, facts.Owner, facts.Repo, facts.PR, LabelReady)
	if err != nil {
		return facts, err
	}
	if guardsPass(repo, RunFinish, facts, actor) {
		return facts, nil
	}
	if err := adaptation.RemoveLabel(ctx, facts.Owner, facts.Repo, facts.PR, LabelReady); err != nil {
		return facts, err
	}
	fmt.Fprintf(logw, "clear unauthorised %s on %s#%s actor=%s\n", LabelReady, facts.RepoSlug(), facts.PR, actor)
	facts.Labels = removeFactLabel(facts.Labels, LabelReady)
	return facts, nil
}

func hasRunTrigger(repo RepoConfig, kind RunKind) bool {
	for _, rule := range repo.Triggers {
		ruleKind, err := ParseRunKind(rule.Run)
		if err == nil && ruleKind == kind {
			return true
		}
	}
	return false
}

func removeFactLabel(labels []string, label string) []string {
	filtered := labels[:0]
	for _, existing := range labels {
		if existing != label {
			filtered = append(filtered, existing)
		}
	}
	return filtered
}

var reapedSuffix = func() int64 {
	return time.Now().UnixNano()
}

func stopAndVerifyUnitGone(ctx context.Context, unit string) error {
	stop := systemctlCommand(ctx, "systemctl", "--user", "stop", unit)
	if out, err := stop.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(out))
		if strings.Contains(message, "not loaded") {
			return nil
		}
		return fmt.Errorf("stop unit %s: %w: %s", unit, err, message)
	}
	show := systemctlCommand(ctx, "systemctl", "--user", "show", unit, "--property=ActiveState", "--value")
	out, err := show.CombinedOutput()
	if err != nil {
		// A collected transient unit may already have disappeared; that is gone.
		return nil
	}
	state := strings.TrimSpace(string(out))
	if state != "" && state != "inactive" && state != "failed" {
		return fmt.Errorf("unit %s still active: %s", unit, state)
	}
	return nil
}

var systemctlCommand = exec.CommandContext

func readMeta(path string) map[string]string {
	values, err := readMetaFile(path)
	if err != nil {
		return map[string]string{}
	}
	return values
}

func readMetaFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values, err := parseKeyValues(file)
	if err != nil {
		return nil, err
	}
	return values, nil
}

func reconcileDecision(ctx context.Context, repo RepoConfig, adaptation Adaptation, facts Facts, statuses []Status) (RunKind, bool, error) {
	reviewContext, _ := StatusContext(RunReview)
	fixContext, _ := StatusContext(RunFix)
	finishContext, _ := StatusContext(RunFinish)
	if _, ok := statusForContext(statuses, reviewContext); !ok && reviewReconcileGuardsPass(repo, facts) {
		return RunReview, true, nil
	}
	reviewStatus, reviewOK := statusForContext(statuses, reviewContext)
	if reviewOK && reviewStatus.State == "failure" {
		if _, fixOK := statusForContext(statuses, fixContext); !fixOK && !facts.HasLabel(LabelStandingFindings) && guardsPass(repo, RunFix, facts, reviewStatus.Creator) {
			return RunFix, true, nil
		}
	}
	if facts.HasLabel(LabelReady) {
		if _, finishOK := statusForContext(statuses, finishContext); !finishOK {
			actor, err := adaptation.LabelActor(ctx, facts.Owner, facts.Repo, facts.PR, LabelReady)
			if err != nil {
				return "", false, err
			}
			if guardsPass(repo, RunFinish, facts, actor) {
				return RunFinish, true, nil
			}
		}
	}
	return "", false, nil
}

func guardsPass(repo RepoConfig, kind RunKind, facts Facts, actor string) bool {
	return guardsPassWithActorMode(repo, kind, facts, actor, true)
}

func reviewReconcileGuardsPass(repo RepoConfig, facts Facts) bool {
	return guardsPassWithActorMode(repo, RunReview, facts, "", false)
}

func guardsPassWithActorMode(repo RepoConfig, kind RunKind, facts Facts, actor string, checkActors bool) bool {
	for _, rule := range repo.Triggers {
		ruleKind, err := ParseRunKind(rule.Run)
		if err != nil || ruleKind != kind {
			continue
		}
		if rule.Drafts != nil && facts.Draft != *rule.Drafts {
			continue
		}
		if len(rule.Authors) > 0 && !matchesGlobAny(rule.Authors, facts.Author) {
			continue
		}
		if checkActors && len(rule.Actors) > 0 && !matchesGlobAny(rule.Actors, actor) {
			continue
		}
		return true
	}
	return false
}

func init() {
	log.SetFlags(log.LstdFlags | log.LUTC)
}
