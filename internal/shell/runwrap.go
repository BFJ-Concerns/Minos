package shell

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"bfj/minos/internal/atomicreplace"
	"bfj/minos/internal/ledger"
)

type attemptInstrumentation struct {
	Schema        int          `json:"schema"`
	ArtefactFiles int          `json:"artefact_files"`
	ArtefactBytes int64        `json:"artefact_bytes"`
	Lead          *leadMetrics `json:"lead,omitempty"`
}

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
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRunLedger, err)
	}
	defer store.Close()
	facts := envFacts(os.Getenv("MINOS_FORGE"))
	key := coordinationKey(facts)
	token, err := attemptToken()
	if err != nil {
		return err
	}
	owned, err := store.Owns(ctx, key, token)
	if err != nil {
		return err
	}
	if !owned {
		return ledger.ErrNotOwner
	}
	var leaderPID int
	var cancelHeartbeat context.CancelFunc
	var heartbeatDone <-chan struct{}
	workspace := os.Getenv("MINOS_WORKSPACE")
	runDir := os.Getenv("MINOS_RUN_DIR")
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("run-wrap panic: %v", recovered)
		}
		if cancelHeartbeat != nil {
			cancelHeartbeat()
		}
		if heartbeatDone != nil {
			<-heartbeatDone
		}
		cleanupErr := cleanupRunProcessGroup(leaderPID)
		var removeErr error
		if workspace != "" {
			removeErr = os.RemoveAll(workspace)
		}
		instrumentationErr := writeAttemptInstrumentation(runDir)
		closeErr := closeRunLease(context.Background(), cfg, facts, store, token)
		err = errors.Join(err, cleanupErr, removeErr, instrumentationErr, closeErr)
	}()
	if runDir == "" || workspace == "" {
		return fmt.Errorf("MINOS_RUN_DIR and MINOS_WORKSPACE are required")
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(runDir, "run.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	multiOut, multiErr := io.MultiWriter(os.Stdout, logFile), io.MultiWriter(os.Stderr, logFile)
	if err := writeMeta(runDir); err != nil {
		return err
	}
	fmt.Fprintf(logFile, "minos lifecycle started at %s token=%d\n", time.Now().UTC().Format(time.RFC3339), token)

	heartbeatCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	cancelHeartbeat, heartbeatDone = cancel, done
	go renewHeartbeat(heartbeatCtx, store, key, token, heartbeatInterval(cfg), logFile, done)
	defer func() {
		fmt.Fprintf(logFile, "minos lifecycle finished at %s error=%t\n", time.Now().UTC().Format(time.RFC3339), err != nil)
		_ = logFile.Sync()
	}()

	forge, ok := cfg.Forges[facts.Forge]
	if !ok {
		return fmt.Errorf("unknown forge %q", facts.Forge)
	}
	adaptation, err := NewAdaptation(forge)
	if err != nil {
		return err
	}
	if err := adaptation.PrepareWorkspace(ctx, facts, workspace, os.Getenv("MINOS_DIFF")); err != nil {
		_, backoffErr := store.RecordFailure(context.Background(), key, token)
		return errors.Join(err, backoffErr)
	}
	cmd, err := runBodyCommandForWrap(ctx)
	if err != nil {
		_, backoffErr := store.RecordFailure(context.Background(), key, token)
		return errors.Join(err, backoffErr)
	}
	cmd.Stdout, cmd.Stderr, cmd.Env = multiOut, multiErr, os.Environ()
	configureRunProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		_, backoffErr := store.RecordFailure(context.Background(), key, token)
		return errors.Join(err, backoffErr)
	}
	leaderPID = cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		_, backoffErr := store.RecordFailure(context.Background(), key, token)
		return errors.Join(err, backoffErr)
	}
	retryable, declared, err := readRetryableExitRecord(runDir)
	if err != nil {
		return errors.Join(err, recordRetryableExit(context.Background(), cfg, facts, store, token, retryableExitRecord{Category: "retryable-exit-record"}))
	}
	if declared {
		return recordRetryableExit(context.Background(), cfg, facts, store, token, retryable)
	}
	cleared, err := store.ClearBackoff(context.Background(), key, token)
	if err != nil {
		return fmt.Errorf("clear operational backoff: %w", err)
	}
	if !cleared {
		if _, found, lookupErr := store.Backoff(context.Background(), key); lookupErr != nil {
			return fmt.Errorf("confirm operational backoff: %w", lookupErr)
		} else if found {
			return ledger.ErrNotOwner
		}
	}
	return nil
}

