package shell

import (
	"bytes"
	"context"
	"io"
	"strings"

	"github.com/BFJ-Concerns/Minos/internal/forge"
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
	return (forge.ScriptRunner{Directory: a.Dir, APIBase: a.APIBase, Credential: a.Credential}).Run(ctx, forge.RunRequest{
		Operation: name, Arguments: args, Stdin: stdin, Env: extraEnv,
	})
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
