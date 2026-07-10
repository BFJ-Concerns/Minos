package shell

import (
	"context"
	"flag"
	"fmt"
	"os"
)

const runPresenceReaction = "eyes"
const serviceBotLogin = "Minos"

// RunGuardCommand exposes the run claim's forge-visible guards to an agent
// session. Keeping these checks here gives real and stand-in sessions one
// lifecycle for posting work and releasing presence.
func RunGuardCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run-guard", flag.ContinueOnError)
	configRoot := fs.String("config", DefaultConfigRoot, "configuration root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: pump19 run-guard [--config root] begin|current|release")
	}

	cfg, adaptation, facts, kind, err := loadRunGuard(*configRoot)
	if err != nil {
		return err
	}
	switch fs.Arg(0) {
	case "begin":
		return beginRun(ctx, adaptation, facts, kind)
	case "current":
		return currentRun(ctx, adaptation, facts)
	case "release":
		return releaseRun(ctx, cfg, adaptation, facts, kind)
	default:
		return fmt.Errorf("unknown run-guard action %q", fs.Arg(0))
	}
}

func loadRunGuard(configRoot string) (ServiceConfig, Adaptation, Facts, RunKind, error) {
	cfg, err := LoadServiceConfig(configRoot)
	if err != nil {
		return ServiceConfig{}, Adaptation{}, Facts{}, "", err
	}
	kind, err := ParseRunKind(os.Getenv("PUMP19_RUN_KIND"))
	if err != nil {
		return ServiceConfig{}, Adaptation{}, Facts{}, "", err
	}
	forgeName := os.Getenv("PUMP19_FORGE")
	forge, ok := cfg.Forges[forgeName]
	if !ok {
		return ServiceConfig{}, Adaptation{}, Facts{}, "", fmt.Errorf("unknown forge %q", forgeName)
	}
	adaptation, err := NewAdaptation(forge)
	if err != nil {
		return ServiceConfig{}, Adaptation{}, Facts{}, "", err
	}
	return cfg, adaptation, envFacts(forgeName), kind, nil
}

func beginRun(ctx context.Context, adaptation Adaptation, facts Facts, kind RunKind) error {
	outcome, err := claimRun(ctx, adaptation, facts, kind)
	if err != nil {
		return err
	}
	fmt.Println(outcome)
	return nil
}

func claimRun(ctx context.Context, adaptation Adaptation, facts Facts, kind RunKind) (string, error) {
	contextName, err := StatusContext(kind)
	if err != nil {
		return "", err
	}
	statuses, err := adaptation.GetStatuses(ctx, facts.Owner, facts.Repo, facts.HeadSHA)
	if err != nil {
		return "", err
	}
	if _, exists := statusForContext(statuses, contextName); exists {
		return "yield-terminal", nil
	}
	current, err := adaptation.GetPRFacts(ctx, facts.Forge, facts.Owner, facts.Repo, facts.PR)
	if err != nil {
		return "", err
	}
	if current.HeadSHA != facts.HeadSHA {
		return "yield-head", nil
	}
	label, err := InFlightLabel(kind)
	if err != nil {
		return "", err
	}
	if err := adaptation.addRunClaimLabel(ctx, facts.Owner, facts.Repo, facts.PR, label); err != nil {
		return "", err
	}
	if err := adaptation.addRunClaimReaction(ctx, facts.Owner, facts.Repo, facts.PR, runPresenceReaction); err != nil {
		return "", err
	}
	if err := adaptation.assignRunClaimIfMissing(ctx, facts.Owner, facts.Repo, facts.PR, serviceBotLogin); err != nil {
		return "", err
	}
	return "claimed", nil
}

func currentRun(ctx context.Context, adaptation Adaptation, facts Facts) error {
	current, err := adaptation.GetPRFacts(ctx, facts.Forge, facts.Owner, facts.Repo, facts.PR)
	if err != nil {
		return err
	}
	if current.HeadSHA != facts.HeadSHA {
		fmt.Println("stale")
		return nil
	}
	fmt.Println("current")
	return nil
}

func releaseRun(ctx context.Context, cfg ServiceConfig, adaptation Adaptation, facts Facts, kind RunKind) error {
	if newerLiveRunDirExists(cfg.Runs.Dir, facts, kind, os.Getenv("PUMP19_RUN_DIR"), cfg.Sweep.LivenessThreshold.Duration) {
		fmt.Println("retained-newer-run")
		return nil
	}
	label, err := InFlightLabel(kind)
	if err != nil {
		return err
	}
	if err := releaseRunPresence(ctx, adaptation, facts, label); err != nil {
		return err
	}
	fmt.Println("released")
	return nil
}

func releaseRunPresence(ctx context.Context, adaptation Adaptation, facts Facts, label string) error {
	if err := adaptation.removeRunClaimReaction(ctx, facts.Owner, facts.Repo, facts.PR, runPresenceReaction); err != nil {
		return err
	}
	return adaptation.removeRunClaimLabel(ctx, facts.Owner, facts.Repo, facts.PR, label)
}
