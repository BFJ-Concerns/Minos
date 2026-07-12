package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"bfj/minos/internal/ledger"
)

var adaptationOperation = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Reads are the deliberately small exception. An organisation may add a new
// mutating adaptation without changing the service; treating unknown verbs as
// writes keeps replay safety conservative when that happens.
var forgeReadOperations = map[string]bool{
	"get-pr-facts":         true,
	"get-combined-status":  true,
	"get-statuses":         true,
	"list-open-prs":        true,
	"list-review-comments": true,
	"list-reviews":         true,
	"normalise-event":      true,
	"prepare-workspace":    true,
}

var runClaimMutationOperations = map[string]bool{
	"add-reaction":    true,
	"remove-reaction": true,
}

// Find ingest is deliberately outside the PR publication boundary. A retry may
// append the same advisory entry twice, but it must not turn a later transient
// review failure into a terminal body-exit-after-forge-write latch.
var untrackedMutationOperations = map[string]bool{
	"append-findings": true,
}

type Adaptation struct {
	Dir        string
	APIBase    string
	Credential string
}

type Status struct {
	ID          int64  `json:"id"`
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description"`
	Creator     string `json:"creator"`
}

// Review is the machine-checkable part of a forge review. The sweep deliberately
// ignores Body: prose is review substance, never reconciliation state.
type Review struct {
	ID       int64  `json:"id"`
	State    string `json:"state"`
	CommitID string `json:"commit_id"`
	User     string `json:"user"`
}

type CombinedStatus struct {
	State string `json:"state"`
}

func NewAdaptation(forge ForgeConfig) (Adaptation, error) {
	credential := ""
	if forge.CredentialFile != "" {
		value, err := ReadSecret(forge.CredentialFile)
		if err != nil {
			return Adaptation{}, err
		}
		credential = value
	}
	return Adaptation{Dir: forge.Adaptation, APIBase: forge.APIBase, Credential: credential}, nil
}

func (a Adaptation) Run(ctx context.Context, name string, stdin io.Reader, extraEnv map[string]string, args ...string) ([]byte, error) {
	return a.run(ctx, name, stdin, extraEnv, args...)
}

// runClaimMutation is reserved for the service's idempotent claim/release
// seam. A mission invoking the same adaptation verb through minos adapt still
// takes the ordinary tracked path above.
func (a Adaptation) runClaimMutation(ctx context.Context, name string, args ...string) ([]byte, error) {
	if !runClaimMutationOperations[name] {
		return nil, fmt.Errorf("adaptation operation %q is not run-claim state", name)
	}
	return a.run(ctx, name, nil, nil, args...)
}

func (a Adaptation) runUntrackedMutation(ctx context.Context, name string, stdin io.Reader, extraEnv map[string]string, args ...string) ([]byte, error) {
	if !untrackedMutationOperations[name] {
		return nil, fmt.Errorf("adaptation operation %q is not an untracked mutation", name)
	}
	return a.run(ctx, name, stdin, extraEnv, args...)
}

func (a Adaptation) run(ctx context.Context, name string, stdin io.Reader, extraEnv map[string]string, args ...string) ([]byte, error) {
	if a.Dir == "" {
		return nil, fmt.Errorf("adaptation directory is not configured")
	}
	if !forgeReadOperations[name] {
		// Agent-owned mutations are fenced at the common adaptation dispatch, so
		// adding a new script cannot accidentally bypass stale-attempt rejection.
		if err := guardAdaptationMutation(ctx); err != nil {
			return nil, fmt.Errorf("fence forge mutation %s: %w", name, err)
		}
	}
	path := filepath.Join(a.Dir, name)
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = stdin
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "MINOS_API_BASE="+a.APIBase)
	cmd.Env = append(cmd.Env, "MINOS_FORGE_TOKEN="+a.Credential)
	for key, value := range extraEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func guardAdaptationMutation(ctx context.Context) error {
	value := os.Getenv("MINOS_ATTEMPT_TOKEN")
	if value == "" {
		// Receiver/sweep-owned guarded operations do not belong to a lifecycle
		// token. Their operation-specific expected-head guard is the authority.
		return nil
	}
	token, err := strconv.ParseInt(value, 10, 64)
	if err != nil || token <= 0 {
		return fmt.Errorf("invalid attempt token %q", value)
	}
	cfg, err := LoadServiceConfig(os.Getenv("MINOS_CONFIG"))
	if err != nil {
		return err
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		return err
	}
	defer store.Close()
	facts := envFacts(os.Getenv("MINOS_FORGE"))
	lease, found, err := store.Lease(ctx, coordinationKey(facts))
	if err != nil {
		return err
	}
	if !found || lease.Token != token || lease.ObservedHead != facts.HeadSHA || lease.ObservedTarget != os.Getenv("MINOS_TARGET_SHA") {
		return ledger.ErrNotOwner
	}
	return nil
}

// AdaptCommand gives the accountable session a credentialled route to the
// configured, service-owned adaptation scripts. The operation is a basename,
// never a caller-selected path into the host filesystem.
func AdaptCommand(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || !adaptationOperation.MatchString(args[0]) {
		return fmt.Errorf("usage: minos adapt OPERATION [ARG...]")
	}
	cfg, err := LoadServiceConfig(os.Getenv("MINOS_CONFIG"))
	if err != nil {
		return err
	}
	forgeName := os.Getenv("MINOS_FORGE")
	forge, ok := cfg.Forges[forgeName]
	if !ok {
		return fmt.Errorf("unknown forge %q", forgeName)
	}
	adaptation, err := NewAdaptation(forge)
	if err != nil {
		return err
	}
	extraEnv := map[string]string(nil)
	if args[0] == "append-findings" {
		repo, err := FindRepoConfig(cfg.Root, envFacts(forgeName))
		if err != nil {
			return err
		}
		if repo.FindIngest == nil {
			_, err = io.WriteString(stdout, "unconfigured\n")
			return err
		}
		extraEnv = map[string]string{
			"MINOS_FIND_INGEST_REPOSITORY": repo.FindIngest.Repository,
			"MINOS_FIND_INGEST_PATH":       repo.FindIngest.Path,
		}
	}
	var out []byte
	if untrackedMutationOperations[args[0]] {
		out, err = adaptation.runUntrackedMutation(ctx, args[0], stdin, extraEnv, args[1:]...)
	} else {
		out, err = adaptation.Run(ctx, args[0], stdin, extraEnv, args[1:]...)
	}
	if err != nil {
		return err
	}
	_, err = stdout.Write(out)
	return err
}

