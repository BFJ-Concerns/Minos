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

var errRetryClaimAlreadyReleased = errors.New("retry claim already released")
var runClaimNamePattern = regexp.MustCompile(`^([0-9A-Za-z]{1,12})-(review|fix|finish|flaky)$`)

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
		repo.serviceBotLogin = cfg.Service.BotLogin
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
		candidates := make([]sweepCandidate, 0, len(prs))
		for _, facts := range prs {
			snapshot, err := loadSweepSnapshot(ctx, repo, adaptation, facts)
			if err != nil {
				fmt.Fprintf(sweepLog, "snapshot %s#%s: %v\n", facts.RepoSlug(), facts.PR, err)
				continue
			}
			candidates = append(candidates, sweepCandidate{
				snapshot: snapshot, priority: classifySweepPriority(snapshot),
			})
		}
		rankSweepCandidates(candidates)
		for _, candidate := range candidates {
			if err := sweepPRSnapshot(ctx, cfg, repo, adaptation, candidate.snapshot, sweepLog); err != nil {
				facts := candidate.snapshot.facts
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
	return sweepPRSnapshot(ctx, cfg, repo, adaptation, sweepSnapshot{facts: facts, statuses: statuses}, logw)
}

func loadSweepSnapshot(ctx context.Context, repo RepoConfig, adaptation Adaptation, facts Facts) (sweepSnapshot, error) {
	statuses, err := adaptation.GetStatuses(ctx, facts.Owner, facts.Repo, facts.HeadSHA)
	if err != nil {
		return sweepSnapshot{}, err
	}
	reviews, err := adaptation.ListReviews(ctx, facts.Owner, facts.Repo, facts.PR)
	if err != nil {
		return sweepSnapshot{}, err
	}
	combinedStatus := ""
	if repo.Policy.AutoMerge && len(statuses) > 0 && !facts.HasLabel(LabelFlakyTests) {
		if review, ok := currentHeadBotReview(reviews, facts.HeadSHA, repo.serviceBotLogin); ok && strings.EqualFold(review.State, "APPROVED") && !facts.HasLabel(LabelReady) {
			combinedStatus, err = adaptation.GetCombinedStatus(ctx, facts.Owner, facts.Repo, facts.HeadSHA)
			if err != nil {
				return sweepSnapshot{}, err
			}
		}
	}
	return sweepSnapshot{facts: facts, statuses: statuses, reviews: reviews, combinedStatus: combinedStatus, serviceBotLogin: repo.serviceBotLogin}, nil
}

func sweepPRSnapshot(ctx context.Context, cfg ServiceConfig, repo RepoConfig, adaptation Adaptation, snapshot sweepSnapshot, logw *os.File) error {
	facts := snapshot.facts
	statuses := snapshot.statuses
	reviews := snapshot.reviews
	combinedStatus := snapshot.combinedStatus
	var err error
	for _, kind := range runKinds {
		label, _ := InFlightLabel(kind)
		if !facts.HasLabel(label) {
			continue
		}
		runDir, err := newestLiveRunDir(cfg.Runs.Dir, facts, kind, cfg.Sweep.LivenessThreshold.Duration)
		if err != nil {
			return err
		}
		if runDir == "" {
			fmt.Fprintf(logw, "release orphaned presence for %s on %s#%s\n", label, facts.RepoSlug(), facts.PR)
			if err := releaseRunPresence(ctx, adaptation, facts, label); err != nil {
				return err
			}
			continue
		}
		attempt, retryable, err := retryableAttempt(runDir, kind, statuses)
		if err != nil {
			return err
		}
		markedRetry := retryable && hasRetryableFailureMarker(runDir)
		if markedRetry {
			due, err := retryDue(runDir, attempt, time.Now())
			if err != nil {
				return err
			}
			if !due {
				fmt.Fprintf(logw, "backoff retains %s attempt %d for %s#%s\n", kind, attempt, facts.RepoSlug(), facts.PR)
				return nil
			}
			retryFacts := facts
			retryFacts.Labels = removeFactLabel(append([]string(nil), facts.Labels...), label)
			readyActor, err := resolveReadyActor(ctx, repo, adaptation, retryFacts)
			if err != nil {
				return err
			}
			flakyActor, err := resolveFlakyActor(ctx, repo, adaptation, retryFacts)
			if err != nil {
				return err
			}
			retryFacts.Actor = flakyActor
			retryAction, retryEligible := decideSweepAction(repo, retryFacts, statuses, reviews, combinedStatus, readyActor)
			if retryEligible && retryAction.Run == kind {
				spawned, err := spawnRunAfterAdmission(ctx, cfg, repo, facts, kind, "reconcile", func() (bool, error) {
					var handledRetry, releasedRetry bool
					statuses, handledRetry, releasedRetry, err = handleRetryableClaim(ctx, facts, kind, runDir, statuses, logw)
					if err != nil || !handledRetry || !releasedRetry {
						return false, err
					}
					if err := releaseRunPresence(ctx, adaptation, facts, label); err != nil {
						return false, err
					}
					facts.Labels = removeFactLabel(facts.Labels, label)
					return true, nil
				})
				if errors.Is(err, ErrRunCapacity) {
					fmt.Fprintf(logw, "deferred %s for %s#%s capacity=%d\n", kind, facts.RepoSlug(), facts.PR, cfg.Runs.MaxConcurrent)
					return nil
				}
				if err != nil {
					return err
				}
				if spawned {
					fmt.Fprintf(logw, "reconcile fires %s for %s#%s %s\n", kind, facts.RepoSlug(), facts.PR, facts.HeadSHA)
				}
				return nil
			}
			// Preserve the failed attempt even when current trigger guards no
			// longer authorise the same run kind to start again.
		}
		if !markedRetry {
			_, alive, err := runState(runDir, cfg.Sweep.LivenessThreshold.Duration)
			if err != nil {
				return err
			}
			if alive {
				fmt.Fprintf(logw, "alive %s for %s#%s at %s\n", kind, facts.RepoSlug(), facts.PR, runDir)
				return nil
			}
		}
		var handledRetry, releasedRetry bool
		statuses, handledRetry, releasedRetry, err = handleRetryableClaim(ctx, facts, kind, runDir, statuses, logw)
		if err != nil {
			return err
		}
		if handledRetry {
			if releasedRetry {
				if err := releaseRunPresence(ctx, adaptation, facts, label); err != nil {
					return err
				}
				facts.Labels = removeFactLabel(facts.Labels, label)
			} else {
				// Another sweep owns this claim, or its release failed closed. Do
				// not race it into the fixed systemd unit name.
				return nil
			}
			continue
		}
		// A marked retry was already proved due above. An unmarked claim reached
		// here only after the ordinary liveness threshold declared it stale.
		contextName, _ := StatusContext(kind)
		if _, completed := statusForContext(statuses, contextName); completed {
			if _, finished, err := readFinishedAt(runDir); err != nil {
				return err
			} else if finished {
				if err := cleanupRunDirResources(ctx, runDir); err != nil {
					fmt.Fprintf(logw, "finished-unit reap failed closed for %s: %v\n", runDir, err)
					return nil
				}
				if err := releaseRunPresence(ctx, adaptation, facts, label); err != nil {
					return err
				}
				facts.Labels = removeFactLabel(facts.Labels, label)
				fmt.Fprintf(logw, "reaped finished unit while preserving claim %s for %s#%s\n", runDir, facts.RepoSlug(), facts.PR)
				continue
			}
		}
		statuses, _, err = terminaliseUnsafeCrash(facts, kind, runDir, statuses)
		if err != nil {
			return err
		}
		if err := reapRunDir(ctx, runDir); err != nil {
			fmt.Fprintf(logw, "reap failed closed for %s: %v\n", runDir, err)
			return nil
		}
		fmt.Fprintf(logw, "reaped %s; release %s presence on %s#%s\n", runDir, label, facts.RepoSlug(), facts.PR)
		if err := releaseRunPresence(ctx, adaptation, facts, label); err != nil {
			return err
		}
	}
	statuses, err = reapLabelLessClaims(ctx, cfg, repo, adaptation, facts, statuses, logw)
	if err != nil {
		return err
	}
	readyActor, err := resolveReadyActor(ctx, repo, adaptation, facts)
	if err != nil {
		return err
	}
	flakyActor, err := resolveFlakyActor(ctx, repo, adaptation, facts)
	if err != nil {
		return err
	}
	// Open-PR facts have no event actor. Reconciliation supplies the actor that
	// applied the standing flaky label so its configured guard remains real.
	facts.Actor = flakyActor
	facts, err = clearUnauthorisedReady(ctx, repo, adaptation, facts, readyActor, logw)
	if err != nil {
		return err
	}
	action, ok := decideSweepAction(repo, facts, statuses, reviews, combinedStatus, readyActor)
	if !ok {
		return nil
	}
	if action.ApplyReady {
		applied, err := applyDeferredReady(ctx, repo, adaptation, facts)
		if err != nil {
			return err
		}
		if applied {
			fmt.Fprintf(logw, "reconcile reapplies %s for %s#%s %s\n", LabelReady, facts.RepoSlug(), facts.PR, facts.HeadSHA)
		} else {
			fmt.Fprintf(logw, "reconcile yields stale or newly blocked %s repair for %s#%s %s\n", LabelReady, facts.RepoSlug(), facts.PR, facts.HeadSHA)
		}
		return nil
	}
	decision := action.Run
	if pending, attempt, err := retryBackoffPending(cfg.Runs.Dir, facts, decision, statuses, time.Now()); err != nil {
		return err
	} else if pending {
		if attempt == 0 {
			fmt.Fprintf(logw, "live claim suppresses duplicate %s for %s#%s\n", decision, facts.RepoSlug(), facts.PR)
		} else {
			fmt.Fprintf(logw, "backoff suppresses %s attempt %d for %s#%s\n", decision, attempt, facts.RepoSlug(), facts.PR)
		}
		return nil
	}
	latched, err := hasTerminalMarker(cfg.Runs.Dir, facts, decision)
	if err != nil {
		return err
	}
	if latched {
		fmt.Fprintf(logw, "terminal marker suppresses %s for %s#%s %s\n", decision, facts.RepoSlug(), facts.PR, facts.HeadSHA)
		return nil
	}
	err = SpawnRun(ctx, cfg, repo, facts, decision, "reconcile")
	if errors.Is(err, ErrRunCapacity) {
		fmt.Fprintf(logw, "deferred %s for %s#%s capacity=%d\n", decision, facts.RepoSlug(), facts.PR, cfg.Runs.MaxConcurrent)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(logw, "reconcile fires %s for %s#%s %s\n", decision, facts.RepoSlug(), facts.PR, facts.HeadSHA)
	return nil
}

func applyDeferredReady(ctx context.Context, repo RepoConfig, adaptation Adaptation, snapshotFacts Facts) (bool, error) {
	current, err := adaptation.GetPRFacts(ctx, snapshotFacts.Forge, snapshotFacts.Owner, snapshotFacts.Repo, snapshotFacts.PR)
	if err != nil {
		return false, err
	}
	if current.HeadSHA != snapshotFacts.HeadSHA || current.HasLabel(LabelReady) || current.HasLabel(LabelFlakyTests) {
		return false, nil
	}
	statuses, err := adaptation.GetStatuses(ctx, current.Owner, current.Repo, current.HeadSHA)
	if err != nil {
		return false, err
	}
	reviews, err := adaptation.ListReviews(ctx, current.Owner, current.Repo, current.PR)
	if err != nil {
		return false, err
	}
	combinedStatus := ""
	if len(statuses) > 0 {
		combinedStatus, err = adaptation.GetCombinedStatus(ctx, current.Owner, current.Repo, current.HeadSHA)
		if err != nil {
			return false, err
		}
	}
	action, ok := decideSweepAction(repo, current, statuses, reviews, combinedStatus, "")
	if !ok || !action.ApplyReady {
		return false, nil
	}
	if err := adaptation.AddLabel(ctx, current.Owner, current.Repo, current.PR, LabelReady); err != nil {
		return false, err
	}
	return true, nil
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
	runDir, err := newestLiveRunDir(root, facts, kind, threshold)
	if err != nil || runDir == "" {
		return runDir, false, err
	}
	_, alive, err := runState(runDir, threshold)
	return runDir, alive, err
}

func runState(runDir string, threshold time.Duration) (string, bool, error) {
	if finishedAt, finished, err := readFinishedAt(runDir); err != nil {
		return "", false, err
	} else if finished {
		// A wrapper that has exited is not doing honest long-running work. Keep its
		// unit within the same generous grace period, then let the sweep recover a
		// cgroup that remained active because a descendant outlived the wrapper.
		return runDir, time.Since(finishedAt) < threshold, nil
	}
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

func newestLiveRunDir(root string, facts Facts, kind RunKind, threshold time.Duration) (string, error) {
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
		if newest.path == "" || runClaimAfter(claim, newest, threshold) {
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
	var claims []runClaim
	for _, entry := range entries {
		if !entry.IsDir() || isReapedRunDir(entry.Name()) || isRetryEvidenceDir(entry.Name()) {
			continue
		}
		matches := runClaimNamePattern.FindStringSubmatch(entry.Name())
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
	matches := runClaimNamePattern.FindStringSubmatch(name)
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
	if claim.metaMissing {
		stat, err := os.Stat(path)
		if err != nil {
			return runClaim{}, err
		}
		claim.startedAt = stat.ModTime()
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

func runClaimAfter(left, right runClaim, threshold time.Duration) bool {
	if left.metaMissing != right.metaMissing {
		leftFresh := left.metaMissing && time.Since(left.startedAt) < threshold
		rightFresh := right.metaMissing && time.Since(right.startedAt) < threshold
		if leftFresh != rightFresh {
			return leftFresh
		}
	}
	if !left.startedAt.Equal(right.startedAt) {
		return left.startedAt.After(right.startedAt)
	}
	return left.path > right.path
}

func reapLabelLessClaims(ctx context.Context, cfg ServiceConfig, repo RepoConfig, adaptation Adaptation, facts Facts, currentStatuses []Status, logw *os.File) ([]Status, error) {
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
		currentHead := claim.headSHA == facts.HeadSHA || strings.HasPrefix(facts.HeadSHA, claim.sha)
		if currentHead {
			attempt, retryable, err := retryableAttempt(claim.path, claim.kind, currentStatuses)
			if err != nil {
				return currentStatuses, err
			}
			markedRetry := retryable && hasRetryableFailureMarker(claim.path)
			if markedRetry {
				due, err := retryDue(claim.path, attempt, time.Now())
				if err != nil {
					return currentStatuses, err
				}
				if !due {
					fmt.Fprintf(logw, "backoff retains label-less %s attempt %d for %s#%s\n", claim.kind, attempt, facts.RepoSlug(), facts.PR)
					continue
				}
				var handledRetry bool
				currentStatuses, handledRetry, _, err = handleRetryableClaim(ctx, facts, claim.kind, claim.path, currentStatuses, logw)
				if err != nil {
					return currentStatuses, err
				}
				if handledRetry {
					continue
				}
			}
		}
		_, alive, err := runState(claim.path, cfg.Sweep.LivenessThreshold.Duration)
		if err != nil {
			return currentStatuses, err
		}
		if alive {
			continue
		}
		if currentHead {
			var handledRetry bool
			currentStatuses, handledRetry, _, err = handleRetryableClaim(ctx, facts, claim.kind, claim.path, currentStatuses, logw)
			if err != nil {
				return currentStatuses, err
			}
			if handledRetry {
				continue
			}
		}
		contextName, _ := StatusContext(claim.kind)
		if currentHead {
			if _, ok := statusForContext(currentStatuses, contextName); ok {
				// A completed wrapper can leave its ExitType=cgroup unit active even
				// after publishing terminal status. Once the common liveness grace
				// expires, its claim is cleanup evidence rather than live work.
				if _, finished, err := readFinishedAt(claim.path); err != nil {
					return currentStatuses, err
				} else if !finished {
					continue
				}
				active, err := runDirUnitActive(ctx, claim.path)
				if err != nil {
					return currentStatuses, err
				}
				if !active {
					continue
				}
				if err := cleanupRunDirResources(ctx, claim.path); err != nil {
					fmt.Fprintf(logw, "finished-unit reap failed closed for %s: %v\n", claim.path, err)
					continue
				}
				fmt.Fprintf(logw, "reaped finished unit while preserving label-less claim %s for %s#%s\n", claim.path, facts.RepoSlug(), facts.PR)
				continue
			}
		}
		if currentHead {
			var terminalised bool
			currentStatuses, terminalised, err = terminaliseUnsafeCrash(facts, claim.kind, claim.path, currentStatuses)
			if err != nil {
				return currentStatuses, err
			}
			if terminalised {
				fmt.Fprintf(logw, "second unmarked crash became terminal for %s on %s#%s\n", claim.kind, facts.RepoSlug(), facts.PR)
			}
		}
		if err := reapRunDir(ctx, claim.path); err != nil {
			fmt.Fprintf(logw, "label-less reap failed closed for %s: %v\n", claim.path, err)
			continue
		}
		// The stage label and eyes reaction are one presence signal. A human can
		// remove the label while the run is alive; once its abandoned claim is
		// reaped, leaving the reaction behind would advertise work that no longer
		// exists. Presence clean-up is deliberately best-effort.
		if err := adaptation.removeRunClaimReaction(ctx, facts.Owner, facts.Repo, facts.PR, runPresenceReaction); err != nil {
			fmt.Fprintf(logw, "label-less presence release failed for %s: %v\n", claim.path, err)
		}
		fmt.Fprintf(logw, "reaped label-less %s for %s#%s\n", claim.path, facts.RepoSlug(), facts.PR)
	}
	return currentStatuses, nil
}

func handleRetryableClaim(ctx context.Context, facts Facts, kind RunKind, runDir string, currentStatuses []Status, logw *os.File) ([]Status, bool, bool, error) {
	attempt, retryable, err := retryableAttempt(runDir, kind, currentStatuses)
	if err != nil || !retryable {
		return currentStatuses, false, false, err
	}
	if err := releaseRetryableClaim(ctx, runDir, attempt); err != nil {
		if errors.Is(err, errRetryClaimAlreadyReleased) {
			fmt.Fprintf(logw, "retry claim already released for %s; yielding\n", runDir)
			return currentStatuses, true, false, nil
		}
		fmt.Fprintf(logw, "retry release failed closed for %s: %v\n", runDir, err)
		return currentStatuses, true, false, nil
	}
	fmt.Fprintf(logw, "released retryable %s attempt %d for %s#%s\n", runDir, attempt, facts.RepoSlug(), facts.PR)
	return currentStatuses, true, true, nil
}

func retryableAttempt(runDir string, kind RunKind, currentStatuses []Status) (int, bool, error) {
	if _, err := readTerminalMarker(runDir); err == nil {
		return 0, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, false, err
	}
	if hasForgeWritesAttempted(runDir) {
		return 0, false, nil
	}
	contextName, _ := StatusContext(kind)
	if _, ok := statusForContext(currentStatuses, contextName); ok {
		return 0, false, nil
	}
	retryCount, err := retryEvidenceCount(runDir)
	if err != nil {
		return 0, false, err
	}
	return retryCount + 1, true, nil
}

func terminaliseUnsafeCrash(facts Facts, kind RunKind, runDir string, currentStatuses []Status) ([]Status, bool, error) {
	claim, err := readRunClaim(runDir)
	if err != nil {
		return currentStatuses, false, err
	}
	currentHead := claim.headSHA == facts.HeadSHA || strings.HasPrefix(facts.HeadSHA, claim.sha)
	if !currentHead {
		return currentStatuses, false, nil
	}
	contextName, _ := StatusContext(kind)
	if _, ok := statusForContext(currentStatuses, contextName); ok {
		return currentStatuses, false, nil
	}
	if _, err := readTerminalMarker(runDir); err == nil {
		return currentStatuses, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return currentStatuses, false, err
	}
	if !hasForgeWritesAttempted(runDir) {
		return currentStatuses, false, nil
	}
	if err := writeTerminalMarker(runDir, kind, facts.HeadSHA, "stale-after-forge-write"); err != nil {
		return currentStatuses, false, err
	}
	return currentStatuses, true, nil
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
		return fmt.Errorf("%w: %s", errRetryClaimAlreadyReleased, target)
	}
	if err := os.Rename(runDir, target); err != nil {
		if _, targetErr := os.Lstat(target); targetErr == nil {
			return fmt.Errorf("%w: %s", errRetryClaimAlreadyReleased, target)
		}
		return err
	}
	return nil
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

func runDirUnitActive(ctx context.Context, runDir string) (bool, error) {
	unit := readMeta(filepath.Join(runDir, "meta.env"))["PUMP19_UNIT"]
	if unit == "" {
		return false, nil
	}
	show := systemctlCommand(ctx, "systemctl", "--user", "show", unit, "--property=ActiveState", "--value")
	out, err := show.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if strings.Contains(message, "not loaded") || strings.Contains(message, "not found") {
			return false, nil
		}
		return false, fmt.Errorf("show unit %s: %w: %s", unit, err, message)
	}
	state := strings.TrimSpace(string(out))
	return state != "" && state != "inactive" && state != "failed", nil
}

func isRetryEvidenceDir(name string) bool {
	return strings.Contains(name, ".retry-")
}

func retryEvidenceCount(runDir string) (int, error) {
	matches, err := filepath.Glob(runDir + ".retry-*")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			return 0, err
		}
		if info.IsDir() {
			count++
		}
	}
	return count, nil
}

func resolveReadyActor(ctx context.Context, repo RepoConfig, adaptation Adaptation, facts Facts) (string, error) {
	if !facts.HasLabel(LabelReady) || !hasRunTrigger(repo, RunFinish) {
		return "", nil
	}
	return adaptation.LabelActor(ctx, facts.Owner, facts.Repo, facts.PR, LabelReady)
}

func resolveFlakyActor(ctx context.Context, repo RepoConfig, adaptation Adaptation, facts Facts) (string, error) {
	if !facts.HasLabel(LabelFlakyTests) || !hasRunTrigger(repo, RunFlaky) {
		return "", nil
	}
	return adaptation.LabelActor(ctx, facts.Owner, facts.Repo, facts.PR, LabelFlakyTests)
}

func clearUnauthorisedReady(ctx context.Context, repo RepoConfig, adaptation Adaptation, facts Facts, actor string, logw *os.File) (Facts, error) {
	if !facts.HasLabel(LabelReady) || !hasRunTrigger(repo, RunFinish) || actorGuardPass(repo, RunFinish, actor) {
		return facts, nil
	}
	if err := adaptation.RemoveLabel(ctx, facts.Owner, facts.Repo, facts.PR, LabelReady); err != nil {
		return facts, err
	}
	fmt.Fprintf(logw, "clear %s on %s#%s actor=%s failed finish actor guard\n", LabelReady, facts.RepoSlug(), facts.PR, actor)
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

func reconcileDecision(repo RepoConfig, facts Facts, statuses []Status, readyActor string) (RunKind, bool) {
	reviewContext, _ := StatusContext(RunReview)
	fixContext, _ := StatusContext(RunFix)
	finishContext, _ := StatusContext(RunFinish)
	flakyContext, _ := StatusContext(RunFlaky)
	if facts.HasLabel(LabelFlakyTests) {
		if _, flakyOK := statusForContext(statuses, flakyContext); !flakyOK && guardsPass(repo, RunFlaky, facts, facts.Actor) {
			return RunFlaky, true
		}
	}
	if _, ok := statusForContext(statuses, reviewContext); !ok && reviewReconcileGuardsPass(repo, facts) {
		return RunReview, true
	}
	reviewStatus, reviewOK := statusForContext(statuses, reviewContext)
	if reviewOK && reviewStatus.State == "failure" {
		if _, fixOK := statusForContext(statuses, fixContext); !fixOK && guardsPass(repo, RunFix, facts, reviewStatus.Creator) {
			return RunFix, true
		}
	}
	if facts.HasLabel(LabelReady) {
		if _, finishOK := statusForContext(statuses, finishContext); !finishOK {
			if guardsPass(repo, RunFinish, facts, readyActor) {
				return RunFinish, true
			}
		}
	}
	return "", false
}

func actorGuardPass(repo RepoConfig, kind RunKind, actor string) bool {
	for _, rule := range repo.Triggers {
		ruleKind, err := ParseRunKind(rule.Run)
		if err != nil || ruleKind != kind {
			continue
		}
		if len(rule.Actors) == 0 || matchesGlobAny(rule.Actors, actor) {
			return true
		}
	}
	return false
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
