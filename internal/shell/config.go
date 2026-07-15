package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const DefaultConfigRoot = "/etc/minos"

var errRepoNotOptedIn = errors.New("repository is not opted in")

type ServiceConfig struct {
	Root    string `toml:"-"`
	Service struct {
		BotLogin string `toml:"bot-login"`
	} `toml:"service"`
	Listener struct {
		Bind string `toml:"bind"`
	} `toml:"listener"`
	Forges map[string]ForgeConfig `toml:"forges"`
	Runs   struct {
		Dir string `toml:"dir"`
	} `toml:"runs"`
}

type ForgeConfig struct {
	Adaptation        string `toml:"adaptation"`
	APIBase           string `toml:"api-base"`
	WebhookSecretFile string `toml:"webhook-secret-file"`
	CredentialFile    string `toml:"credential-file"`
	SignatureHeader   string `toml:"signature-header"`
}

type RepoConfig struct {
	Path       string `toml:"-"`
	Forge      string `toml:"forge"`
	Owner      string `toml:"owner"`
	Repo       string `toml:"repo"`
	Adaptation struct {
		Build   string `toml:"build"`
		Test    string `toml:"test"`
		RunBody string `toml:"run-body"`
	} `toml:"adaptation"`
	Policy struct {
		AutoMerge bool `toml:"auto-merge"`
	} `toml:"policy"`
}

func LoadServiceConfig(root string) (ServiceConfig, error) {
	if root == "" {
		root = DefaultConfigRoot
	}
	var cfg ServiceConfig
	if err := decodeStrictTOML(filepath.Join(root, "service.toml"), &cfg); err != nil {
		return ServiceConfig{}, err
	}
	cfg.Root = root
	if cfg.Service.BotLogin == "" || cfg.Listener.Bind == "" || cfg.Runs.Dir == "" || len(cfg.Forges) == 0 {
		return ServiceConfig{}, fmt.Errorf("service.toml: bot login, listener, runs directory and at least one forge are required")
	}
	for name, forge := range cfg.Forges {
		if forge.Adaptation == "" || forge.APIBase == "" || forge.WebhookSecretFile == "" || forge.CredentialFile == "" {
			return ServiceConfig{}, fmt.Errorf("service.toml: forge %s is incomplete", name)
		}
	}
	return cfg, nil
}

func LoadRepoConfigs(root string) ([]RepoConfig, error) {
	paths, err := filepath.Glob(filepath.Join(root, "repos", "*.toml"))
	if err != nil {
		return nil, err
	}
	repos := make([]RepoConfig, 0, len(paths))
	for _, path := range paths {
		var repo RepoConfig
		if err := decodeStrictTOML(path, &repo); err != nil {
			return nil, err
		}
		repo.Path = path
		if repo.Forge == "" || repo.Owner == "" || repo.Repo == "" || repo.Adaptation.RunBody == "" {
			return nil, fmt.Errorf("%s: forge, owner, repo and adaptation.run-body are required", path)
		}
		repos = append(repos, repo)
	}
	return repos, nil
}

func FindRepoConfig(cfg ServiceConfig, facts Facts) (RepoConfig, error) {
	repos, err := LoadRepoConfigs(cfg.Root)
	if err != nil {
		return RepoConfig{}, err
	}
	for _, repo := range repos {
		if repo.Forge == facts.Forge && repo.Owner == facts.Owner && repo.Repo == facts.Repo {
			return repo, nil
		}
	}
	return RepoConfig{}, fmt.Errorf("%w: %s/%s on %s", errRepoNotOptedIn, facts.Owner, facts.Repo, facts.Forge)
}

func decodeStrictTOML(path string, target any) error {
	metadata, err := toml.DecodeFile(path, target)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return fmt.Errorf("%s: unknown TOML keys: %s", path, strings.Join(keys, ", "))
	}
	return nil
}

func ReadSecret(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
