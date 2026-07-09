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

func SpawnRun(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, kind RunKind, occasion string) error {
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
	args := []string{"--user", "--collect", "--unit", unitName}
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
		"PUMP19_CONFIG=" + cfg.Root,
		"PUMP19_UNIT=" + unitName,
	}
}
