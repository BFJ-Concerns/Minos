package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/product"
)

// ForgeCommand gives the lead a small command surface for reading and updating
// the pull request it was started for.
func ForgeCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: minos forge snapshot|snapshot-member|head-movement|check-logs|claim|claim-member|status[-member]|review[-member]|comment[-member]|reaction[-member]|reaction-remove[-member]|label-remove[-member]|merge[-member]|delete-source-branch[-member]")
	}
	adapter, guard, botLogin, err := leadForge()
	if err != nil {
		return err
	}
	guard, args, err = memberAddressedForgeWrite(args, guard)
	if err != nil {
		return err
	}
	switch args[0] {
	case "snapshot":
		if len(args) != 1 {
			return fmt.Errorf("usage: minos forge snapshot")
		}
		snapshot, err := adapter.Snapshot(ctx, guard.Repository, guard.PullRequest)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(snapshot)
	case "snapshot-member":
		if len(args) != 4 {
			return fmt.Errorf("usage: minos forge snapshot-member OWNER REPO NUMBER")
		}
		if args[1] != guard.Repository.Owner || args[2] != guard.Repository.Name {
			return fmt.Errorf("member is outside this run repository")
		}
		pullRequest, err := strconv.ParseInt(args[3], 10, 64)
		if err != nil {
			return fmt.Errorf("member number: %w", err)
		}
		snapshot, err := adapter.Snapshot(ctx, guard.Repository, pullRequest)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(snapshot)
	case "head-movement":
		if len(args) != 3 {
			return fmt.Errorf("usage: minos forge head-movement EARLIER LATER")
		}
		commits, err := adapter.PullRequestCommits(ctx, guard.Repository, guard.PullRequest)
		if err != nil {
			return err
		}
		movement := "foreign"
		if forge.OwnMovement(commits, args[1], args[2], botLogin) {
			movement = "own"
		}
		_, err = fmt.Fprintln(stdout, movement)
		return err
	case "check-logs":
		if len(args) != 3 {
			return fmt.Errorf("usage: minos forge check-logs HEAD TARGET")
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		evidence, err := adapter.CheckLogs(ctx, guard)
		if err != nil {
			return err
		}
		_, err = stdout.Write(evidence)
		return err
	case "claim":
		if len(args) != 1 {
			return fmt.Errorf("usage: minos forge claim")
		}
		return emitForgeResult(stdout, "claim", adapter.Claim(ctx, guard.Repository, guard.PullRequest))
	case "claim-member":
		if len(args) != 4 {
			return fmt.Errorf("usage: minos forge claim-member OWNER REPO NUMBER")
		}
		cfg, err := LoadServiceConfig(os.Getenv("MINOS_CONFIG"))
		if err != nil {
			return err
		}
		repos, err := LoadRepoConfigs(cfg.Root)
		if err != nil {
			return err
		}
		var repo RepoConfig
		for _, candidate := range repos {
			if candidate.Forge == os.Getenv("MINOS_FORGE") && candidate.Owner == guard.Repository.Owner && candidate.Repo == guard.Repository.Name {
				repo = candidate
				break
			}
		}
		if repo.Repo == "" {
			return fmt.Errorf("lead repository is not configured")
		}
		primary := Facts{Forge: os.Getenv("MINOS_FORGE"), Owner: guard.Repository.Owner, Repo: guard.Repository.Name, PR: strconv.FormatInt(guard.PullRequest, 10)}
		facts, err := claimGroupMember(ctx, cfg, repo, primary, args[1], args[2], args[3], os.Getenv("MINOS_WORKSPACE"))
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(facts)
	case "status":
		if len(args) != 4 {
			return fmt.Errorf("usage: minos forge status HEAD TARGET working|attention|incomplete|held|clean|merged|continuation")
		}
		state, ok := namedProductState(args[3])
		if !ok {
			return fmt.Errorf("unknown product state %q", args[3])
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		return emitForgeResult(stdout, "status", adapter.SetProductStatus(ctx, guard, state))
	case "review":
		if len(args) != 5 && len(args) != 6 {
			return fmt.Errorf("usage: minos forge review HEAD TARGET approve|approve-chain-wait|request-changes|request-changes-checks|comment BODY_FILE [COMMENTS_FILE]")
		}
		checkCaused := args[3] == "request-changes-checks"
		chainWait := args[3] == "approve-chain-wait"
		verdict, ok := map[string]forge.ReviewVerdict{
			"approve": forge.ReviewApprove, "approve-chain-wait": forge.ReviewApprove, "request-changes": forge.ReviewRequestChanges,
			"request-changes-checks": forge.ReviewRequestChanges, "comment": forge.ReviewVerdictComment,
		}[args[3]]
		if !ok {
			return fmt.Errorf("unknown review verdict %q", args[3])
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		body, err := os.ReadFile(args[4])
		if err != nil {
			return err
		}
		recordValues := map[string]string{"head": guard.HeadSHA, "target": guard.TargetSHA}
		if checkCaused {
			recordValues[product.RecordCauseKey] = product.RecordCauseRequiredChecks
		}
		if chainWait {
			recordValues[product.RecordCauseKey] = product.RecordCauseChainWait
		}
		record, err := product.FormatRecord(recordValues)
		if err != nil {
			return err
		}
		var comments []forge.ReviewComment
		addendum := ""
		if len(args) == 6 {
			requested, readErr := readRequestedComments(args[5])
			if readErr != nil {
				return readErr
			}
			var diagnostic string
			comments, addendum, diagnostic = anchorReviewComments(
				ctx,
				os.Getenv("MINOS_WORKSPACE"),
				guard.TargetSHA,
				guard.HeadSHA,
				requested,
			)
			if diagnostic != "" {
				fmt.Fprintf(os.Stderr, "forge review: %s\n", diagnostic)
			}
		}
		text := strings.TrimRight(string(body), "\r\n") + addendum + "\n\n" + record
		return emitForgeResult(stdout, "review", adapter.PostReview(ctx, guard, verdict, text, comments))
	case "comment":
		if len(args) != 4 {
			return fmt.Errorf("usage: minos forge comment HEAD TARGET FIX_REVIEW_FILE")
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		fixReview, err := readFixReview(args[3])
		if err != nil {
			return err
		}
		record, err := product.FormatRecord(map[string]string{"head": guard.HeadSHA, "target": guard.TargetSHA})
		if err != nil {
			return err
		}
		text := renderFixReview(fixReview) + "\n\n" + record
		return emitForgeResult(stdout, "comment", adapter.PostComment(ctx, guard, text))
	case "reaction":
		if len(args) != 4 {
			return fmt.Errorf("usage: minos forge reaction HEAD TARGET CONTENT")
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		return emitForgeResult(stdout, "reaction", adapter.AddReaction(ctx, guard, args[3]))
	case "reaction-remove":
		if len(args) != 4 {
			return fmt.Errorf("usage: minos forge reaction-remove HEAD TARGET CONTENT")
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		return emitForgeResult(stdout, "reaction-remove", adapter.RemoveReaction(ctx, guard, args[3]))
	case "label-remove":
		if len(args) != 4 {
			return fmt.Errorf("usage: minos forge label-remove HEAD TARGET LABEL")
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		return emitForgeResult(stdout, "label-remove", adapter.RemoveLabel(ctx, guard, args[3]))
	case "merge":
		if len(args) != 4 {
			return fmt.Errorf("usage: minos forge merge HEAD TARGET METHOD")
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		return emitForgeResult(stdout, "merge", adapter.Merge(ctx, guard, forge.MergeMethod(args[3])))
	case "delete-source-branch":
		if len(args) != 4 {
			return fmt.Errorf("usage: minos forge delete-source-branch HEAD TARGET BRANCH")
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		return emitForgeResult(stdout, "delete-source-branch", adapter.DeleteSourceBranch(ctx, guard, args[3]))
	default:
		return fmt.Errorf("unknown forge action %q", args[0])
	}
}

var memberForgeWriteActions = map[string]string{
	"status-member":               "status",
	"review-member":               "review",
	"comment-member":              "comment",
	"reaction-member":             "reaction",
	"reaction-remove-member":      "reaction-remove",
	"label-remove-member":         "label-remove",
	"merge-member":                "merge",
	"delete-source-branch-member": "delete-source-branch",
}

// memberAddressedForgeWrite selects a claimed grouped member before the
// ordinary guarded writer runs. The durable group guard binds that member to
// this primary run, so repository coordinates alone cannot widen its write
// authority to an arbitrary pull request.
func memberAddressedForgeWrite(args []string, guard forge.Guard) (forge.Guard, []string, error) {
	action, memberWrite := memberForgeWriteActions[args[0]]
	if !memberWrite {
		return guard, args, nil
	}
	if len(args) < 4 {
		return forge.Guard{}, nil, fmt.Errorf("usage: minos forge %s OWNER REPO NUMBER ...", args[0])
	}
	if args[1] != guard.Repository.Owner || args[2] != guard.Repository.Name {
		return forge.Guard{}, nil, fmt.Errorf("member is outside this run repository")
	}
	pullRequest, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil || pullRequest <= 0 {
		if err == nil {
			err = fmt.Errorf("must be positive")
		}
		return forge.Guard{}, nil, fmt.Errorf("member number: %w", err)
	}
	cfg, err := LoadServiceConfig(os.Getenv("MINOS_CONFIG"))
	if err != nil {
		return forge.Guard{}, nil, err
	}
	if pullRequest != guard.PullRequest {
		primary := Facts{Owner: guard.Repository.Owner, Repo: guard.Repository.Name, PR: strconv.FormatInt(guard.PullRequest, 10)}
		member := Facts{Owner: guard.Repository.Owner, Repo: guard.Repository.Name, PR: args[3]}
		binding, err := os.ReadFile(groupMemberGuardPath(cfg.Runs.Dir, UnitName(member)))
		if err != nil || strings.TrimSpace(string(binding)) != UnitName(primary) {
			return forge.Guard{}, nil, fmt.Errorf("member is not claimed by this run")
		}
	}
	guard.PullRequest = pullRequest
	return guard, append([]string{action}, args[4:]...), nil
}

func leadForge() (*forge.Adapter, forge.Guard, string, error) {
	cfg, err := LoadServiceConfig(os.Getenv("MINOS_CONFIG"))
	if err != nil {
		return nil, forge.Guard{}, "", err
	}
	forgeName := os.Getenv("MINOS_FORGE")
	adapter, err := newBehaviouralForge(cfg, forgeName)
	if err != nil {
		return nil, forge.Guard{}, "", err
	}
	pr, err := strconv.ParseInt(os.Getenv("MINOS_PR"), 10, 64)
	if err != nil {
		return nil, forge.Guard{}, "", fmt.Errorf("MINOS_PR: %w", err)
	}
	guard := forge.Guard{
		Repository:  forge.Repository{Owner: os.Getenv("MINOS_OWNER"), Name: os.Getenv("MINOS_REPO_NAME")},
		PullRequest: pr,
	}
	if guard.Repository.Owner == "" || guard.Repository.Name == "" {
		return nil, forge.Guard{}, "", fmt.Errorf("pull-request environment is incomplete")
	}
	return adapter, guard, cfg.Service.BotLogin, nil
}

func namedProductState(name string) (product.State, bool) {
	for _, state := range product.States() {
		if state.Name() == name {
			return state, true
		}
	}
	return product.State{}, false
}

func emitForgeResult(stdout io.Writer, operation string, result forge.WriteResult) error {
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return err
	}
	if result.Outcome != forge.WriteApplied {
		return fmt.Errorf("%s %s: %s", operation, result.Outcome, result.Reason)
	}
	return nil
}
