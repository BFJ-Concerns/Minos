package shell

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	defer removeRunDir(runDir)
	cmd := exec.CommandContext(ctx, runBodyPath())
	cmd.Dir = runDir
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
