package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
)

// ForgeCommand gives the lead a small command surface for reading and updating
// the pull request it was started for.
func ForgeCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: minos forge snapshot|threads|comments|claim|status|summary|earlier|review|approve|marker|file-issue|filed-issues|repository-metadata|clone-credential")
	}
	// These repository reads need the run's forge but no pull-request coordinates.
	switch args[0] {
	case "filed-issues":
		if len(args) != 2 || !repositoryName.MatchString(args[1]) {
			return fmt.Errorf("usage: minos forge filed-issues REPOSITORY")
		}
		adapter, _, err := runForge()
		if err != nil {
			return err
		}
		owner, name, _ := strings.Cut(args[1], "/")
		issues, err := adapter.FiledIssues(ctx, forge.Repository{Owner: owner, Name: name})
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(issues)
	case "repository-metadata":
		if len(args) != 3 || !repositoryName.MatchString(args[1]+"/"+args[2]) {
			return fmt.Errorf("usage: minos forge repository-metadata OWNER REPO")
		}
		adapter, _, err := runForge()
		if err != nil {
			return err
		}
		metadata, err := adapter.RepositoryMetadata(ctx, forge.Repository{Owner: args[1], Name: args[2]})
		// The forge's answer is the caller's to word, so it travels as data.
		var lookup *forge.RepositoryLookupError
		if errors.As(err, &lookup) {
			if encodeErr := json.NewEncoder(stdout).Encode(map[string]int{"lookup_status": lookup.Status}); encodeErr != nil {
				return encodeErr
			}
		}
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(metadata)
	case "clone-credential":
		repository := forge.Repository{Owner: os.Getenv("MINOS_OWNER"), Name: os.Getenv("MINOS_REPO_NAME")}
		if len(args) == 3 && repositoryName.MatchString(args[1]+"/"+args[2]) {
			repository = forge.Repository{Owner: args[1], Name: args[2]}
		} else if len(args) != 1 {
			return fmt.Errorf("usage: minos forge clone-credential OWNER REPO")
		}
		adapter, _, err := runForge()
		if errors.Is(err, errEmptyForgeCredential) {
			if encodeErr := json.NewEncoder(stdout).Encode(map[string]string{"refusal": "empty-credential"}); encodeErr != nil {
				return encodeErr
			}
		}
		if err != nil {
			return err
		}
		credential, err := adapter.CloneCredential(ctx, repository)
		if err != nil {
			return err
		}
		// The secret's one destination: the caller's capture of stdout.
		return json.NewEncoder(stdout).Encode(credential)
	}
	adapter, guard, adaptationDirectory, err := leadForge()
	if err != nil {
		return err
	}
	switch args[0] {
	case "file-issue":
		if len(args) != 4 {
			return fmt.Errorf("usage: minos forge file-issue REPOSITORY TITLE_FILE BODY_FILE")
		}
		if !repositoryName.MatchString(args[1]) {
			return fmt.Errorf("filing repository must be owner/name")
		}
		owner, name, _ := strings.Cut(args[1], "/")
		title, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		body, err := os.ReadFile(args[3])
		if err != nil {
			return err
		}
		return emitForgeResult(stdout, "file-issue", adapter.FileIssue(ctx, forge.Repository{Owner: owner, Name: name}, string(title), string(body)))
	case "snapshot":
		if len(args) != 1 {
			return fmt.Errorf("usage: minos forge snapshot")
		}
		snapshot, err := adapter.Snapshot(ctx, guard.Repository, guard.PullRequest)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(snapshot)
	case "threads":
		if len(args) != 1 {
			return fmt.Errorf("usage: minos forge threads")
		}
		threads, err := adapter.ReviewThreads(ctx, guard.Repository, guard.PullRequest)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(threads)
	case "comments":
		if len(args) != 1 {
			return fmt.Errorf("usage: minos forge comments")
		}
		comments, err := adapter.PullRequestComments(ctx, guard.Repository, guard.PullRequest)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(comments)
	case "claim":
		if len(args) != 1 {
			return fmt.Errorf("usage: minos forge claim")
		}
		// The claim adaptation reads the in-flight form from MINOS_MARKERS
		// itself; reading it here first refuses a malformed value before
		// any forge write.
		if _, err := markersFromEnvironment(); err != nil {
			return err
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
		description := state.Description()
		result := adapter.SetProductStatusWithDescription(ctx, guard, state, description)
		if err := emitForgeResult(stdout, "status", result); err != nil {
			return err
		}
		recordAppliedStatus(args[3])
		if args[3] == "clean" || args[3] == "attention" {
			recordReviewedChange(ctx, guard, args[3])
		}
		return nil
	case "summary":
		if len(args) != 5 {
			return fmt.Errorf("usage: minos forge summary HEAD TARGET inspecting|reviewing|clean|changes-requested|incomplete TEXT_FILE")
		}
		state, ok := product.NamedSummaryState(args[3])
		if !ok {
			return fmt.Errorf("unknown summary state %q", args[3])
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		text, err := os.ReadFile(args[4])
		if err != nil {
			return err
		}
		result, err := writeSummary(ctx, adapter, guard, state, string(text))
		if err != nil {
			return err
		}
		if err := emitForgeResult(stdout, "summary", result); err != nil {
			return err
		}
		if completion, finished := state.Completion(); finished {
			recordReviewedChange(ctx, guard, completion.Name())
		}
		return nil
	case "earlier":
		if len(args) != 3 || args[1] == "" || args[2] == "" {
			return fmt.Errorf("usage: minos forge earlier HEAD TARGET")
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		earlier, err := findEarlierHead(ctx, adapter, guard)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(earlier)
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
				readAnchoringCapabilities(adaptationDirectory),
			)
			if diagnostic != "" {
				fmt.Fprintf(os.Stderr, "forge review: %s\n", diagnostic)
			}
		}
		result, err := postReview(ctx, adapter, guard, verdict, strings.TrimRight(string(body), "\r\n")+addendum, comments)
		if err != nil {
			return err
		}
		return emitForgeResult(stdout, "review", result)
	case "approve":
		if len(args) != 3 || args[1] == "" || args[2] == "" {
			return fmt.Errorf("usage: minos forge approve HEAD TARGET")
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		snapshot, err := adapter.Snapshot(ctx, guard.Repository, guard.PullRequest)
		if err != nil {
			return err
		}
		if snapshot.HeadSHA != guard.HeadSHA {
			return emitForgeResult(stdout, "approve", forge.WriteResult{Outcome: forge.WriteRejected, Reason: "head moved"})
		}
		if !ownChangesRequested(snapshot) {
			return json.NewEncoder(stdout).Encode(map[string]string{"outcome": "not-needed", "reason": "Minos's latest verdict on this pull request does not request changes"})
		}
		result, err := postApproval(ctx, adapter, guard)
		if err != nil {
			return err
		}
		return emitForgeResult(stdout, "review", result)
	case "marker":
		if len(args) != 5 || (args[4] != "add" && args[4] != "remove") {
			return fmt.Errorf("usage: minos forge marker HEAD TARGET in-flight|clean|attention add|remove")
		}
		markers, err := markersFromEnvironment()
		if err != nil {
			return err
		}
		marker, known := markers.Roles()[args[3]]
		if !known {
			return fmt.Errorf("unknown marker role %q", args[3])
		}
		if marker == nil {
			return emitForgeResult(stdout, "marker", forge.WriteResult{Outcome: forge.WriteApplied, Reason: args[3] + " marker is not configured"})
		}
		guard.HeadSHA, guard.TargetSHA = args[1], args[2]
		if args[4] == "add" {
			return emitForgeResult(stdout, "marker", adapter.AddMarker(ctx, guard, *marker))
		}
		return emitForgeResult(stdout, "marker", adapter.RemoveMarker(ctx, guard, *marker))
	default:
		return fmt.Errorf("unknown forge action %q", args[0])
	}
}

// recordedOutcome names the terminal outcome the service's latest status on
// a commit states: "clean" or "attention" when that status is a completion
// marker, "none" otherwise. A run that ended incomplete, or never ran, reads
// as "none".
func recordedOutcome(ctx context.Context, adapter *forge.Adapter, repository forge.Repository, sha string) (string, error) {
	cfg, err := LoadServiceConfig(os.Getenv("MINOS_CONFIG"))
	if err != nil {
		return "", err
	}
	statuses, err := adapter.CommitStatuses(ctx, repository, sha)
	if err != nil {
		return "", err
	}
	latest, found := latestOwnedStatus(forge.Snapshot{Statuses: statuses}, cfg.BotLogin(os.Getenv("MINOS_FORGE")), cfg.Service.StatusContext)
	if !found {
		return "none", nil
	}
	state, terminal := product.CompletionMarker(string(latest.State), latest.Description)
	if !terminal {
		return "none", nil
	}
	return state.Name(), nil
}

// markersFromEnvironment reads the run's resolved markers from
// MINOS_MARKERS. The loader filled every default, so an absent, malformed or
// incomplete value is an error, never a reason to assume a form.
func markersFromEnvironment() (Markers, error) {
	raw, present := os.LookupEnv("MINOS_MARKERS")
	if !present {
		return Markers{}, fmt.Errorf("MINOS_MARKERS is required")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var markers Markers
	if err := decoder.Decode(&markers); err != nil {
		return Markers{}, fmt.Errorf("MINOS_MARKERS: %w", err)
	}
	// Anything after the object — another value, a stray bracket, text —
	// is malformed: only end of input passes.
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Markers{}, fmt.Errorf("MINOS_MARKERS: trailing data after the markers object")
	}
	if markers.InFlight == nil || markers.Clean == nil {
		return Markers{}, fmt.Errorf("MINOS_MARKERS: in-flight and clean markers are required")
	}
	if err := validateRepositoryKnobs("MINOS_MARKERS", RepositoryKnobs{Markers: markers}); err != nil {
		return Markers{}, err
	}
	return markers, nil
}

// leadForge builds the adapter and guard for the pull request this run serves,
// and returns the adaptation directory so the review boundary can read what
// that forge declares it can anchor.
func leadForge() (*forge.Adapter, forge.Guard, string, error) {
	adapter, adaptationDirectory, err := runForge()
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
	return adapter, guard, adaptationDirectory, nil
}

// runForge builds the adapter for the forge this run is configured against
// and returns its adaptation directory.
func runForge() (*forge.Adapter, string, error) {
	cfg, err := LoadServiceConfig(os.Getenv("MINOS_CONFIG"))
	if err != nil {
		return nil, "", err
	}
	forgeName := os.Getenv("MINOS_FORGE")
	adapter, err := newBehaviouralForge(cfg, forgeName)
	if err != nil {
		return nil, "", err
	}
	return adapter, cfg.Forges[forgeName].Adaptation, nil
}

// recordAppliedStatus keeps the last status this run applied to the forge
// in the run directory — one word, replaced on every applied write — so
// run-body can tell at exit whether the head is spent: clean and attention
// leave a completion marker on the head, any other last status leaves it
// eligible, and only a spent head's unit may succeed and trigger a sweep
// pass. The forge write is the durable effect and has already been
// reported; a record that cannot be written is said on stderr, and the
// run then exits as if its head were not spent — the direction that
// cannot loop. Outside a run there is no directory and nothing to record.
func recordAppliedStatus(state string) {
	runDir := os.Getenv("MINOS_RUN_DIR")
	if runDir == "" {
		return
	}
	path := filepath.Join(runDir, appliedStatusFile)
	staging := path + ".next"
	if err := os.WriteFile(staging, []byte(state+"\n"), 0o600); err == nil {
		err = os.Rename(staging, path)
		if err == nil {
			return
		}
		_ = os.Remove(staging)
		fmt.Fprintf(os.Stderr, "minos: the applied %s status could not be recorded at %s: %v; the run will exit as if its head were not spent\n", state, path, err)
		return
	} else {
		fmt.Fprintf(os.Stderr, "minos: the applied %s status could not be recorded at %s: %v; the run will exit as if its head were not spent\n", state, path, err)
	}
}

// appliedStatusFile is the run-directory file run-body reads at exit.
const appliedStatusFile = "forge-status"

func namedProductState(name string) (product.State, bool) {
	for _, state := range product.States() {
		if state.Name() == name {
			return state, true
		}
	}
	return product.State{}, false
}

// postReview signs and records a review body and posts it under the guard.
// The lead signs the review it authors; each comment already carries its
// proposing and verifying models from the publication composer, so the
// lead's identity goes on the body alone.
func postReview(ctx context.Context, adapter *forge.Adapter, guard forge.Guard, verdict forge.ReviewVerdict, text string, comments []forge.ReviewComment) (forge.WriteResult, error) {
	record, err := product.FormatRecord(map[string]string{"head": guard.HeadSHA, product.RecordTargetKey: guard.TargetSHA})
	if err != nil {
		return forge.WriteResult{}, err
	}
	if leadModel := os.Getenv("MINOS_LEAD_MODEL"); leadModel != "" {
		text += "\n\nReviewed by: `" + leadModel + "`."
	}
	return adapter.PostReview(ctx, guard, verdict, text+"\n\n"+record, comments), nil
}

// approvalAttempts and approvalRetryDelay bound how often, and how far
// apart, an approval whose write came back uncertain or retryable is
// tried again.
var (
	approvalAttempts   = 3
	approvalRetryDelay = 2 * time.Second
)

// postApproval posts the clean approval, trying again while the forge's
// answer leaves it uncertain or retryable. Such an approval may already
// have reached the forge, and an approving Minos review of the current
// head is a clean marker to the sweep, so giving up at the first such
// answer can leave the head showing approved under an incomplete status
// that no later run revisits. Trying again is safe: the guarded review
// write finds an identical review already posted and reports it applied
// without writing another. A rejection, a moved head among them, is final.
func postApproval(ctx context.Context, adapter *forge.Adapter, guard forge.Guard) (forge.WriteResult, error) {
	var result forge.WriteResult
	for attempt := 1; ; attempt++ {
		var err error
		result, err = postReview(ctx, adapter, guard, forge.ReviewApprove, cleanApprovalBody, nil)
		if err != nil {
			return result, err
		}
		if (result.Outcome != forge.WriteUncertain && result.Outcome != forge.WriteRetryable) || attempt >= approvalAttempts {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return result, nil
		case <-time.After(approvalRetryDelay):
		}
	}
}

// cleanApprovalBody is the approving review's whole message: it answers an
// earlier request for changes, and the summary comment carries the rest.
const cleanApprovalBody = "No blocking finding remains at this head."

// ownChangesRequested says whether the service's own latest verdict on the
// pull request requests changes. A clean head approves only then, so the
// forge's view of the pull request stops showing a block Minos no longer
// holds, and a pull request Minos never blocked gets no approval. Comment
// reviews carry no verdict and are passed over; a dismissed review ends the
// block it carried. Review ids rise with submission on both forges.
func ownChangesRequested(snapshot forge.Snapshot) bool {
	var latest *forge.Review
	for index := range snapshot.Reviews {
		review := &snapshot.Reviews[index]
		if review.User != snapshot.AuthenticatedUser {
			continue
		}
		switch strings.ToUpper(review.State) {
		case "REQUEST_CHANGES", "REQUESTED_CHANGES", "APPROVED", "APPROVE", "DISMISSED":
			if latest == nil || review.ID > latest.ID {
				latest = review
			}
		}
	}
	if latest == nil {
		return false
	}
	state := strings.ToUpper(latest.State)
	return state == "REQUEST_CHANGES" || state == "REQUESTED_CHANGES"
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
