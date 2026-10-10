package shell

import (
	"errors"
	"fmt"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

// errEmptyForgeCredential is a configured credential file with nothing in
// it: no forge can authenticate with that.
var errEmptyForgeCredential = errors.New("forge credential file is empty")

func newBehaviouralForge(cfg ServiceConfig, forgeName string) (*forge.Adapter, error) {
	configuration, ok := cfg.Forges[forgeName]
	if !ok {
		return nil, fmt.Errorf("unknown forge %q", forgeName)
	}
	return newBehaviouralForgeFromConfig(cfg, forgeName, configuration)
}

func newBehaviouralForgeFromConfig(cfg ServiceConfig, forgeName string, configuration ForgeConfig) (*forge.Adapter, error) {
	credential, err := ReadSecret(configuration.CredentialFile)
	if err != nil {
		return nil, err
	}
	if credential == "" {
		return nil, errEmptyForgeCredential
	}
	return forge.NewAdapter(forge.ScriptRunner{
		Directory: configuration.Adaptation, APIBase: configuration.APIBase, WebBase: configuration.WebBase, Credential: credential,
	}, cfg.BotLogin(forgeName), cfg.Service.StatusContext)
}
