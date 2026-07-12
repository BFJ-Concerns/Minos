package shell

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	runDir := os.Getenv("MINOS_RUN_DIR")
	if runDir == "" {
		return fmt.Errorf("MINOS_RUN_DIR is required")
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
	fmt.Fprintf(logFile, "minos run-wrap started at %s\n", time.Now().UTC().Format(time.RFC3339))
	touchRunLog(logPath, logFile, "metadata written")
	workspace := os.Getenv("MINOS_WORKSPACE")
	if workspace == "" {
		return fmt.Errorf("MINOS_WORKSPACE is required")
	}
	defer func() {
		_ = os.RemoveAll(workspace)
		finishedAt := time.Now().UTC()
		fmt.Fprintf(logFile, "minos run-wrap finished at %s\n", finishedAt.Format(time.RFC3339))
		_ = logFile.Sync()
		outcome := "success"
		if err != nil {
			outcome = "error"
		}
		if markerErr := writeFinishedMarker(runDir, finishedAt, outcome); markerErr != nil {
			if err == nil {
				err = fmt.Errorf("record run completion: %w", markerErr)
			} else {
				fmt.Fprintf(logFile, "minos run-wrap could not record completion: %v\n", markerErr)
			}
		}
	}()
	kind, err := ParseRunKind(os.Getenv("MINOS_RUN_KIND"))
	if err != nil {
		return err
	}
	forgeName := os.Getenv("MINOS_FORGE")
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
		fmt.Fprintf(logFile, "minos run-wrap error: %v\n", err)
		if !bodyStarted || !hasForgeWritesAttempted(runDir) {
			if markerErr := writeRetryableFailure(runDir, failurePhase); markerErr != nil {
				fmt.Fprintf(logFile, "minos run-wrap could not record retryable failure: %v\n", markerErr)
			}
			return
		}
		if markerErr := writeTerminalMarker(runDir, kind, os.Getenv("MINOS_HEAD_SHA"), "body-exit-after-forge-write"); markerErr != nil {
			fmt.Fprintf(logFile, "minos run-wrap could not record terminal failure: %v\n", markerErr)
		}
	}()
	touchRunLog(logPath, logFile, "preparing workspace")
	failurePhase = "prepare-workspace"
	if err := adaptation.PrepareWorkspace(ctx, facts, workspace, os.Getenv("MINOS_DIFF")); err != nil {
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
	cmd.Env = append(cmd.Env, "MINOS_RUN_KIND="+string(kind))
	configureRunProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Once Start succeeds, the body may touch forge-visible state. The shared
	// adaptation dispatcher records that boundary precisely; an early body exit
	// without its marker remains safe for the liveness-spaced retry budget.
	bodyStarted = true
	failurePhase = "run-body-exit"
	waitErr := cmd.Wait()
	cleanupErr := cleanupRunProcessGroup(cmd.Process.Pid)
	return errors.Join(waitErr, cleanupErr)
}

func configureRunProcessGroup(cmd *exec.Cmd) {
	// The body may leave adaptation pipeline children behind. Giving the body
	// its own process group lets run-wrap remove those descendants without
	// signalling itself or relying on an arbitrary runtime timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func cleanupRunProcessGroup(leaderPID int) error {
	if leaderPID <= 0 {
		return nil
	}
	if err := syscall.Kill(-leaderPID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill run process group %d: %w", leaderPID, err)
	}
	return nil
}

func runBodyCommand(ctx context.Context) (*exec.Cmd, error) {
	if body := os.Getenv("MINOS_RUN_BODY"); body != "" {
		return exec.CommandContext(ctx, body), nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, exe, "stub-run"), nil
}

func writeRetryableFailure(runDir, phase string) error {
	values := []string{
		"MINOS_RETRYABLE_FAILURE=1",
		"MINOS_FAILURE_PHASE=" + phase,
		"MINOS_FAILURE_AT=" + time.Now().UTC().Format(time.RFC3339Nano),
	}
	return atomicPublishFile(filepath.Join(runDir, "retry.env"), []byte(values[0]+"\n"+values[1]+"\n"+values[2]+"\n"), 0o644)
}

func touchRunLog(logPath string, logFile *os.File, message string) {
	fmt.Fprintf(logFile, "minos run-wrap: %s at %s\n", message, time.Now().UTC().Format(time.RFC3339))
	_ = logFile.Sync()
	now := time.Now()
	_ = os.Chtimes(logPath, now, now)
}

func writeMeta(runDir string) error {
	meta := []string{
		"MINOS_UNIT=" + os.Getenv("MINOS_UNIT"),
		"MINOS_WORKSPACE=" + os.Getenv("MINOS_WORKSPACE"),
		"MINOS_STARTED_AT=" + time.Now().UTC().Format(time.RFC3339Nano),
		"MINOS_OCCASION=" + os.Getenv("MINOS_OCCASION"),
		"MINOS_HEAD_SHA=" + os.Getenv("MINOS_HEAD_SHA"),
	}
	if err := os.WriteFile(filepath.Join(runDir, "meta.env"), []byte(strings.Join(meta, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("write run metadata: %w", err)
	}
	return nil
}

func envFacts(forge string) Facts {
	owner := os.Getenv("MINOS_OWNER")
	repo := os.Getenv("MINOS_REPO_NAME")
	return Facts{
		Forge:   forge,
		Owner:   owner,
		Repo:    repo,
		PR:      os.Getenv("MINOS_PR"),
		HeadSHA: os.Getenv("MINOS_HEAD_SHA"),
		BaseRef: os.Getenv("MINOS_BASE_REF"),
	}
}
