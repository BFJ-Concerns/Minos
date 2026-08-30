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
		return fmt.Errorf("usage: minos forge snapshot|claim|status|review|reaction|reaction-remove")
	}
	adapter, guard, _, err := leadForge()
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
	case "claim":
		if len(args) != 1 {
			return fmt.Errorf("usage: minos forge claim")
		}
		return emitForgeResult(stdout, "claim", adapter.Claim(ctx, guard.Repository, guard.PullRequest))
	case "status":
		if len(args) != 4 {
			return fmt.Errorf("usage: minos forge status HEAD TARGET working|attention|incomplete|clean|continuation")
		}
		state, ok := namedProductState(args[3])
		if !ok {
			return fmt.Errorf("unknown product state %q", args[3])
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		return emitForgeResult(stdout, "status", adapter.SetProductStatus(ctx, guard, state))
	case "review":
		if len(args) != 5 && len(args) != 6 {
			return fmt.Errorf("usage: minos forge review HEAD TARGET approve|request-changes|comment BODY_FILE [COMMENTS_FILE]")
		}
		verdict, ok := map[string]forge.ReviewVerdict{
			"approve": forge.ReviewApprove, "request-changes": forge.ReviewRequestChanges, "comment": forge.ReviewVerdictComment,
		}[args[3]]
		if !ok {
			return fmt.Errorf("unknown review verdict %q", args[3])
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		body, err := os.ReadFile(args[4])
		if err != nil {
			return err
		}
		record, err := product.FormatRecord(map[string]string{"head": guard.HeadSHA, "target": guard.TargetSHA})
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
	default:
		return fmt.Errorf("unknown forge action %q", args[0])
	}
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
