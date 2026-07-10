package shell

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"
)

func StubRunCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("stub-run", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := LoadServiceConfig(os.Getenv("PUMP19_CONFIG"))
	if err != nil {
		return err
	}
	kind, err := ParseRunKind(os.Getenv("PUMP19_RUN_KIND"))
	if err != nil {
		return err
	}
	facts := envFacts(os.Getenv("PUMP19_FORGE"))
	forge := cfg.Forges[facts.Forge]
	adaptation, err := NewAdaptation(forge)
	if err != nil {
		return err
	}
	contextName, err := StatusContext(kind)
	if err != nil {
		return err
	}
	claim, err := claimRun(ctx, adaptation, facts, kind)
	if err != nil {
		return err
	}
	if claim != "claimed" {
		fmt.Println(claim)
		return nil
	}
	defer func() {
		_ = releaseRun(context.Background(), cfg, adaptation, facts, kind)
	}()
	mode := os.Getenv("PUMP19_STUB_MODE")
	if mode == "" {
		mode = "normal"
	}
	switch mode {
	case "hang":
		fmt.Println("stub entering hang mode")
		select {}
	case "slow":
		fmt.Println("stub slow mode started")
		duration := 2 * time.Second
		if seconds := os.Getenv("PUMP19_STUB_SLOW_SECONDS"); seconds != "" {
			parsed, err := time.ParseDuration(seconds + "s")
			if err != nil {
				return err
			}
			duration = parsed
		}
		time.Sleep(duration)
		fmt.Println("stub slow mode heartbeat")
	case "crash":
		fmt.Println("stub crash mode")
		if err := writeTerminalMarker(os.Getenv("PUMP19_RUN_DIR"), kind, facts.HeadSHA, "stub-crash"); err != nil {
			return fmt.Errorf("record stub crash: %w", err)
		}
		return fmt.Errorf("stub crash requested")
	case "normal":
		fmt.Println("stub normal mode")
	default:
		return fmt.Errorf("unknown PUMP19_STUB_MODE %q", mode)
	}
	liveFacts, err := adaptation.GetPRFacts(ctx, facts.Forge, facts.Owner, facts.Repo, facts.PR)
	if err != nil {
		return err
	}
	if liveFacts.HeadSHA != facts.HeadSHA {
		fmt.Printf("head moved before posting: served=%s live=%s; discarding output\n", facts.HeadSHA, liveFacts.HeadSHA)
		return nil
	}
	state := "success"
	description := "Pump-19 stub " + string(kind) + " completed"
	if kind == RunReview && os.Getenv("PUMP19_STUB_REVIEW_STATE") != "" {
		state = os.Getenv("PUMP19_STUB_REVIEW_STATE")
	}
	return adaptation.SetStatus(ctx, facts.Owner, facts.Repo, facts.HeadSHA, contextName, state, description)
}

func newerLiveRunDirExists(root string, facts Facts, kind RunKind, currentRunDir string, threshold time.Duration) bool {
	current, err := readRunClaim(currentRunDir)
	if err != nil {
		return false
	}
	runDir, err := newestLiveRunDir(root, facts, kind, threshold)
	if err != nil || runDir == "" || runDir == currentRunDir {
		return false
	}
	newest, err := readRunClaim(runDir)
	if err != nil {
		return false
	}
	return runClaimAfter(newest, current, threshold)
}
