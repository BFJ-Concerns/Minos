package shell

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func RunWrapCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run-wrap", flag.ContinueOnError)
	configRoot := fs.String("config", DefaultConfigRoot, "configuration root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := LoadServiceConfig(*configRoot)
	if err != nil {
		return err
	}
	runDir := os.Getenv("PUMP19_RUN_DIR")
	if runDir == "" {
		return fmt.Errorf("PUMP19_RUN_DIR is required")
	}
	claimed, err := ClaimRunDir(runDir)
	if err != nil {
		return err
	}
	if !claimed {
		fmt.Fprintf(os.Stderr, "run claim already exists: %s\n", runDir)
		return nil
	}
	logPath := filepath.Join(runDir, "run.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	multiOut := io.MultiWriter(os.Stdout, logFile)
	multiErr := io.MultiWriter(os.Stderr, logFile)
	if err := writeMeta(runDir); err != nil {
		return err
	}
	fmt.Fprintf(logFile, "pump19 run-wrap started at %s\n", time.Now().UTC().Format(time.RFC3339))
	touchRunLog(logPath, logFile, "metadata written")
	workspace := os.Getenv("PUMP19_WORKSPACE")
	if workspace == "" {
		return fmt.Errorf("PUMP19_WORKSPACE is required")
	}
	defer func() {
		_ = os.RemoveAll(workspace)
		fmt.Fprintf(logFile, "pump19 run-wrap finished at %s\n", time.Now().UTC().Format(time.RFC3339))
	}()
	kind, err := ParseRunKind(os.Getenv("PUMP19_RUN_KIND"))
	if err != nil {
		return err
	}
	forgeName := os.Getenv("PUMP19_FORGE")
	forge, ok := cfg.Forges[forgeName]
	if !ok {
		return fmt.Errorf("unknown forge %q", forgeName)
	}
	adaptation, err := NewAdaptation(forge)
	if err != nil {
		return err
	}
	facts := envFacts(forgeName)
	touchRunLog(logPath, logFile, "preparing workspace")
	if err := adaptation.PrepareWorkspace(ctx, facts, workspace, os.Getenv("PUMP19_DIFF")); err != nil {
		return err
	}
	touchRunLog(logPath, logFile, "workspace ready")
	cmd, err := runBodyCommand(ctx)
	if err != nil {
		return err
	}
	cmd.Stdout = multiOut
	cmd.Stderr = multiErr
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "PUMP19_RUN_KIND="+string(kind))
	return cmd.Run()
}

func runBodyCommand(ctx context.Context) (*exec.Cmd, error) {
	if skill := os.Getenv("PUMP19_SKILL"); skill != "" {
		return exec.CommandContext(ctx, skill), nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, exe, "stub-run"), nil
}

func touchRunLog(logPath string, logFile *os.File, message string) {
	fmt.Fprintf(logFile, "pump19 run-wrap: %s at %s\n", message, time.Now().UTC().Format(time.RFC3339))
	_ = logFile.Sync()
	now := time.Now()
	_ = os.Chtimes(logPath, now, now)
}

func writeMeta(runDir string) error {
	meta := []string{
		"PUMP19_UNIT=" + os.Getenv("PUMP19_UNIT"),
		"PUMP19_WORKSPACE=" + os.Getenv("PUMP19_WORKSPACE"),
		"PUMP19_STARTED_AT=" + time.Now().UTC().Format(time.RFC3339),
		"PUMP19_OCCASION=" + os.Getenv("PUMP19_OCCASION"),
		"PUMP19_HEAD_SHA=" + os.Getenv("PUMP19_HEAD_SHA"),
	}
	if err := os.WriteFile(filepath.Join(runDir, "meta.env"), []byte(strings.Join(meta, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("write run metadata: %w", err)
	}
	return nil
}

func envFacts(forge string) Facts {
	owner := os.Getenv("PUMP19_OWNER")
	repo := os.Getenv("PUMP19_REPO_NAME")
	return Facts{
		Forge:   forge,
		Owner:   owner,
		Repo:    repo,
		PR:      os.Getenv("PUMP19_PR"),
		HeadSHA: os.Getenv("PUMP19_HEAD_SHA"),
		BaseRef: os.Getenv("PUMP19_BASE_REF"),
	}
}
