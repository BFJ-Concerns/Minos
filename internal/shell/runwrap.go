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

func RunWrapCommand(ctx context.Context, args []string) (err error) {
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
	bodyStarted := false
	failurePhase := "pre-body"
	defer func() {
		if err == nil {
			return
		}
		fmt.Fprintf(logFile, "pump19 run-wrap error: %v\n", err)
		if !bodyStarted {
			if markerErr := writeRetryableFailure(runDir, failurePhase, err); markerErr != nil {
				fmt.Fprintf(logFile, "pump19 run-wrap could not record retryable failure: %v\n", markerErr)
			}
			return
		}
		if statusErr := recordRunWrapFailureStatus(ctx, adaptation, facts, kind, err); statusErr != nil {
			fmt.Fprintf(logFile, "pump19 run-wrap could not record failure status: %v\n", statusErr)
		}
	}()
	touchRunLog(logPath, logFile, "preparing workspace")
	failurePhase = "prepare-workspace"
	if err := adaptation.PrepareWorkspace(ctx, facts, workspace, os.Getenv("PUMP19_DIFF")); err != nil {
		return err
	}
	touchRunLog(logPath, logFile, "workspace ready")
	failurePhase = "run-body-start"
	cmd, err := runBodyCommand(ctx)
	if err != nil {
		return err
	}
	cmd.Stdout = multiOut
	cmd.Stderr = multiErr
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "PUMP19_RUN_KIND="+string(kind))
	if err := cmd.Start(); err != nil {
		return err
	}
	// Once Start succeeds, the body may have touched forge-visible state. From
	// this boundary onwards failure is terminal rather than safely retryable.
	bodyStarted = true
	return cmd.Wait()
}

func runBodyCommand(ctx context.Context) (*exec.Cmd, error) {
	if body := os.Getenv("PUMP19_RUN_BODY"); body != "" {
		return exec.CommandContext(ctx, body), nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, exe, "stub-run"), nil
}

func recordRunWrapFailureStatus(ctx context.Context, adaptation Adaptation, facts Facts, kind RunKind, cause error) error {
	if kind == RunFlaky {
		// Flaky-run failures are operator evidence, not PR outcomes. run.log
		// already carries the cause and the standing label keeps the pause honest.
		return nil
	}
	contextName, err := StatusContext(kind)
	if err != nil {
		return err
	}
	statuses, err := adaptation.GetStatuses(ctx, facts.Owner, facts.Repo, facts.HeadSHA)
	if err != nil {
		return fmt.Errorf("check existing status before wrapper failure write: %w", err)
	}
	if _, ok := statusForContext(statuses, contextName); ok {
		return nil
	}
	description := fmt.Sprintf("Pump-19 %s wrapper failed before terminal status", kind)
	return adaptation.SetStatus(ctx, facts.Owner, facts.Repo, facts.HeadSHA, contextName, "error", description)
}

func writeRetryableFailure(runDir, phase string, cause error) error {
	values := []string{
		"PUMP19_RETRYABLE_FAILURE=1",
		"PUMP19_FAILURE_PHASE=" + phase,
		"PUMP19_FAILURE_AT=" + time.Now().UTC().Format(time.RFC3339Nano),
		"PUMP19_FAILURE=" + strings.NewReplacer("\n", " ", "\r", " ").Replace(cause.Error()),
	}
	return os.WriteFile(filepath.Join(runDir, "retry.env"), []byte(strings.Join(values, "\n")+"\n"), 0o644)
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
		"PUMP19_STARTED_AT=" + time.Now().UTC().Format(time.RFC3339Nano),
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
