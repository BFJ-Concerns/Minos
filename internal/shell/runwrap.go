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
		if preservesContinuation(runDir, os.Getenv("MINOS_HANDOFF")) {
			return
		}
		_ = removeRunDir(runDir)
	}()
	cmd := exec.CommandContext(ctx, runBodyPath())
	cmd.Dir = runDir
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func preservesContinuation(runDir, handoff string) bool {
	if handoff == "" {
		return false
	}
	marker, err := os.Open(filepath.Join(runDir, "lead-complete"))
	if err != nil {
		return false
	}
	data, readErr := io.ReadAll(io.LimitReader(marker, 65))
	closeErr := marker.Close()
	if readErr != nil || closeErr != nil || len(data) > 64 || strings.TrimSpace(string(data)) != "continuation" {
		return false
	}
	info, err := os.Stat(handoff)
	return err == nil && info.Mode().IsRegular()
}