func (a Adaptation) NormaliseEvent(ctx context.Context, body []byte, headers map[string]string) (Facts, error) {
	env := make(map[string]string, len(headers))
	for key, value := range headers {
		env["MINOS_HEADER_"+headerEnvName(key)] = value
	}
	out, err := a.Run(ctx, "normalise-event", bytes.NewReader(body), env)
	if err != nil {
		return Facts{}, err
	}
	return ParseFactsAllowUnmapped(bytes.NewReader(out))
}

func (a Adaptation) GetPRFacts(ctx context.Context, forge, owner, repo, pr string) (Facts, error) {
	out, err := a.Run(ctx, "get-pr-facts", nil, nil, owner, repo, pr)
	if err != nil {
		return Facts{}, err
	}
	facts, err := ParseFacts(bytes.NewReader(out))
	if err != nil {
		return Facts{}, err
	}
	facts.Forge = forge
	return facts, nil
}

func (a Adaptation) GetStatuses(ctx context.Context, owner, repo, sha string) ([]Status, error) {
	out, err := a.Run(ctx, "get-statuses", nil, nil, owner, repo, sha)
	if err != nil {
		return nil, err
	}
	var statuses []Status
	if err := json.Unmarshal(out, &statuses); err != nil {
		return nil, err
	}
	for _, status := range statuses {
		// Forgejo 14 orders status writes with its monotonic status ID. Without
		// that field there is no safe way to choose the latest write in a context.
		if status.ID <= 0 {
			return nil, fmt.Errorf("get-statuses returned status without a positive id")
		}
	}
	return statuses, nil
}

func (a Adaptation) GetCombinedStatus(ctx context.Context, owner, repo, sha string) (string, error) {
	out, err := a.Run(ctx, "get-combined-status", nil, nil, owner, repo, sha)
	if err != nil {
		return "", err
	}
	var status CombinedStatus
	if err := json.Unmarshal(out, &status); err != nil {
		return "", err
	}
	if strings.TrimSpace(status.State) == "" {
		return "", fmt.Errorf("get-combined-status returned no state")
	}
	return status.State, nil
}

func (a Adaptation) ListReviews(ctx context.Context, owner, repo, pr string) ([]Review, error) {
	out, err := a.Run(ctx, "list-reviews", nil, nil, owner, repo, pr)
	if err != nil {
		return nil, err
	}
	var reviews []Review
	if err := json.Unmarshal(out, &reviews); err != nil {
		return nil, err
	}
	for _, review := range reviews {
		if review.ID <= 0 || review.State == "" || review.CommitID == "" || review.User == "" {
			return nil, fmt.Errorf("list-reviews returned incomplete machine review")
		}
	}
	return reviews, nil
}

func (a Adaptation) ListOpenPRs(ctx context.Context, forge, owner, repo string) ([]Facts, error) {
	out, err := a.Run(ctx, "list-open-prs", nil, nil, owner, repo)
	if err != nil {
		return nil, err
	}
	blocks := strings.Split(strings.TrimSpace(string(out)), "\n\n")
	var facts []Facts
	for _, block := range blocks {
		if strings.TrimSpace(block) == "" {
			continue
		}
		parsed, err := ParseFacts(strings.NewReader(block))
		if err != nil {
			return nil, err
		}
		parsed.Forge = forge
		facts = append(facts, parsed)
	}
	return facts, nil
}

func (a Adaptation) addRunClaimReaction(ctx context.Context, owner, repo, pr, reaction string) error {
	_, err := a.runClaimMutation(ctx, "add-reaction", owner, repo, pr, reaction)
	return err
}

func (a Adaptation) removeRunClaimReaction(ctx context.Context, owner, repo, pr, reaction string) error {
	_, err := a.runClaimMutation(ctx, "remove-reaction", owner, repo, pr, reaction)
	return err
}

func (a Adaptation) SetStatus(ctx context.Context, owner, repo, sha, contextName, state, description string) error {
	_, err := a.Run(ctx, "set-status", nil, nil, owner, repo, sha, contextName, state, description)
	return err
}

func (a Adaptation) PrepareWorkspace(ctx context.Context, facts Facts, workspace, diffPath string) error {
	_, err := a.Run(ctx, "prepare-workspace", nil, map[string]string{
		"MINOS_WORKSPACE": workspace,
		"MINOS_DIFF":      diffPath,
	}, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, facts.BaseRef)
	return err
}

func headerEnvName(header string) string {
	replacer := strings.NewReplacer("-", "_")
	return strings.ToUpper(replacer.Replace(header))
}

func statusForContext(statuses []Status, contextName string) (Status, bool) {
	var newest Status
	found := false
	for _, status := range statuses {
		if status.Context != contextName {
			continue
		}
		if !found || statusNewer(status, newest) {
			newest = status
			found = true
		}
	}
	return newest, found
}

func statusNewer(left, right Status) bool {
	return left.ID > right.ID
}
