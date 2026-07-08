package main

import (
	"context"
	"fmt"
	"os"

	"bfj/pump19/internal/shell"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "pump19: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pump19 receive|sweep|run-wrap|stub-run|ws-exec")
	}
	ctx := context.Background()
	switch args[0] {
	case "receive":
		return shell.ReceiveCommand(ctx, args[1:])
	case "sweep":
		return shell.SweepCommand(ctx, args[1:])
	case "run-wrap":
		return shell.RunWrapCommand(ctx, args[1:])
	case "stub-run":
		return shell.StubRunCommand(ctx, args[1:])
	case "ws-exec":
		return shell.WorkspaceExecCommand(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
