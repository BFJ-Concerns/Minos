package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/BFJ-Concerns/Minos/internal/shell"
	"github.com/BFJ-Concerns/Minos/internal/webpage"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "minos: %v\n", err)
		os.Exit(exitStatus(err))
	}
}

// exitStatus carries a child process's own exit status through: the run
// unit's status is run-body's (75 when the head is not spent, which the
// deployment guide and the journal then agree on); every other failure is 1.
func exitStatus(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() > 0 {
		return exit.ExitCode()
	}
	return 1
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: minos --version|receive|sweep|run|describe|forge|install-units")
	}
	ctx := context.Background()
	switch args[0] {
	case "--version":
		if len(args) != 1 {
			return fmt.Errorf("usage: minos --version")
		}
		_, err := fmt.Fprintln(os.Stdout, version)
		return err
	case "receive":
		return shell.ReceiveCommand(ctx, args[1:], webpage.Routes)
	case "sweep":
		return shell.SweepCommand(ctx, args[1:])
	case "run":
		return shell.RunCommand(ctx, args[1:])
	case "describe":
		return shell.DescribeCommand(ctx, args[1:])
	case "forge":
		return shell.ForgeCommand(ctx, args[1:], os.Stdout)
	case "install-units":
		return shell.InstallUnitsCommand(args[1:], os.Stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
