package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

const DefaultConfigRoot = "/etc/pump19"

type ServiceConfig struct {
	Root     string `toml:"-"`
	Listener struct {
		Bind string `toml:"bind"`
	} `toml:"listener"`
	Forges map[string]ForgeConfig `toml:"forges"`
	Runs   struct {
		Dir string `toml:"dir"`
	} `toml:"runs"`
	Sweep struct {
		LivenessThreshold Duration `toml:"liveness-threshold"`
		Log               string   `toml:"log"`
	} `toml:"sweep"`
	Scrub struct {
		Vars []string `toml:"vars"`
	} `toml:"scrub"`
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
		Build  string `toml:"build"`
		Test   string `toml:"test"`
		Briefs string `toml:"briefs"`
		Skill  string `toml:"skill"`
	} `toml:"adaptation"`
	Policy struct {
		AutoMerge bool `toml:"auto-merge"`
	} `toml:"policy"`
	Triggers []TriggerRule `toml:"trigger"`
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

func LoadServiceConfig(root string) (ServiceConfig, error) {
	if root == "" {
		root = DefaultConfigRoot
	}
	var cfg ServiceConfig
	cfg.Root = root
	cfg.Listener.Bind = ":8919"
	cfg.Runs.Dir = "/var/lib/pump19/runs"
	cfg.Sweep.LivenessThreshold.Duration = time.Hour
	if _, err := toml.DecodeFile(filepath.Join(root, "service.toml"), &cfg); err != nil {
		return ServiceConfig{}, err
	}
	cfg.Root = root
	if cfg.Listener.Bind == "" {
		cfg.Listener.Bind = ":8919"
	}
	if cfg.Runs.Dir == "" {
		cfg.Runs.Dir = "/var/lib/pump19/runs"
	}
	if cfg.Sweep.LivenessThreshold.Duration == 0 {
		cfg.Sweep.LivenessThreshold.Duration = time.Hour
	}
	return cfg, nil
}

func LoadRepoConfigs(root string) ([]RepoConfig, error) {
	paths, err := filepath.Glob(filepath.Join(root, "repos", "*.toml"))
	if err != nil {
		return nil, err
	}
	var repos []RepoConfig
	for _, path := range paths {
		var repo RepoConfig
		if _, err := toml.DecodeFile(path, &repo); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		repo.Path = path
		repos = append(repos, repo)
	}
	return repos, nil
}

func FindRepoConfig(root string, facts Facts) (RepoConfig, error) {
	repos, err := LoadRepoConfigs(root)
	if err != nil {
		return RepoConfig{}, err
	}
	for _, repo := range repos {
		if repo.Forge == facts.Forge && repo.Owner == facts.Owner && repo.Repo == facts.Repo {
			return repo, nil
		}
	}
	return RepoConfig{}, fmt.Errorf("repository %s/%s on %s is not opted in", facts.Owner, facts.Repo, facts.Forge)
}

func ReadSecret(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return stringsTrimSpace(string(data)), nil
}

func stringsTrimSpace(value string) string {
	for len(value) > 0 {
		switch value[0] {
		case ' ', '\n', '\r', '\t':
			value = value[1:]
		default:
			goto right
		}
	}
right:
	for len(value) > 0 {
		last := value[len(value)-1]
		switch last {
		case ' ', '\n', '\r', '\t':
			value = value[:len(value)-1]
		default:
			return value
		}
	}
	return value
}
