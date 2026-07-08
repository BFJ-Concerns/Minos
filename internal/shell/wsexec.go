package shell

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
)

func WorkspaceExecCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ws-exec", flag.ContinueOnError)
	configRoot := fs.String("config", DefaultConfigRoot, "configuration root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: pump19 ws-exec [--config root] command [args...]")
	}
	cfg, err := LoadServiceConfig(*configRoot)
	if err != nil {
		return err
	}
	workspace := os.Getenv("PUMP19_WORKSPACE")
	if workspace == "" {
		return fmt.Errorf("PUMP19_WORKSPACE is required")
	}
	cmd := exec.CommandContext(ctx, fs.Arg(0), fs.Args()[1:]...)
	cmd.Dir = workspace
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = scrubEnv(os.Environ(), cfg.Scrub.Vars)
	return cmd.Run()
}

func scrubEnv(env []string, vars []string) []string {
	blocked := make(map[string]bool, len(vars))
	for _, name := range vars {
		blocked[name] = true
	}
	out := env[:0]
	for _, pair := range env {
		name := pair
		for i, ch := range pair {
			if ch == '=' {
				name = pair[:i]
				break
			}
		}
		if !blocked[name] {
			out = append(out, pair)
		}
	}
	return out
}
