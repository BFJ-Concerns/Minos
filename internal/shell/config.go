package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
		Dir           string `toml:"dir"`
		MaxConcurrent int    `toml:"max-concurrent"`
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
	Path            string `toml:"-"`
	serviceBotLogin string
	Forge           string `toml:"forge"`
	Owner           string `toml:"owner"`
	Repo            string `toml:"repo"`
	Adaptation      struct {
		Build   string `toml:"build"`
		Test    string `toml:"test"`
		Briefs  string `toml:"briefs"`
		Skill   string `toml:"skill"`
		RunBody string `toml:"run-body"`
	} `toml:"adaptation"`
	Policy struct {
		AutoMerge bool `toml:"auto-merge"`
	} `toml:"policy"`
	CI struct {
		RequiredChecks []string `toml:"required-checks"`
	} `toml:"ci"`
	Eligibility []EligibilityRule `toml:"eligibility"`
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
	path := filepath.Join(root, "service.toml")
	if err := decodeStrictTOML(path, &cfg); err != nil {
		return ServiceConfig{}, err
	}
	cfg.Root = root
	if err := validateServiceConfig(cfg); err != nil {
		return ServiceConfig{}, fmt.Errorf("%s: %w", path, err)
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
		if err := decodeStrictTOML(path, &repo); err != nil {
			return nil, err
		}
		repo.Path = path
		if err := validateRepoConfig(repo); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		repos = append(repos, repo)
	}
	return repos, nil
}

// decodeStrictTOML keeps configuration typos from quietly turning into zero
// values. BurntSushi/toml reports every key it could not map after decoding.
func decodeStrictTOML(path string, target any) error {
	metadata, err := toml.DecodeFile(path, target)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	undecoded := metadata.Undecoded()
	if len(undecoded) == 0 {
		return nil
	}
	keys := make([]string, 0, len(undecoded))
	for _, key := range undecoded {
		keys = append(keys, key.String())
	}
	sort.Strings(keys)
	return fmt.Errorf("%s: unknown TOML keys: %s", path, strings.Join(keys, ", "))
}

func validateServiceConfig(cfg ServiceConfig) error {
	var missing []string
	requireConfigValue(&missing, "service.bot-login", cfg.Service.BotLogin)
	requireConfigValue(&missing, "listener.bind", cfg.Listener.Bind)
	if len(cfg.Forges) == 0 {
		missing = append(missing, "forges")
	}
	forgeNames := make([]string, 0, len(cfg.Forges))
	for name := range cfg.Forges {
		forgeNames = append(forgeNames, name)
	}
	sort.Strings(forgeNames)
	for _, name := range forgeNames {
		forge := cfg.Forges[name]
		prefix := "forges." + name + "."
		requireConfigValue(&missing, prefix+"adaptation", forge.Adaptation)
		requireConfigValue(&missing, prefix+"api-base", forge.APIBase)
		requireConfigValue(&missing, prefix+"webhook-secret-file", forge.WebhookSecretFile)
		requireConfigValue(&missing, prefix+"credential-file", forge.CredentialFile)
	}
	requireConfigValue(&missing, "runs.dir", cfg.Runs.Dir)
	if cfg.Runs.MaxConcurrent <= 0 {
		missing = append(missing, "runs.max-concurrent")
	}
	if cfg.Sweep.LivenessThreshold.Duration <= 0 {
		missing = append(missing, "sweep.liveness-threshold")
	}
	return missingConfigError(missing)
}

func validateRepoConfig(repo RepoConfig) error {
	var missing []string
	requireConfigValue(&missing, "forge", repo.Forge)
	requireConfigValue(&missing, "owner", repo.Owner)
	requireConfigValue(&missing, "repo", repo.Repo)
	if len(repo.CI.RequiredChecks) == 0 {
		requireConfigValue(&missing, "adaptation.build", repo.Adaptation.Build)
		requireConfigValue(&missing, "adaptation.test", repo.Adaptation.Test)
	} else {
		for index, check := range repo.CI.RequiredChecks {
			requireConfigValue(&missing, fmt.Sprintf("ci.required-checks[%d]", index), check)
		}
	}
	requireConfigValue(&missing, "adaptation.skill", repo.Adaptation.Skill)
	if len(repo.Eligibility) == 0 {
		missing = append(missing, "eligibility")
	}
	for index, rule := range repo.Eligibility {
		if len(rule.Authors) == 0 {
			missing = append(missing, fmt.Sprintf("eligibility[%d].authors", index))
		}
	}
	return missingConfigError(missing)
}

func requireConfigValue(missing *[]string, name, value string) {
	if strings.TrimSpace(value) == "" {
		*missing = append(*missing, name)
	}
}

func missingConfigError(missing []string) error {
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing required fields: %s", strings.Join(missing, ", "))
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
	return RepoConfig{}, fmt.Errorf("%w: %s/%s on %s", errRepoNotOptedIn, facts.Owner, facts.Repo, facts.Forge)
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
