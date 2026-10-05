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
		return fmt.Errorf("usage: minos receive|sweep|run|forge|alert|install-units")
	}
	ctx := context.Background()
	switch args[0] {
	case "receive":
		return shell.ReceiveCommand(ctx, args[1:])
	case "sweep":
		return shell.SweepCommand(ctx, args[1:])
	case "run":
		return shell.RunCommand(ctx, args[1:])
	case "forge":
		return shell.ForgeCommand(ctx, args[1:], os.Stdout)
	case "alert":
		return shell.AlertCommand(ctx, args[1:])
	case "install-units":
		return shell.InstallUnitsCommand(args[1:], os.Stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
