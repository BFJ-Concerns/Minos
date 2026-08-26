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
		return fmt.Errorf("usage: minos forge snapshot|head-movement|check-logs|claim|status|review|comment|reaction|reaction-remove|label-remove|merge|delete-source-branch")
	}
	member := forge.Repository{}
	var memberPR int64
	if args[0] == "--member" {
		if len(args) < 5 {
			return fmt.Errorf("usage: minos forge --member OWNER REPO NUMBER ACTION ...")
		}
		parsed, parseErr := strconv.ParseInt(args[3], 10, 64)
		if parseErr != nil || parsed < 1 {
			return fmt.Errorf("member pull-request number %q is invalid", args[3])
		}
		member, memberPR, args = forge.Repository{Owner: args[1], Name: args[2]}, parsed, args[4:]
		if member.Owner == "" || member.Name == "" {
			return fmt.Errorf("member coordinates are incomplete")
		}
		if len(args) == 0 {
			return fmt.Errorf("usage: minos forge --member OWNER REPO NUMBER ACTION ...")
		}
	}
	adapter, guard, botLogin, err := leadForge(member, memberPR)
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
			return fmt.Errorf("usage: minos forge review HEAD TARGET approve|request-changes|request-changes-checks|comment BODY_FILE [COMMENTS_FILE]")
		}
		checkCaused := args[3] == "request-changes-checks"
		verdict, ok := map[string]forge.ReviewVerdict{
			"approve": forge.ReviewApprove, "request-changes": forge.ReviewRequestChanges,
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

func leadForge(member forge.Repository, memberPR int64) (*forge.Adapter, forge.Guard, string, error) {
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
	if memberPR != 0 {
		guard.Repository = member
		guard.PullRequest = memberPR
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
