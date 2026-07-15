package shell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var unitSafe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)
var systemdRunCommand = exec.CommandContext

func SpawnRun(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts) error {
	unit := UnitName(facts)
	if err := exec.CommandContext(ctx, "systemctl", "--user", "is-active", "--quiet", unit).Run(); err == nil {
		return nil
	}
	runDir, err := os.MkdirTemp(cfg.Runs.Dir, unit+"-")
	if err != nil {
		return err
	}
	forgeConfig := cfg.Forges[facts.Forge]
	env := map[string]string{
		"MINOS_RUN_DIR": runDir, "MINOS_CONFIG": cfg.Root, "MINOS_FORGE": facts.Forge,
		"MINOS_WORKSPACE": filepath.Join(runDir, "workspace"),
		"MINOS_OWNER":     facts.Owner, "MINOS_REPO_NAME": facts.Repo, "MINOS_PR": facts.PR,
		"MINOS_HEAD_SHA": facts.HeadSHA, "MINOS_TARGET_SHA": facts.BaseSHA,
		"MINOS_BASE_REF": facts.BaseRef, "MINOS_HEAD_BRANCH": facts.HeadRef,
		"MINOS_API_BASE": forgeConfig.APIBase, "MINOS_CREDENTIAL_FILE": forgeConfig.CredentialFile,
		"MINOS_BUILD_CMD": repo.Adaptation.Build, "MINOS_TEST_CMD": repo.Adaptation.Test,
		"MINOS_RUN_BODY": repo.Adaptation.RunBody, "MINOS_AUTO_MERGE": fmt.Sprintf("%t", repo.Policy.AutoMerge),
	}
	exe, err := os.Executable()
	if err != nil {
		_ = os.RemoveAll(runDir)
		return err
	}
	args := []string{"--user", "--collect", "--unit", unit, "--property=KillMode=control-group"}
	for key, value := range env {
		args = append(args, "--setenv", key+"="+value)
	}
	args = append(args, exe, "run", "--config", cfg.Root)
	out, err := systemdRunCommand(ctx, "systemd-run", args...).CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(runDir)
		if strings.Contains(string(out), "already exists") {
			return nil
		}
		return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func UnitName(facts Facts) string {
	return unitSafe.ReplaceAllString(fmt.Sprintf("minos-%s-%s-pr%s", facts.Owner, facts.Repo, facts.PR), "-")
}

func runBodyPath() string { return filepath.Clean(os.Getenv("MINOS_RUN_BODY")) }
