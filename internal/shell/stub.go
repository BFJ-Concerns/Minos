package shell

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
	label, err := InFlightLabel(kind)
	if err != nil {
		return err
	}
	liveFacts, err := adaptation.GetPRFacts(ctx, facts.Forge, facts.Owner, facts.Repo, facts.PR)
	if err != nil {
		return err
	}
	statuses, err := adaptation.GetStatuses(ctx, facts.Owner, facts.Repo, facts.HeadSHA)
	if err != nil {
		return err
	}
	if _, ok := statusForContext(statuses, contextName); ok {
		fmt.Printf("terminal status already exists for %s at %s; yielding\n", contextName, facts.HeadSHA)
		return nil
	}
	if liveFacts.HeadSHA != facts.HeadSHA {
		fmt.Printf("head moved before claim: served=%s live=%s; yielding\n", facts.HeadSHA, liveFacts.HeadSHA)
		return nil
	}
	if err := adaptation.AddLabel(ctx, facts.Owner, facts.Repo, facts.PR, label); err != nil {
		return err
	}
	defer func() {
		newer := newerLiveRunDirExists(cfg.Runs.Dir, facts, kind)
		if !newer {
			_ = adaptation.RemoveLabel(context.Background(), facts.Owner, facts.Repo, facts.PR, label)
		}
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
		_ = adaptation.SetStatus(ctx, facts.Owner, facts.Repo, facts.HeadSHA, contextName, "error", "stub run crashed")
		return fmt.Errorf("stub crash requested")
	case "normal":
		fmt.Println("stub normal mode")
	default:
		return fmt.Errorf("unknown PUMP19_STUB_MODE %q", mode)
	}
	liveFacts, err = adaptation.GetPRFacts(ctx, facts.Forge, facts.Owner, facts.Repo, facts.PR)
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

func newerLiveRunDirExists(root string, facts Facts, kind RunKind) bool {
	// The exact same deterministic directory is this run's own claim. Newer
	// heads use a different sha prefix, and reaped directories are evidence only.
	pattern := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, "*", kind)
	matches, _ := filepathGlob(pattern)
	for _, match := range matches {
		if isReapedRunDir(match) {
			continue
		}
		if match != os.Getenv("PUMP19_RUN_DIR") {
			return true
		}
	}
	return false
}

var filepathGlob = filepath.Glob
