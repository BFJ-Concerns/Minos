package shell

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"
)

// StubRunCommand is a mechanical lifecycle stand-in for coordination tests. It
// deliberately knows no review/fix/finish stages.
func StubRunCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("stub-run", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, adaptation, facts, store, token, err := loadRunGuard(os.Getenv("MINOS_CONFIG"))
	if err != nil {
		return err
	}
	defer store.Close()
	if err := beginRun(ctx, adaptation, facts, store, token); err != nil {
		return err
	}
	defer func() { _ = releaseRun(context.Background(), adaptation, facts, store, token) }()
	switch mode := os.Getenv("MINOS_STUB_MODE"); mode {
	case "hang":
		select {}
	case "slow":
		duration := 2 * time.Second
		if raw := os.Getenv("MINOS_STUB_SLOW_SECONDS"); raw != "" {
			parsed, err := time.ParseDuration(raw + "s")
			if err != nil {
				return err
			}
			duration = parsed
		}
		time.Sleep(duration)
	case "crash":
		return fmt.Errorf("stub crash requested")
	case "", "normal":
	default:
		return fmt.Errorf("unknown MINOS_STUB_MODE %q", mode)
	}
	current, err := currentAttempt(ctx, adaptation, facts, store, token)
	if err != nil {
		return err
	}
	if !current {
		fmt.Println("stale; output discarded")
		return nil
	}
	return adaptation.SetStatus(ctx, facts.Owner, facts.Repo, facts.HeadSHA, "Minos", "success", "Changes approved")
}
