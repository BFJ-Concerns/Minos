package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var unitSafe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

var ErrRunCapacity = errors.New("run capacity unavailable")
var ErrRunLedger = errors.New("run ledger unavailable")

func SpawnRun(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, kind RunKind, occasion string) error {
	_, err := spawnRunAfterAdmission(ctx, cfg, repo, facts, kind, occasion, nil)
	return err
}

// spawnRunAfterAdmission keeps retry evidence changes behind the same admission
// gate as the unit start. A capacity deferral therefore leaves the failed claim
// untouched for a later sweep rather than spending an attempt on work not run.
func spawnRunAfterAdmission(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, kind RunKind, occasion string, prepare func() (bool, error)) (bool, error) {
	spawned := false
	err := withRunAdmission(ctx, cfg, func() error {
		if prepare != nil {
			proceed, err := prepare()
			if err != nil || !proceed {
				return err
			}
		}
		if err := spawnRunUnit(ctx, cfg, repo, facts, kind, occasion); err != nil {
			return err
		}
		spawned = true
		return nil
	})
	return spawned, err
}

func withRunAdmission(ctx context.Context, cfg ServiceConfig, start func() error) error {
	if err := os.MkdirAll(cfg.Runs.Dir, 0o755); err != nil {
		return fmt.Errorf("create runs directory: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(cfg.Runs.Dir, ".dispatch.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open dispatch lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("%w: max-concurrent=%d", ErrRunCapacity, cfg.Runs.MaxConcurrent)
		}
		return fmt.Errorf("lock dispatch admission: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck // closing the descriptor also releases the lock

	active, err := liveRunUnitCount(ctx)
	if err != nil {
		return err
	}
	if active >= cfg.Runs.MaxConcurrent {
		return fmt.Errorf("%w: active=%d max-concurrent=%d", ErrRunCapacity, active, cfg.Runs.MaxConcurrent)
	}
	return start()
}

func liveRunUnitCount(ctx context.Context) (int, error) {
	cmd := systemctlCommand(ctx, "systemctl", "--user", "list-units", "--type=service", "--state=activating,active", "--no-legend", "--plain", "--full", "--no-pager", "pump19-run-*.service")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("%w: systemctl list-units: %v: %s", ErrRunLedger, err, strings.TrimSpace(string(out)))
	}
	count, err := countLiveRunUnits(out)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrRunLedger, err)
	}
	return count, nil
}

func countLiveRunUnits(output []byte) (int, error) {
	count := 0
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return 0, fmt.Errorf("malformed systemctl list-units row %q", line)
		}
		if !strings.HasPrefix(fields[0], "pump19-run-") || !strings.HasSuffix(fields[0], ".service") {
			continue
		}
		if fields[2] == "active" || fields[2] == "activating" {
			count++
		}
	}
	return count, nil
}

func spawnRunUnit(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, kind RunKind, occasion string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	runDir := RunDir(cfg.Runs.Dir, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, kind)
	unitName := UnitName(facts, kind)
	env := runEnv(cfg, repo, facts, kind, runDir, unitName, occasion)
	for _, name := range []string{"PUMP19_STUB_MODE", "PUMP19_STUB_REVIEW_STATE", "PUMP19_STUB_SLOW_SECONDS"} {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	args := []string{
		"--user", "--collect", "--unit", unitName,
		"--property=ExitType=cgroup",
		"--property=KillMode=control-group",
	}
	for _, pair := range env {
		args = append(args, "--setenv", pair)
	}
	args = append(args, exe, "run-wrap", "--config", cfg.Root)
	cmd := exec.CommandContext(ctx, "systemd-run", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func UnitName(facts Facts, kind RunKind) string {
	raw := fmt.Sprintf("pump19-run-%s-%s-pr%s-%s-%s", facts.Owner, facts.Repo, facts.PR, kind, shortSHA(facts.HeadSHA))
	return unitSafe.ReplaceAllString(raw, "-")
}

func runEnv(cfg ServiceConfig, repo RepoConfig, facts Facts, kind RunKind, runDir, unitName, occasion string) []string {
	workspace := filepath.Join(os.TempDir(), "pump19-workspaces", unitName)
	diff := runDir + "/diff.patch"
	skill := repo.Adaptation.Skill
	runBody := repo.Adaptation.RunBody
	briefs := repo.Adaptation.Briefs
	if briefs == "" {
		briefs = ".review"
	}
	forge := cfg.Forges[facts.Forge]
	// Fix and finish sessions read the repository's own build/test commands and
	// merge policy from the environment: they are per-repository adaptation, and
	// there is no in-session route to the repo config the receiver already holds.
	// Harmless to a review session, which never reads them.
	autoMerge := "false"
	if repo.Policy.AutoMerge {
		autoMerge = "true"
	}
	return []string{
		"PUMP19_RUN_DIR=" + runDir,
		"PUMP19_RUN_KIND=" + string(kind),
		"PUMP19_OCCASION=" + occasion,
		"PUMP19_FORGE=" + facts.Forge,
		"PUMP19_REPO=" + facts.RepoSlug(),
		"PUMP19_OWNER=" + facts.Owner,
		"PUMP19_REPO_NAME=" + facts.Repo,
		"PUMP19_PR=" + facts.PR,
		"PUMP19_HEAD_SHA=" + facts.HeadSHA,
		"PUMP19_BASE_REF=" + facts.BaseRef,
		"PUMP19_WORKSPACE=" + workspace,
		"PUMP19_DIFF=" + diff,
		"PUMP19_ADAPTATION=" + forge.Adaptation,
		"PUMP19_SKILL=" + skill,
		"PUMP19_RUN_BODY=" + runBody,
		"PUMP19_BRIEFS=" + briefs,
		"PUMP19_BUILD_CMD=" + repo.Adaptation.Build,
		"PUMP19_TEST_CMD=" + repo.Adaptation.Test,
		"PUMP19_AUTO_MERGE=" + autoMerge,
		"PUMP19_CONFIG=" + cfg.Root,
		"PUMP19_UNIT=" + unitName,
	}
}
