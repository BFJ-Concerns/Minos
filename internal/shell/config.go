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
		// Operator alerts are filed as issues on this repository (one open
		// issue per alert title, repeats as comments). All three keys unset
		// leaves alerting off and alerts as journal lines only.
		AlertForge string `toml:"alert-forge"`
		AlertOwner string `toml:"alert-owner"`
		AlertRepo  string `toml:"alert-repo"`
	} `toml:"service"`
	Listener struct {
		Bind string `toml:"bind"`
		// StatusTokenFile holds the bearer token a caller must present to
		// read the run status projection. Unset leaves the status route
		// unmounted, so a deployment gains the surface only once the
		// operator has installed a token for it.
		StatusTokenFile string `toml:"status-token-file"`
	} `toml:"listener"`
	Forges map[string]ForgeConfig `toml:"forges"`
	Runs   struct {
		Dir                    string `toml:"dir"`
		FailureLog             string `toml:"failure-log"`
		FailuresRepo           string `toml:"failures-repo"`
		FailuresCredentialFile string `toml:"failures-credential-file"`
		ArchiveCommand         string `toml:"archive-command"`
		// TimingsCommand assembles a run's timing record from the residue
		// the run has already written. Archiving invokes it at the end of a
		// run; the status projection invokes it mid-run for the same record.
		TimingsCommand string `toml:"timings-command"`
		// RecentTimingsCommand lists the timing sidecars archive-run has
		// delivered since a cutoff, so a finished run can still be reported
		// after its directory has been swept. Unset leaves the recent-runs
		// route answering with no runs.
		RecentTimingsCommand string `toml:"recent-timings-command"`
		// RecentTimingsCacheSeconds is how long a recent-runs listing stays
		// good for. The listing costs an SSH session to the archive host and
		// only changes when a run finishes, so this is what keeps a polling
		// operator surface from setting the session rate. Unset takes the
		// default below.
		RecentTimingsCacheSeconds int `toml:"recent-timings-cache-seconds"`
		MaxConcurrent             int `toml:"max-concurrent"`
	} `toml:"runs"`
	Ensemble struct {
		ConcurrencyClaude int `toml:"concurrency-claude"`
		ConcurrencyCodex  int `toml:"concurrency-codex"`
		// AgentCeiling caps a workflow's live agents across all engines
		// together, the same way the per-engine knobs cap each engine.
		// Zero leaves the runtime's own default in force.
		AgentCeiling int `toml:"agent-ceiling"`
	} `toml:"ensemble"`
}

// defaultRecentTimingsCacheSeconds keeps a recent-runs listing good for two
// minutes. Runs are minutes to hours apart, so this loses no reading an
// operator would notice while holding the archive host to one session per
// window per two minutes however often the surface polls.
const defaultRecentTimingsCacheSeconds = 120

// RecentTimingsCacheTTL is how long a recent-runs listing is served from
// memory before the archive host is asked again. An unset or nonsensical knob
// means the default, so a configuration written before the knob existed still
// gets the caching.
func (cfg ServiceConfig) RecentTimingsCacheTTL() time.Duration {
	seconds := cfg.Runs.RecentTimingsCacheSeconds
	if seconds < 1 {
		seconds = defaultRecentTimingsCacheSeconds
	}
	return time.Duration(seconds) * time.Second
}

// MaxConcurrentRuns is how many run units may be live at once. An unset knob
// means one, so a configuration written before the knob existed keeps the
// serialised behaviour it was written for.
func (cfg ServiceConfig) MaxConcurrentRuns() int {
	if cfg.Runs.MaxConcurrent < 1 {
		return 1
	}
	return cfg.Runs.MaxConcurrent
}

type ForgeConfig struct {
	Adaptation        string `toml:"adaptation"`
	APIBase           string `toml:"api-base"`
	WebhookSecretFile string `toml:"webhook-secret-file"`
	CredentialFile    string `toml:"credential-file"`
	SignatureHeader   string `toml:"signature-header"`
}

type RepoConfig struct {
	Path                         string   `toml:"-"`
	Forge                        string   `toml:"forge"`
	Owner                        string   `toml:"owner"`
	Repo                         string   `toml:"repo"`
	WorkInProgressBranchPrefixes []string `toml:"work-in-progress-branch-prefixes"`
	Adaptation                   struct {
		RunBody string `toml:"run-body"`
	} `toml:"adaptation"`
	Review struct {
		Threshold string `toml:"threshold"`
	} `toml:"review"`
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
	if cfg.Runs.MaxConcurrent < 0 || cfg.Runs.MaxConcurrent > runMemoryEnvelopeGiB {
		return ServiceConfig{}, fmt.Errorf("service.toml: runs max-concurrent must be between 1 and %d — beyond that the shared %dG run memory envelope cannot give each run a useful share", runMemoryEnvelopeGiB, runMemoryEnvelopeGiB)
	}
	if cfg.Ensemble.ConcurrencyClaude == 0 && cfg.Ensemble.ConcurrencyCodex == 0 {
		cfg.Ensemble.ConcurrencyClaude = 2
		cfg.Ensemble.ConcurrencyCodex = 2
	}
	if cfg.Ensemble.ConcurrencyClaude < 1 || cfg.Ensemble.ConcurrencyCodex < 1 {
		return ServiceConfig{}, fmt.Errorf("service.toml: ensemble concurrency-claude and concurrency-codex must be positive")
	}
	if cfg.Ensemble.AgentCeiling < 0 {
		return ServiceConfig{}, fmt.Errorf("service.toml: ensemble agent-ceiling must be positive when set")
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
		if repo.Review.Threshold == "" {
			repo.Review.Threshold = "High"
		}
		if repo.Forge == "" || repo.Owner == "" || repo.Repo == "" || repo.Adaptation.RunBody == "" {
			return nil, fmt.Errorf("%s: forge, owner, repo and adaptation.run-body are required", path)
		}
		if !validReviewThreshold(repo.Review.Threshold) {
			return nil, fmt.Errorf("%s: review.threshold must be Critical, High, Medium or Low", path)
		}
		for _, prefix := range repo.WorkInProgressBranchPrefixes {
			if prefix == "" {
				return nil, fmt.Errorf("%s: work-in-progress-branch-prefixes cannot contain an empty prefix", path)
			}
		}
		repos = append(repos, repo)
	}
	return repos, nil
}

func validReviewThreshold(value string) bool {
	switch value {
	case "Critical", "High", "Medium", "Low":
		return true
	default:
		return false
	}
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
