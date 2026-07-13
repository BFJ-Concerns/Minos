package shell

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"bfj/minos/internal/ledger"
)

// Reads are the deliberately small exception. An organisation may add a new
// mutating adaptation without changing the service; treating unknown verbs as
// writes keeps replay safety conservative when that happens.
var forgeReadOperations = map[string]bool{
	"list-open-prs":     true,
	"normalise-event":   true,
	"prepare-workspace": true,
}

var runClaimMutationOperations = map[string]bool{
	"add-reaction":    true,
	"remove-reaction": true,
}

type Adaptation struct {
	Dir        string
	APIBase    string
	Credential string
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
// seam. Calling the same adaptation verb through Run still takes the ordinary
// tracked path above.
func (a Adaptation) runClaimMutation(ctx context.Context, name string, args ...string) ([]byte, error) {
	if !runClaimMutationOperations[name] {
		return nil, fmt.Errorf("adaptation operation %q is not run-claim state", name)
	}
	// Eyes represent the current token-owned lease, not the immutable pair at
	// which the process was launched. An authorised ledger advance must therefore
	// carry claim and release without weakening ordinary forge mutations.
	if err := guardRunClaimMutation(ctx); err != nil {
		return nil, fmt.Errorf("fence forge mutation %s: %w", name, err)
	}
	return a.runUnchecked(ctx, name, nil, nil, args...)
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
	return a.runUnchecked(ctx, name, stdin, extraEnv, args...)
}

func (a Adaptation) runUnchecked(ctx context.Context, name string, stdin io.Reader, extraEnv map[string]string, args ...string) ([]byte, error) {
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
	return guardAttemptMutation(ctx, true)
}

func guardRunClaimMutation(ctx context.Context) error {
	return guardAttemptMutation(ctx, false)
}

func guardAttemptMutation(ctx context.Context, requireLaunchPair bool) error {
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
	lease, err := ownedLease(ctx, store, coordinationKey(facts), token)
	if err != nil {
		return err
	}
	if requireLaunchPair && (lease.ObservedHead != facts.HeadSHA || lease.ObservedTarget != os.Getenv("MINOS_TARGET_SHA")) {
		return ledger.ErrNotOwner
	}
	return nil
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