func writeAttemptInstrumentation(runDir string) error {
	if strings.TrimSpace(runDir) == "" {
		return nil
	}
	metrics := attemptInstrumentation{Schema: 1}
	err := filepath.WalkDir(runDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Base(path) == "instrumentation.json" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			metrics.ArtefactFiles++
			metrics.ArtefactBytes += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if data, err := os.ReadFile(filepath.Join(runDir, "lead-metrics.json")); err == nil {
		var lead leadMetrics
		if json.Unmarshal(data, &lead) == nil {
			metrics.Lead = &lead
		}
	}
	data, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	return atomicreplace.Write(filepath.Join(runDir, "instrumentation.json"), append(data, '\n'), 0o644)
}

func attemptToken() (int64, error) {
	value := os.Getenv("MINOS_ATTEMPT_TOKEN")
	token, err := strconv.ParseInt(value, 10, 64)
	if err != nil || token <= 0 {
		return 0, fmt.Errorf("invalid MINOS_ATTEMPT_TOKEN %q", value)
	}
	return token, nil
}

func heartbeatInterval(cfg ServiceConfig) time.Duration {
	interval := cfg.Sweep.LivenessThreshold.Duration / 6
	if interval < time.Second {
		return time.Second
	}
	return interval
}

func renewHeartbeat(ctx context.Context, store *ledger.Store, key ledger.Key, token int64, interval time.Duration, logw io.Writer, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewed, err := store.RenewHeartbeat(context.Background(), key, token)
			if err != nil {
				fmt.Fprintf(logw, "heartbeat renewal failed: %v\n", err)
				continue
			}
			if !renewed {
				fmt.Fprintln(logw, "heartbeat stopped: lease superseded")
				return
			}
		}
	}
}

func configureRunProcessGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

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

var runBodyCommandForWrap = runBodyCommand

func writeMeta(runDir string) error {
	meta := []string{
		"MINOS_UNIT=" + os.Getenv("MINOS_UNIT"),
		"MINOS_WORKSPACE=" + os.Getenv("MINOS_WORKSPACE"),
		"MINOS_STARTED_AT=" + time.Now().UTC().Format(time.RFC3339Nano),
		"MINOS_OCCASION=" + os.Getenv("MINOS_OCCASION"),
		"MINOS_HEAD_SHA=" + os.Getenv("MINOS_HEAD_SHA"),
		"MINOS_TARGET_SHA=" + os.Getenv("MINOS_TARGET_SHA"),
		"MINOS_ATTEMPT_TOKEN=" + os.Getenv("MINOS_ATTEMPT_TOKEN"),
	}
	if err := os.WriteFile(filepath.Join(runDir, "meta.env"), []byte(strings.Join(meta, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("write run metadata: %w", err)
	}
	return nil
}

func envFacts(forge string) Facts {
	return Facts{Forge: forge, Owner: os.Getenv("MINOS_OWNER"), Repo: os.Getenv("MINOS_REPO_NAME"), PR: os.Getenv("MINOS_PR"), HeadSHA: os.Getenv("MINOS_HEAD_SHA"), BaseRef: os.Getenv("MINOS_BASE_REF"), BaseSHA: os.Getenv("MINOS_TARGET_SHA")}
}
