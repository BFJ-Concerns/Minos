package shell

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const archivePendingFile = ".archive-pending"

// The driver uses this exit code when it cannot set up an archive retry.
const archivePreservationExit = 74

func RunCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	_ = fs.String("config", DefaultConfigRoot, "configuration root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	runDir := os.Getenv("MINOS_RUN_DIR")
	if runDir == "" || os.Getenv("MINOS_RUN_BODY") == "" {
		return fmt.Errorf("MINOS_RUN_DIR and MINOS_RUN_BODY are required")
	}
	if err := os.Remove(filepath.Join(runDir, runOwnerMarker)); err != nil {
		return fmt.Errorf("refuse nested Minos run: this invocation does not own run directory %q: %w", runDir, err)
	}
	archivePending := filepath.Join(runDir, archivePendingFile)
	// A resumed continuation may still have its predecessor's archive
	// request. The driver archives the combined evidence when this run ends.
	if err := os.Remove(archivePending); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear predecessor archive request: %w", err)
	}
	cmd := exec.CommandContext(ctx, runBodyPath())
	cmd.Dir = runDir
	cmd.Env = append(os.Environ(), "MINOS_RUN_STARTED_AT="+strconv.FormatInt(time.Now().Unix(), 10))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() == archivePreservationExit {
		return runErr
	}
	if _, err := os.Lstat(archivePending); !os.IsNotExist(err) {
		if err != nil {
			log.Printf("preserve run directory %s: inspect archive request: %v", runDir, err)
		}
		return runErr
	}
	preserve, err := preservesContinuation(runDir, os.Getenv("MINOS_HANDOFF"))
	if err != nil {
		log.Printf("preserve run directory %s: continuation state unreadable: %v", runDir, err)
	} else if !preserve {
		if err := removeRunDir(runDir); err != nil {
			log.Printf("remove run directory %s: %v", runDir, err)
		}
	}
	return runErr
}

func preservesContinuation(runDir, handoff string) (bool, error) {
	if handoff == "" {
		return false, nil
	}
	marker, err := os.Open(filepath.Join(runDir, "lead-complete"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	data, readErr := io.ReadAll(io.LimitReader(marker, 65))
	closeErr := marker.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return false, err
	}
	if len(data) > 64 || strings.TrimSpace(string(data)) != "continuation" {
		return false, nil
	}
	info, err := os.Stat(handoff)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}
