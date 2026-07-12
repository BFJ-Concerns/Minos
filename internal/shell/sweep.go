package shell

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"bfj/minos/internal/ledger"
	"bfj/minos/internal/reconcile"
)

var systemctlCommand = exec.CommandContext

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
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRunLedger, err)
	}
	defer store.Close()
	logw, closeLog, err := sweepWriter(cfg)
	if err != nil {
		return err
	}
	defer closeLog()
	repos, err := LoadRepoConfigs(cfg.Root)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		forge, ok := cfg.Forges[repo.Forge]
		if !ok {
			fmt.Fprintf(logw, "repo %s: unknown forge %s\n", repo.Path, repo.Forge)
			continue
		}
		adaptation, err := NewAdaptation(forge)
		if err != nil {
			return err
		}
		factsList, err := adaptation.ListOpenPRs(ctx, repo.Forge, repo.Owner, repo.Repo)
		if err != nil {
			return err
		}
		var candidates []sweepCandidate
		for _, facts := range factsList {
			facts.Occasion = "reconcile"
			snapshot, err := buildSnapshot(ctx, cfg, repo, facts)
			if err != nil {
				fmt.Fprintf(logw, "snapshot %s#%s: %v\n", facts.RepoSlug(), facts.PR, err)
				continue
			}
			view, err := ledgerView(ctx, store, snapshot.Key, cfg.Sweep.LivenessThreshold.Duration)
			if err != nil {
				return err
			}
			candidates = append(candidates, sweepCandidate{facts: facts, snapshot: snapshot, drain: view.Lease != nil || snapshot.Product.Valid()})
		}
		rankCandidates(candidates)
		for _, candidate := range candidates {
			facts, snapshot := candidate.facts, candidate.snapshot
			view, err := ledgerView(ctx, store, snapshot.Key, cfg.Sweep.LivenessThreshold.Duration)
			if err != nil {
				return err
			}
			decision := reconcile.Decide(snapshot, view, time.Now())
			if err := executeDecision(ctx, cfg, repo, facts, snapshot, decision, store, logw); err != nil {
				fmt.Fprintf(logw, "reconcile %s#%s: %v\n", facts.RepoSlug(), facts.PR, err)
			}
		}
	}
	// Cleanup obligations are enumerated independently because their PR is no
	// longer part of the open-PR sweep once the merge which created them lands.
	cleanups, err := store.ListCleanup(ctx)
	if err != nil {
		return err
	}
	for _, cleanup := range cleanups {
		repo, found := configuredRepo(repos, cleanup.Key)
		if !found {
			fmt.Fprintf(logw, "cleanup %s/%s#%s: repository is no longer configured\n", cleanup.Owner, cleanup.Repo, cleanup.PR)
			continue
		}
		facts := Facts{Forge: cleanup.Forge, Owner: cleanup.Owner, Repo: cleanup.Repo, PR: cleanup.PR, HeadSHA: cleanup.MergedHead, Occasion: "cleanup"}
		snapshot, err := buildSnapshot(ctx, cfg, repo, facts)
		if err != nil {
			fmt.Fprintf(logw, "cleanup snapshot %s/%s#%s: %v\n", cleanup.Owner, cleanup.Repo, cleanup.PR, err)
			continue
		}
		if err := executeDecision(ctx, cfg, repo, facts, snapshot, reconcile.Decision{Kind: reconcile.CleanUp}, store, logw); err != nil {
			fmt.Fprintf(logw, "cleanup %s/%s#%s: %v\n", cleanup.Owner, cleanup.Repo, cleanup.PR, err)
		}
	}
	return nil
}

func configuredRepo(repos []RepoConfig, key ledger.Key) (RepoConfig, bool) {
	for _, repo := range repos {
		if repo.Forge == key.Forge && repo.Owner == key.Owner && repo.Repo == key.Repo {
			return repo, true
		}
	}
	return RepoConfig{}, false
}

type sweepCandidate struct {
	facts    Facts
	snapshot reconcile.ForgeSnapshot
	drain    bool
}

func rankCandidates(values []sweepCandidate) {
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].drain != values[j].drain {
			return values[i].drain
		}
		left, leftErr := strconv.Atoi(values[i].facts.PR)
		right, rightErr := strconv.Atoi(values[j].facts.PR)
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		return values[i].facts.PR < values[j].facts.PR
	})
}

func sweepWriter(cfg ServiceConfig) (io.Writer, func(), error) {
	if strings.TrimSpace(cfg.Sweep.Log) == "" {
		return log.Writer(), func() {}, nil
	}
	file, err := os.OpenFile(cfg.Sweep.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return file, func() { _ = file.Close() }, nil
}

// stopAndVerifyUnitGone is the ordering boundary for replacement: callers may
// allocate the successor token only after this function confirms the old
// containment unit is inactive.
func stopAndVerifyUnitGone(ctx context.Context, unit string) error {
	if strings.TrimSpace(unit) == "" {
		return nil
	}
	out, err := systemctlCommand(ctx, "systemctl", "--user", "stop", unit).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "not loaded") {
			return nil
		}
		return fmt.Errorf("stop unit %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	out, err = systemctlCommand(ctx, "systemctl", "--user", "show", unit, "--property=ActiveState", "--value").CombinedOutput()
	if err != nil {
		// A collected transient unit has no cgroup left to inspect.
		return nil
	}
	state := strings.TrimSpace(string(out))
	if state != "" && state != "inactive" && state != "failed" {
		return fmt.Errorf("unit %s remains %s", unit, state)
	}
	return nil
}
