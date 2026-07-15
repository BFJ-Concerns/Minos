package shell

import (
	"bytes"
	"context"
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

func NewAdaptation(forge ForgeConfig) (Adaptation, error) {
	credential, err := ReadSecret(forge.CredentialFile)
	if err != nil {
		return Adaptation{}, err
	}
	return Adaptation{Dir: forge.Adaptation, APIBase: forge.APIBase, Credential: credential}, nil
}

func (a Adaptation) Run(ctx context.Context, name string, stdin io.Reader, extraEnv map[string]string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, filepath.Join(a.Dir, name), args...)
	cmd.Stdin = stdin
	cmd.Env = append(os.Environ(), "MINOS_API_BASE="+a.APIBase, "MINOS_FORGE_TOKEN="+a.Credential)
	for key, value := range extraEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (a Adaptation) NormaliseEvent(ctx context.Context, body []byte, headers map[string]string) (Facts, error) {
	env := make(map[string]string, len(headers))
	for key, value := range headers {
		env["MINOS_HEADER_"+strings.ToUpper(strings.ReplaceAll(key, "-", "_"))] = value
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
	var facts []Facts
	for _, block := range strings.Split(strings.TrimSpace(string(out)), "\n\n") {
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
