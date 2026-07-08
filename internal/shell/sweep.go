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
		if err := reapRunDir(ctx, runDir); err != nil {
			fmt.Fprintf(logw, "reap failed closed for %s: %v\n", runDir, err)
			return nil
		}
		fmt.Fprintf(logw, "reaped %s; clear %s on %s#%s\n", runDir, label, facts.RepoSlug(), facts.PR)
		if err := adaptation.RemoveLabel(ctx, facts.Owner, facts.Repo, facts.PR, label); err != nil {
			return err
		}
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
	var newest string
	var newestMod time.Time
	for _, entry := range entries {
		if !entry.IsDir() || isReapedRunDir(entry.Name()) || !pattern.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		if newest == "" || info.ModTime().After(newestMod) {
			newest = path
			newestMod = info.ModTime()
		}
	}
	return newest, nil
}

func reapRunDir(ctx context.Context, runDir string) error {
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
	target := fmt.Sprintf("%s.reaped-%d", runDir, time.Now().UnixNano())
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("reap target already exists: %s", target)
	}
	return os.Rename(runDir, target)
}

func stopAndVerifyUnitGone(ctx context.Context, unit string) error {
	stop := exec.CommandContext(ctx, "systemctl", "--user", "stop", unit)
	if out, err := stop.CombinedOutput(); err != nil {
		return fmt.Errorf("stop unit %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	show := exec.CommandContext(ctx, "systemctl", "--user", "show", unit, "--property=ActiveState", "--value")
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

func readMeta(path string) map[string]string {
	file, err := os.Open(path)
	if err != nil {
		return map[string]string{}
	}
	defer file.Close()
	values, err := parseKeyValues(file)
	if err != nil {
		return map[string]string{}
	}
	return values
}

func reconcileDecision(ctx context.Context, repo RepoConfig, adaptation Adaptation, facts Facts, statuses []Status) (RunKind, bool, error) {
	reviewContext, _ := StatusContext(RunReview)
	fixContext, _ := StatusContext(RunFix)
	finishContext, _ := StatusContext(RunFinish)
	if _, ok := statusForContext(statuses, reviewContext); !ok && guardsPass(repo, RunReview, facts, facts.Actor) {
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
		if len(rule.Actors) > 0 && !matchesGlobAny(rule.Actors, actor) {
			continue
		}
		return true
	}
	return false
}

func init() {
	log.SetFlags(log.LstdFlags | log.LUTC)
}
