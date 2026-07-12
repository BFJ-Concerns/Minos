package preflight

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
)

func Command(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("preflight", flag.ContinueOnError)
	configPath := flags.String("config", "/etc/minos/preflight.toml", "preflight configuration file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("usage: minos preflight [--config path]")
	}
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		return err
	}
	report := Run(ctx, Probes(cfg))
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		return err
	}
	if !report.Passed {
		return errorsPreflightFailed
	}
	return nil
}

var errorsPreflightFailed = fmt.Errorf("preflight failed")
