package incidents

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"
)

func Command(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: minos incident raise|acknowledge|recover|list|check [options]")
	}
	if args[0] == "list" || args[0] == "check" {
		action := args[0]
		flags := flag.NewFlagSet("incident "+action, flag.ContinueOnError)
		storePath := flags.String("store", "/var/lib/minos/incidents", "incident store")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("usage: minos incident %s [--store directory]", action)
		}
		values, err := NewFileStore(*storePath).List(ctx)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(stdout).Encode(values); err != nil {
			return err
		}
		if action == "check" {
			open := 0
			for _, incident := range values {
				if incident.State == StateOpen {
					open++
				}
			}
			if open > 0 {
				return fmt.Errorf("%d open deployment incident(s)", open)
			}
		}
		return nil
	}
	return mutateCommand(ctx, args, stdout)
}

func mutateCommand(ctx context.Context, args []string, stdout io.Writer) error {
	action := args[0]
	flags := flag.NewFlagSet("incident "+action, flag.ContinueOnError)
	storePath := flags.String("store", "/var/lib/minos/incidents", "incident store")
	forge := flags.String("forge", "", "forge name")
	owner := flags.String("owner", "", "repository owner")
	repo := flags.String("repo", "", "repository name")
	pullRequest := flags.String("pr", "", "pull request identity")
	category := flags.String("category", "", "failure category")
	attempt := flags.Int("attempt", 0, "latest attempt counter")
	observedHead := flags.String("observed-head", "", "observed head revision")
	observedTarget := flags.String("observed-target", "", "observed target revision")
	diagnostic := flags.String("diagnostic", "", "secret-free operator diagnostic")
	logPath := flags.String("log", "", "secret-free diagnostic-log location")
	actor := flags.String("actor", "", "acknowledging operator")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("usage: minos incident %s [options]", action)
	}
	key := Key{Forge: *forge, Owner: *owner, Repo: *repo, PullRequest: *pullRequest, Category: *category}
	store := NewFileStore(*storePath)
	var (
		incident Incident
		err      error
	)
	switch action {
	case "raise":
		incident, err = store.Raise(ctx, Event{Key: key, Diagnostic: *diagnostic, LogPath: *logPath, Attempt: *attempt, ObservedHead: *observedHead, ObservedTarget: *observedTarget, At: time.Now()})
	case "acknowledge":
		incident, err = store.Acknowledge(ctx, key, *actor, time.Now())
	case "recover":
		incident, err = store.Recover(ctx, key, time.Now())
	default:
		return fmt.Errorf("unknown incident action %q", action)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(incident)
}
