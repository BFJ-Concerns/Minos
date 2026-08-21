package shell

import (
	"fmt"

	"bfj/minos/internal/forge"
)

func newBehaviouralForge(cfg ServiceConfig, forgeName string) (*forge.Adapter, error) {
	configuration, ok := cfg.Forges[forgeName]
	if !ok {
		return nil, fmt.Errorf("unknown forge %q", forgeName)
	}
	credential, err := ReadSecret(configuration.CredentialFile)
	if err != nil {
		return nil, err
	}
	return forge.NewAdapter(forge.ScriptRunner{
		Directory: configuration.Adaptation, APIBase: configuration.APIBase, Credential: credential,
		EnvStamp: currentEnvironmentStamp(cfg),
	}, cfg.Service.BotLogin)
}
