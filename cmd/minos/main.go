package main

import (
	"context"
	"fmt"
	"os"

	"bfj/minos/internal/shell"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "minos: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: minos receive|sweep|run-wrap|run-guard|adapt|capture-claude|review|marker|handle|provenance|stub-run|ws-exec")
	}
	ctx := context.Background()
	switch args[0] {
	case "receive":
		return shell.ReceiveCommand(ctx, args[1:])
	case "sweep":
		return shell.SweepCommand(ctx, args[1:])
	case "run-wrap":
		return shell.RunWrapCommand(ctx, args[1:])
	case "run-guard":
		return shell.RunGuardCommand(ctx, args[1:])
	case "adapt":
		return shell.AdaptCommand(ctx, args[1:], os.Stdin, os.Stdout)
	case "capture-claude":
		return shell.CaptureClaudeCommand(ctx, args[1:], os.Stdin, os.Stderr)
	case "review":
		return shell.ReviewCommand(args[1:], os.Stdin, os.Stdout)
	case "marker":
		return shell.MarkerCommand(args[1:], os.Stdout)
	case "handle":
		return shell.HandleCommand(args[1:], os.Stdout)
	case "provenance":
		return shell.ProvenanceCommand(args[1:], os.Stdout)
	case "stub-run":
		return shell.StubRunCommand(ctx, args[1:])
	case "ws-exec":
		return shell.WorkspaceExecCommand(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
