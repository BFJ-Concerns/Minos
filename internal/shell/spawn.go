package shell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

var unitSafe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)
var commandCombinedOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type SpawnOutcome string

const (
	SpawnStarted    SpawnOutcome = "started"
	SpawnSuppressed SpawnOutcome = "suppressed"
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
	runDir, err := os.MkdirTemp(cfg.Runs.Dir, unit+"-")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(runDir, runOwnerMarker), nil, 0o600); err != nil {
		_ = os.RemoveAll(runDir)
		return "", fmt.Errorf("create run ownership marker: %w", err)
	}
	forgeConfig := cfg.Forges[facts.Forge]
	maximumRounds := ""
	if repo.Review.MaximumRounds > 0 {
		maximumRounds = strconv.Itoa(repo.Review.MaximumRounds)
	}
	env := map[string]string{
		"MINOS_RUN_DIR":          runDir,
		"MINOS_CONFIG":           cfg.Root,
		"MINOS_FORGE":            facts.Forge,
		"MINOS_WORKSPACE":        filepath.Join(runDir, "workspace"),
		"MINOS_ORIENTATION":      filepath.Join(runDir, "orientation.json"),
		"MINOS_OWNER":            facts.Owner,
		"MINOS_REPO_NAME":        facts.Repo,
		"MINOS_PR":               facts.PR,
		"MINOS_HEAD_SHA":         facts.HeadSHA,
		"MINOS_TARGET_SHA":       facts.BaseSHA,
		"MINOS_BASE_REF":         facts.BaseRef,
		"MINOS_HEAD_BRANCH":      facts.HeadRef,
		"MINOS_API_BASE":         forgeConfig.APIBase,
		"MINOS_CREDENTIAL_FILE":  forgeConfig.CredentialFile,
		"MINOS_BUILD_CMD":        repo.Adaptation.Build,
		"MINOS_TEST_CMD":         repo.Adaptation.Test,
		"MINOS_RUN_BODY":         repo.Adaptation.RunBody,
		"MINOS_AUTO_MERGE":       fmt.Sprintf("%t", repo.Policy.AutoMerge),
		"MINOS_REVIEW_THRESHOLD": repo.Review.Threshold,
		"MINOS_FIX_CLUSTER_CAP":  strconv.Itoa(repo.Review.ClusterCap),
		"MINOS_MAX_ROUNDS":       maximumRounds,
	}
	exe, err := os.Executable()
	if err != nil {
		_ = os.RemoveAll(runDir)
		return "", err
	}
	args := []string{
		"--user", "--collect", "--unit", unit,
		"--property=ExitType=main",
		"--property=KillMode=control-group",
		"--property=RuntimeMaxSec=12h",
	}
	for key, value := range env {
		args = append(args, "--setenv", key+"="+value)
	}
	args = append(args, exe, "run", "--config", cfg.Root)
	out, err = commandCombinedOutput(ctx, "systemd-run", args...)
	if err != nil {
		_ = os.RemoveAll(runDir)
		if strings.Contains(string(out), "already exists") {
			return SpawnSuppressed, nil
		}
		return "", fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return SpawnStarted, nil
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
