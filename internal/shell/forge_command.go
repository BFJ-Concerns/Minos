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

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
)

// ForgeCommand gives the lead a small command surface for reading and updating
// the pull request it was started for.
func ForgeCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: minos forge snapshot|claim|status|review|marker|file-issue|repository-metadata|clone-credential")
	}
	// Repository metadata and the clone credential are not about the pull
	// request, so they need the run's forge but none of its coordinates.
	switch args[0] {
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
		if len(args) != 1 {
			return fmt.Errorf("usage: minos forge clone-credential")
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
		credential, err := adapter.CloneCredential(ctx)
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
		return nil
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
		record, err := product.FormatRecord(map[string]string{"head": guard.HeadSHA, product.RecordTargetKey: guard.TargetSHA})
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
		// The lead signs the review it authors; each comment already
		// carries its proposing and verifying models from the publication
		// composer, so the lead's identity goes on the body alone.
		if leadModel := os.Getenv("MINOS_LEAD_MODEL"); leadModel != "" {
			addendum += "\n\nReviewed by: `" + leadModel + "`."
		}
		text := strings.TrimRight(string(body), "\r\n") + addendum + "\n\n" + record
		return emitForgeResult(stdout, "review", adapter.PostReview(ctx, guard, verdict, text, comments))
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

func emitForgeResult(stdout io.Writer, operation string, result forge.WriteResult) error {
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return err
	}
	if result.Outcome != forge.WriteApplied {
		return fmt.Errorf("%s %s: %s", operation, result.Outcome, result.Reason)
	}
	return nil
}
