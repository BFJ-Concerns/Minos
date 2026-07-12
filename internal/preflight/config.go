package preflight

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Forge struct {
		APIBase              string   `toml:"api-base"`
		CredentialFile       string   `toml:"credential-file"`
		ExpectedLogin        string   `toml:"expected-login"`
		PermissionRepository string   `toml:"permission-repository"`
		RequiredPermissions  []string `toml:"required-permissions"`
	} `toml:"forge"`
	Engines  []EngineConfig `toml:"engine"`
	Registry struct {
		Enabled bool     `toml:"enabled"`
		Command string   `toml:"command"`
		Args    []string `toml:"args"`
	} `toml:"registry"`
	Alert struct {
		Directory string `toml:"directory"`
	} `toml:"alert"`
	Toolchain struct {
		Binaries []string `toml:"binaries"`
	} `toml:"toolchain"`
}

type EngineConfig struct {
	Name    string   `toml:"name"`
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
	Models  []string `toml:"models"`
}

func LoadConfig(path string) (Config, error) {
	var cfg Config
	metadata, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return Config{}, fmt.Errorf("%s: unknown TOML keys: %s", path, strings.Join(keys, ", "))
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func (cfg Config) validate() error {
	var missing []string
	require := func(name, value string) {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	require("forge.api-base", cfg.Forge.APIBase)
	require("forge.credential-file", cfg.Forge.CredentialFile)
	require("forge.expected-login", cfg.Forge.ExpectedLogin)
	require("forge.permission-repository", cfg.Forge.PermissionRepository)
	if len(cfg.Forge.RequiredPermissions) == 0 {
		missing = append(missing, "forge.required-permissions")
	}
	if len(cfg.Engines) == 0 {
		missing = append(missing, "engine")
	}
	for index, engine := range cfg.Engines {
		require(fmt.Sprintf("engine[%d].name", index), engine.Name)
		require(fmt.Sprintf("engine[%d].command", index), engine.Command)
		if len(engine.Models) == 0 {
			missing = append(missing, fmt.Sprintf("engine[%d].models", index))
		}
		if !argumentsContain(engine.Args, "{model}") {
			missing = append(missing, fmt.Sprintf("engine[%d].args must contain {model}", index))
		}
	}
	if cfg.Registry.Enabled {
		require("registry.command", cfg.Registry.Command)
	}
	require("alert.directory", cfg.Alert.Directory)
	if len(cfg.Toolchain.Binaries) == 0 {
		missing = append(missing, "toolchain.binaries")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

func argumentsContain(arguments []string, fragment string) bool {
	for _, argument := range arguments {
		if strings.Contains(argument, fragment) {
			return true
		}
	}
	return false
}
