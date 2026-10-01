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
	"strings"
)

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
	defer func() {
		preserve, err := preservesContinuation(runDir, os.Getenv("MINOS_HANDOFF"))
		if err != nil {
			log.Printf("preserve run directory %s: continuation state unreadable: %v", runDir, err)
			return
		}
		if preserve {
			return
		}
		if err := removeRunDir(runDir); err != nil {
			log.Printf("remove run directory %s: %v", runDir, err)
		}
	}()
	cmd := exec.CommandContext(ctx, runBodyPath())
	cmd.Dir = runDir
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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
