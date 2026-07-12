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
	"strings"
)

var adaptationOperation = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Reads are the deliberately small exception. An organisation may add a new
// mutating adaptation without changing the service; treating unknown verbs as
// writes keeps replay safety conservative when that happens.
var forgeReadOperations = map[string]bool{
	"get-pr-facts":         true,
	"get-combined-status":  true,
	"get-statuses":         true,
	"label-actor":          true,
	"latest-label-event":   true,
	"list-open-prs":        true,
	"list-review-comments": true,
	"list-reviews":         true,
	"normalise-event":      true,
	"prepare-workspace":    true,
}

var runClaimMutationOperations = map[string]bool{
	"add-label":         true,
	"add-reaction":      true,
	"assign-if-missing": true,
	"remove-label":      true,
	"remove-reaction":   true,
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
	ID      int64  `json:"id"`
	Context string `json:"context"`
	State   string `json:"state"`
	Creator string `json:"creator"`
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

type LabelEvent struct {
	Action string
	Label  string
	Actor  string
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
	return a.run(ctx, name, stdin, extraEnv, true, args...)
}

// runClaimMutation is reserved for the service's idempotent claim/release
// seam. A mission invoking the same adaptation verb through minos adapt still
// takes the ordinary tracked path above.
func (a Adaptation) runClaimMutation(ctx context.Context, name string, args ...string) ([]byte, error) {
	if !runClaimMutationOperations[name] {
		return nil, fmt.Errorf("adaptation operation %q is not run-claim state", name)
	}
	return a.run(ctx, name, nil, nil, false, args...)
}

func (a Adaptation) runUntrackedMutation(ctx context.Context, name string, stdin io.Reader, extraEnv map[string]string, args ...string) ([]byte, error) {
	if !untrackedMutationOperations[name] {
		return nil, fmt.Errorf("adaptation operation %q is not an untracked mutation", name)
	}
	return a.run(ctx, name, stdin, extraEnv, false, args...)
}

func (a Adaptation) run(ctx context.Context, name string, stdin io.Reader, extraEnv map[string]string, trackMutation bool, args ...string) ([]byte, error) {
	if a.Dir == "" {
		return nil, fmt.Errorf("adaptation directory is not configured")
	}
	if trackMutation && !forgeReadOperations[name] {
		// This record must become durable before the adaptation can make a forge
		// mutation. A later non-zero exit is replayable only when it is absent.
		if err := writeForgeWritesAttempted(os.Getenv("MINOS_RUN_DIR"), name); err != nil {
			return nil, fmt.Errorf("record forge write attempt for %s: %w", name, err)
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

func (a Adaptation) AddLabel(ctx context.Context, owner, repo, pr, label string) error {
	_, err := a.Run(ctx, "add-label", nil, nil, owner, repo, pr, label)
	return err
}

func (a Adaptation) RemoveLabel(ctx context.Context, owner, repo, pr, label string) error {
	_, err := a.Run(ctx, "remove-label", nil, nil, owner, repo, pr, label)
	return err
}

func (a Adaptation) addRunClaimLabel(ctx context.Context, owner, repo, pr, label string) error {
	_, err := a.runClaimMutation(ctx, "add-label", owner, repo, pr, label)
	return err
}

func (a Adaptation) removeRunClaimLabel(ctx context.Context, owner, repo, pr, label string) error {
	_, err := a.runClaimMutation(ctx, "remove-label", owner, repo, pr, label)
	return err
}

func (a Adaptation) addRunClaimReaction(ctx context.Context, owner, repo, pr, reaction string) error {
	_, err := a.runClaimMutation(ctx, "add-reaction", owner, repo, pr, reaction)
	return err
}

func (a Adaptation) removeRunClaimReaction(ctx context.Context, owner, repo, pr, reaction string) error {
	_, err := a.runClaimMutation(ctx, "remove-reaction", owner, repo, pr, reaction)
	return err
}

func (a Adaptation) assignRunClaimIfMissing(ctx context.Context, owner, repo, pr, login string) error {
	_, err := a.runClaimMutation(ctx, "assign-if-missing", owner, repo, pr, login)
	return err
}

func (a Adaptation) SetStatus(ctx context.Context, owner, repo, sha, contextName, state, description string) error {
	_, err := a.Run(ctx, "set-status", nil, nil, owner, repo, sha, contextName, state, description)
	return err
}

func (a Adaptation) LabelActor(ctx context.Context, owner, repo, pr, label string) (string, error) {
	out, err := a.Run(ctx, "label-actor", nil, nil, owner, repo, pr, label)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (a Adaptation) LatestLabelEvent(ctx context.Context, owner, repo, pr string) (LabelEvent, error) {
	out, err := a.Run(ctx, "latest-label-event", nil, nil, owner, repo, pr)
	if err != nil {
		return LabelEvent{}, err
	}
	values, err := parseKeyValues(bytes.NewReader(out))
	if err != nil {
		return LabelEvent{}, err
	}
	return LabelEvent{
		Action: values["ACTION"],
		Label:  values["LABEL"],
		Actor:  values["ACTOR"],
	}, nil
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
