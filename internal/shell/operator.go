package shell

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/product"
)

// HeldPullRequest is the forge-derived operator view of work that cannot
// currently proceed without attention to its hold or verdict.
type HeldPullRequest struct {
	Forge       string `json:"forge"`
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	PullRequest string `json:"pull_request"`
	Class       string `json:"class"`
	Stage       string `json:"stage,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// OperatorCommand exposes the small operator surface outside a lead run.
func OperatorCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: minos operator held|force")
	}
	switch args[0] {
	case "held":
		return operatorHeldCommand(ctx, args[1:], stdout)
	case "force":
		return operatorForceCommand(ctx, args[1:], stdout)
	default:
		return fmt.Errorf("unknown operator action %q", args[0])
	}
}

func operatorHeldCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("operator held", flag.ContinueOnError)
	configRoot := fs.String("config", DefaultConfigRoot, "configuration root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: minos operator held [--config ROOT]")
	}
	cfg, err := LoadServiceConfig(*configRoot)
	if err != nil {
		return err
	}
	repos, err := LoadRepoConfigs(cfg.Root)
	if err != nil {
		return err
	}
	var held []HeldPullRequest
	for _, repo := range repos {
		forgeConfig, ok := cfg.Forges[repo.Forge]
		if !ok {
			return fmt.Errorf("%s/%s: unknown forge %q", repo.Owner, repo.Repo, repo.Forge)
		}
		adaptation, err := NewAdaptation(forgeConfig)
		if err != nil {
			return err
		}
		facts, err := adaptation.ListOpenPRs(ctx, repo.Forge, repo.Owner, repo.Repo)
		if err != nil {
			return err
		}
		adapter, err := newBehaviouralForge(cfg, repo.Forge)
		if err != nil {
			return err
		}
		for _, fact := range facts {
			pullRequest, err := strconv.ParseInt(fact.PR, 10, 64)
			if err != nil {
				return fmt.Errorf("%s#%s: parse pull request: %w", fact.RepoSlug(), fact.PR, err)
			}
			snapshot, err := adapter.Snapshot(ctx, forge.Repository{Owner: fact.Owner, Name: fact.Repo}, pullRequest)
			if err != nil {
				return fmt.Errorf("%s#%s: read forge snapshot: %w", fact.RepoSlug(), fact.PR, err)
			}
			entry, found, err := heldPullRequest(ctx, adapter, cfg.Service.BotLogin, currentEnvironmentStamp(cfg), fact, snapshot)
			if err != nil {
				return fmt.Errorf("%s#%s: read held context: %w", fact.RepoSlug(), fact.PR, err)
			}
			if found {
				held = append(held, entry)
			}
		}
	}
	if held == nil {
		held = []HeldPullRequest{}
	}
	return json.NewEncoder(stdout).Encode(held)
}

func heldPullRequest(ctx context.Context, adapter *forge.Adapter, botLogin, envStamp string, facts Facts, snapshot forge.Snapshot) (HeldPullRequest, bool, error) {
	entry := HeldPullRequest{Forge: facts.Forge, Owner: facts.Owner, Repo: facts.Repo, PullRequest: facts.PR}
	if status, found := latestOwnedStatus(snapshot, botLogin); found && status.Description == product.Held().Description() {
		boundTarget, boundStamp := heldBinding(status.TargetURL)
		if boundTarget == snapshot.TargetSHA && boundStamp == envStamp {
			entry.Class = product.Held().Name()
			pullRequest, err := strconv.ParseInt(facts.PR, 10, 64)
			if err != nil {
				return HeldPullRequest{}, false, err
			}
			comments, err := adapter.IssueComments(ctx, forge.Repository{Owner: facts.Owner, Name: facts.Repo}, pullRequest)
			if err != nil {
				return HeldPullRequest{}, false, err
			}
			entry.Stage, entry.Reason = latestHeldComment(comments, botLogin)
			return entry, true, nil
		}
	}
	if review, found := currentReview(snapshot, botLogin); found {
		if class, reason, blocked := blockedVerdict(review, snapshot); blocked {
			entry.Class, entry.Reason = class, reason
			return entry, true, nil
		}
	}
	return HeldPullRequest{}, false, nil
}

func latestHeldComment(comments []forge.IssueComment, botLogin string) (string, string) {
	latest, found := latestOwnedComment(comments, botLogin)
	if !found {
		return "", ""
	}
	stage, reason, found := heldCommentFields(latest.Body)
	if !found {
		return "", ""
	}
	return stage, reason
}

func blockedVerdict(review forge.Review, snapshot forge.Snapshot) (string, string, bool) {
	if strings.ToUpper(review.State) != "REQUEST_CHANGES" && strings.ToUpper(review.State) != "REQUESTED_CHANGES" {
		return "", "", false
	}
	record, ok := product.TrailingRecord(review.Body)
	if !ok || record[product.RecordTargetKey] != snapshot.TargetSHA || record[product.RecordCauseKey] != product.RecordCauseRequiredChecks {
		return "", "", false
	}
	recordStart := strings.LastIndex(review.Body, "<!-- Minos:")
	reason := strings.TrimSpace(review.Body[:recordStart])
	return product.RecordCauseRequiredChecks, reason, true
}

func operatorForceCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("operator force", flag.ContinueOnError)
	configRoot := fs.String("config", DefaultConfigRoot, "configuration root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 4 {
		return fmt.Errorf("usage: minos operator force [--config ROOT] FORGE OWNER REPO PR")
	}
	facts := Facts{Forge: fs.Arg(0), Owner: fs.Arg(1), Repo: fs.Arg(2), PR: fs.Arg(3), Occasion: "operator-force"}
	cfg, err := LoadServiceConfig(*configRoot)
	if err != nil {
		return err
	}
	repo, err := FindRepoConfig(cfg, facts)
	if err != nil {
		return err
	}
	adapter, snapshot, err := currentForgeSnapshot(ctx, cfg, facts)
	if err != nil {
		return err
	}
	if snapshot.State != "open" || snapshot.Merged || snapshot.Draft {
		return fmt.Errorf("%s#%s is not an open, ready pull request", facts.RepoSlug(), facts.PR)
	}
	if workInProgressBranch(snapshot.HeadBranch, repo.WorkInProgressBranchPrefixes) {
		return fmt.Errorf("%s#%s: force refused: work-in-progress branch %q", facts.RepoSlug(), facts.PR, snapshot.HeadBranch)
	}
	if reason, deferred := dependencyDeferral(snapshot); deferred {
		return fmt.Errorf("%s#%s: force refused: %s", facts.RepoSlug(), facts.PR, reason)
	}
	facts.HeadSHA, facts.BaseSHA, facts.BaseRef, facts.HeadRef = snapshot.HeadSHA, snapshot.TargetSHA, snapshot.TargetBranch, snapshot.HeadBranch
	pullRequest, _ := strconv.ParseInt(facts.PR, 10, 64)
	repository := forge.Repository{Owner: facts.Owner, Name: facts.Repo}
	commits, commitsErr := adapter.PullRequestCommits(ctx, repository, pullRequest)
	if commitsErr != nil {
		commits = []forge.Commit{{SHA: snapshot.HeadSHA, Author: cfg.Service.BotLogin}}
	}
	admission := releasedHoldContext(ctx, adapter, snapshot, repository, pullRequest, cfg.Service.BotLogin, commits, currentEnvironmentStamp(cfg))
	runClass := RunClassReview
	if structuralBranch(snapshot.HeadBranch, repo.StructuralBranchPrefixes) {
		runClass = RunClassMaintenance
	}
	result, err := SpawnRun(ctx, cfg, repo, facts, admission, runClass)
	if err != nil {
		return err
	}
	if result.Outcome == SpawnSuppressed {
		if result.Detail != "" {
			return fmt.Errorf("%s#%s: force refused: %s", facts.RepoSlug(), facts.PR, result.Detail)
		}
		return fmt.Errorf("%s#%s: force refused by active unit %s", facts.RepoSlug(), facts.PR, result.BlockingUnit)
	}
	return json.NewEncoder(stdout).Encode(result)
}
