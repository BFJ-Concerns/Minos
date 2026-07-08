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
	"strings"
)

type Adaptation struct {
	Dir        string
	APIBase    string
	Credential string
}

type Status struct {
	Context string `json:"context"`
	State   string `json:"state"`
	Creator string `json:"creator"`
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
	if a.Dir == "" {
		return nil, fmt.Errorf("adaptation directory is not configured")
	}
	path := filepath.Join(a.Dir, name)
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = stdin
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "PUMP19_API_BASE="+a.APIBase)
	cmd.Env = append(cmd.Env, "PUMP19_FORGE_TOKEN="+a.Credential)
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

func (a Adaptation) NormaliseEvent(ctx context.Context, body []byte, headers map[string]string) (Facts, error) {
	env := make(map[string]string, len(headers))
	for key, value := range headers {
		env["PUMP19_HEADER_"+headerEnvName(key)] = value
	}
	out, err := a.Run(ctx, "normalise-event", bytes.NewReader(body), env)
	if err != nil {
		return Facts{}, err
	}
	return ParseFacts(bytes.NewReader(out))
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
	return statuses, nil
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

func (a Adaptation) PrepareWorkspace(ctx context.Context, facts Facts, workspace, diffPath string) error {
	_, err := a.Run(ctx, "prepare-workspace", nil, map[string]string{
		"PUMP19_WORKSPACE": workspace,
		"PUMP19_DIFF":      diffPath,
	}, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, facts.BaseRef)
	return err
}

func headerEnvName(header string) string {
	replacer := strings.NewReplacer("-", "_")
	return strings.ToUpper(replacer.Replace(header))
}

func statusForContext(statuses []Status, contextName string) (Status, bool) {
	for _, status := range statuses {
		if status.Context == contextName {
			return status, true
		}
	}
	return Status{}, false
}
