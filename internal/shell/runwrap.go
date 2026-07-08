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
	writeMeta(runDir)
	fmt.Fprintf(logFile, "pump19 run-wrap started at %s\n", time.Now().UTC().Format(time.RFC3339))
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
	if err := adaptation.PrepareWorkspace(ctx, facts, workspace, os.Getenv("PUMP19_DIFF")); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, "stub-run")
	cmd.Stdout = multiOut
	cmd.Stderr = multiErr
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "PUMP19_RUN_KIND="+string(kind))
	return cmd.Run()
}

func writeMeta(runDir string) {
	meta := []string{
		"PUMP19_UNIT=" + os.Getenv("PUMP19_UNIT"),
		"PUMP19_WORKSPACE=" + os.Getenv("PUMP19_WORKSPACE"),
		"PUMP19_STARTED_AT=" + time.Now().UTC().Format(time.RFC3339),
		"PUMP19_OCCASION=" + os.Getenv("PUMP19_OCCASION"),
	}
	_ = os.WriteFile(filepath.Join(runDir, "meta.env"), []byte(strings.Join(meta, "\n")+"\n"), 0o644)
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
