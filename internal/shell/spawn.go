package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"bfj/minos/internal/ledger"
)

var unitSafe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

var ErrRunCapacity = ledger.ErrCapacity
var ErrRunLedger = ledger.ErrLedger

var systemdRunCommand = exec.CommandContext

func ledgerPath(cfg ServiceConfig) string { return filepath.Join(cfg.Runs.Dir, "coordination.db") }

func coordinationKey(facts Facts) ledger.Key {
	return ledger.Key{Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo, PR: facts.PR}
}

func SpawnRun(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, targetSHA, occasion string) error {
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRunLedger, err)
	}
	defer store.Close()
	unitName := UnitName(facts)
	workspace := filepath.Join(os.TempDir(), "minos-workspaces", unitName)
	lease, err := store.AcquireLease(ctx, ledger.Lease{
		Key: coordinationKey(facts), ObservedHead: facts.HeadSHA, ObservedTarget: targetSHA,
		Unit: unitName, Workspace: workspace,
	}, cfg.Runs.MaxConcurrent)
	if err != nil {
		// Another delivery won the same PR claim between Decide and the atomic
		// acquire. That is successful duplicate collapse, not a launch failure.
		if errors.Is(err, ledger.ErrNotOwner) {
			return nil
		}
		return err
	}
	if err := spawnRunUnit(ctx, cfg, repo, facts, lease, occasion); err != nil {
		// A failed detached launch must never strand the admission slot.
		releaseErr := closeRunLease(context.Background(), cfg, facts, store, lease.Token)
		return errors.Join(err, releaseErr)
	}
	return nil
}

func spawnRunUnit(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, lease ledger.Lease, occasion string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	env := runEnv(cfg, repo, facts, lease, occasion)
	for _, name := range []string{"MINOS_STUB_MODE", "MINOS_STUB_REVIEW_STATE", "MINOS_STUB_SLOW_SECONDS"} {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	args := []string{"--user", "--collect", "--unit", lease.Unit, "--property=ExitType=cgroup", "--property=KillMode=control-group"}
	for _, pair := range env {
		args = append(args, "--setenv", pair)
	}
	args = append(args, exe, "run-wrap", "--config", cfg.Root)
	out, err := systemdRunCommand(ctx, "systemd-run", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func UnitName(facts Facts) string {
	raw := fmt.Sprintf("minos-run-%s-%s-pr%s-%s", facts.Owner, facts.Repo, facts.PR, shortSHA(facts.HeadSHA))
	return unitSafe.ReplaceAllString(raw, "-")
}

func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func RunDir(root string, facts Facts, token int64) string {
	return filepath.Join(root, "attempts", unitSafe.ReplaceAllString(facts.Forge+"--"+facts.Owner+"--"+facts.Repo+"--pr"+facts.PR, "-"), strconv.FormatInt(token, 10))
}

func runEnv(cfg ServiceConfig, repo RepoConfig, facts Facts, lease ledger.Lease, occasion string) []string {
	runDir := RunDir(cfg.Runs.Dir, facts, lease.Token)
	diff := filepath.Join(runDir, "diff.patch")
	briefs := repo.Adaptation.Briefs
	if briefs == "" {
		briefs = ".review"
	}
	autoMerge := "false"
	if repo.Policy.AutoMerge {
		autoMerge = "true"
	}
	forge := cfg.Forges[facts.Forge]
	return []string{
		"MINOS_RUN_DIR=" + runDir,
		"MINOS_ATTEMPT_TOKEN=" + strconv.FormatInt(lease.Token, 10),
		"MINOS_OCCASION=" + occasion,
		"MINOS_FORGE=" + facts.Forge,
		"MINOS_REPO=" + facts.RepoSlug(),
		"MINOS_OWNER=" + facts.Owner,
		"MINOS_REPO_NAME=" + facts.Repo,
		"MINOS_PR=" + facts.PR,
		"MINOS_HEAD_SHA=" + facts.HeadSHA,
		"MINOS_TARGET_SHA=" + lease.ObservedTarget,
		"MINOS_BASE_REF=" + facts.BaseRef,
		"MINOS_WORKSPACE=" + lease.Workspace,
		"MINOS_DIFF=" + diff,
		"MINOS_ADAPTATION=" + forge.Adaptation,
		"MINOS_SKILL=" + repo.Adaptation.Skill,
		"MINOS_RUN_BODY=" + repo.Adaptation.RunBody,
		"MINOS_BRIEFS=" + briefs,
		"MINOS_BUILD_CMD=" + repo.Adaptation.Build,
		"MINOS_TEST_CMD=" + repo.Adaptation.Test,
		"MINOS_AUTO_MERGE=" + autoMerge,
		"MINOS_CONFIG=" + cfg.Root,
		"MINOS_UNIT=" + lease.Unit,
	}
}
