package shell

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

const DefaultConfigRoot = "/etc/minos"

// defaultStatusContext is the status context a deployment that names none
// writes and reads.
const defaultStatusContext = "Minos"

var errRepoNotOptedIn = errors.New("repository is not opted in")

type ServiceConfig struct {
	Root    string `toml:"-"`
	Service struct {
		BotLogin string `toml:"bot-login"`
		// CommitAuthorName and CommitAuthorEmail sign every commit Minos
		// makes — a filed issue log, the failure ledger. Unset, the name is
		// the bot login and the email is that login at minos.invalid.
		CommitAuthorName  string `toml:"commit-author-name"`
		CommitAuthorEmail string `toml:"commit-author-email"`
		// StatusContext names every commit status Minos writes and the only
		// one the sweep reads as its completion marker. Unset, it is Minos.
		StatusContext string `toml:"status-context"`
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
	// Repositories holds the service-level value of every per-repository
	// knob; a repository's own file overrides any of them.
	Repositories RepositoryKnobs `toml:"repositories"`
	Runs         struct {
		Dir                    string `toml:"dir"`
		FailureLog             string `toml:"failure-log"`
		FailuresRepo           string `toml:"failures-repo"`
		FailuresForge          string `toml:"failures-forge"`
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
		MemoryEnvelopeGiB         int `toml:"memory-envelope-gib"`
		// DurationCeiling is normalised by the loader to systemd seconds.
		DurationCeiling          string `toml:"duration-ceiling"`
		PressureThresholdPercent int    `toml:"pressure-threshold-percent"`
	} `toml:"runs"`
	// Routing names, per workflow role, the engine, model and effort the
	// operator chose. A role left unset defaults at run time to the
	// cross-family pairing when both engines are provisioned and to the one
	// provisioned engine otherwise; only set roles are exported.
	Routing  Routing `toml:"routing"`
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
// means the default.
func (cfg ServiceConfig) RecentTimingsCacheTTL() time.Duration {
	seconds := cfg.Runs.RecentTimingsCacheSeconds
	if seconds < 1 {
		seconds = defaultRecentTimingsCacheSeconds
	}
	return time.Duration(seconds) * time.Second
}

const (
	defaultRunMemoryEnvelopeGiB        = 22
	defaultRunDurationCeiling          = "12h"
	defaultRunPressureThresholdPercent = 85
)

func (cfg ServiceConfig) runMemoryEnvelopeGiB() int {
	return cfg.Runs.MemoryEnvelopeGiB
}

func (cfg ServiceConfig) runDurationCeiling() string {
	return cfg.Runs.DurationCeiling
}

func (cfg ServiceConfig) runPressureThresholdPercent() int {
	return cfg.Runs.PressureThresholdPercent
}

// MaxConcurrentRuns is how many run units may be live at once. An unset knob
// means one: runs are serialised.
func (cfg ServiceConfig) MaxConcurrentRuns() int {
	if cfg.Runs.MaxConcurrent < 1 {
		return 1
	}
	return cfg.Runs.MaxConcurrent
}

// RoleRouting is one role's explicit routing; every field is optional, a
// model needing the engine that names its family.
type RoleRouting struct {
	Engine string `toml:"engine" json:"engine,omitempty"`
	Model  string `toml:"model" json:"model,omitempty"`
	Effort string `toml:"effort" json:"effort,omitempty"`
}

// Routing holds the five workflow roles by their configured names.
type Routing struct {
	Exploration    RoleRouting `toml:"exploration"`
	Proposer       RoleRouting `toml:"proposer"`
	Verifier       RoleRouting `toml:"verifier"`
	EngagementGate RoleRouting `toml:"engagement-gate"`
	BriefPlanner   RoleRouting `toml:"brief-planner"`
}

// Roles lists the set roles by name for export and validation; an unset role
// is absent so the run applies its default.
func (r Routing) Roles() map[string]RoleRouting {
	roles := map[string]RoleRouting{}
	for _, role := range []struct {
		name    string
		routing RoleRouting
	}{
		{"exploration", r.Exploration}, {"proposer", r.Proposer}, {"verifier", r.Verifier},
		{"engagement-gate", r.EngagementGate}, {"brief-planner", r.BriefPlanner},
	} {
		if role.routing != (RoleRouting{}) {
			roles[role.name] = role.routing
		}
	}
	return roles
}

var routingEngines = []string{"claude", "codex"}

// routingEfforts is the Ensemble runtime's canonical effort domain
// (CANONICAL_EFFORT_DOMAIN in runtime/ensemble.mjs); the run-time resolver
// in workflows/role-routing.mjs carries the same list.
var routingEfforts = []string{"minimal", "low", "medium", "high", "xhigh"}

func validateRouting(routing Routing) error {
	for name, role := range routing.Roles() {
		if role.Engine != "" && !slices.Contains(routingEngines, role.Engine) {
			return fmt.Errorf("service.toml: routing.%s.engine must be one of %s", name, strings.Join(routingEngines, ", "))
		}
		if role.Model != "" && role.Engine == "" {
			return fmt.Errorf("service.toml: routing.%s.model needs routing.%s.engine, which names the model's family", name, name)
		}
		if role.Effort != "" && !slices.Contains(routingEfforts, role.Effort) {
			return fmt.Errorf("service.toml: routing.%s.effort must be one of %s", name, strings.Join(routingEfforts, ", "))
		}
	}
	return nil
}

type ForgeConfig struct {
	Adaptation        string `toml:"adaptation"`
	APIBase           string `toml:"api-base"`
	WebBase           string `toml:"web-base"`
	WebhookSecretFile string `toml:"webhook-secret-file"`
	CredentialFile    string `toml:"credential-file"`
	SignatureHeader   string `toml:"signature-header"`
}

// RepositoryKnobs are the per-repository knobs. Each is set once at
// service level under [repositories] and any repository overrides it in its
// own file; the override replaces the whole value — a list never merges with
// the service list — and a knob unset at both levels takes its shipped
// default. Every knob is exported into the run by SpawnRun, so a run script
// or input builder reads the resolved value and never reads configuration.
type RepositoryKnobs struct {
	WorkInProgressBranchPrefixes []string `toml:"work-in-progress-branch-prefixes"`
	Review                       struct {
		Threshold string `toml:"threshold"`
	} `toml:"review"`
	// GuidanceSources is the ordered list of documents that carry the
	// reviewed project's declared intent. Empty means the reviewed
	// repository's own checked-in guidance.
	GuidanceSources []GuidanceSource `toml:"guidance-sources"`
	// FilingDestination is where a clean run's confirmed non-gating material
	// goes.
	FilingDestination FilingDestination `toml:"filing-destination"`
	// Markers is the form of each marker Minos writes on a pull request: a
	// reaction or a label each. In-flight and clean are always set; an unset
	// attention marker is never written.
	Markers Markers `toml:"markers"`
}

// Markers names the in-flight, clean and attention markers' forms.
type Markers struct {
	InFlight  *forge.Marker `toml:"in-flight" json:"in-flight,omitempty"`
	Clean     *forge.Marker `toml:"clean" json:"clean,omitempty"`
	Attention *forge.Marker `toml:"attention" json:"attention,omitempty"`
}

// Roles lists the markers by their configured names, set or not.
func (m Markers) Roles() map[string]*forge.Marker {
	return map[string]*forge.Marker{"in-flight": m.InFlight, "clean": m.Clean, "attention": m.Attention}
}

// GuidanceSource names one guidance document: a path in the reviewed
// repository, or a path in a named secondary repository on the same forge.
type GuidanceSource struct {
	Repository string `toml:"repository" json:"repository,omitempty"`
	Path       string `toml:"path" json:"path"`
}

// FilingDestination names where confirmed non-gating material is delivered:
// a file in a repository's default branch (the reviewed repository unless
// one is named), an issue on a named repository, a comment on the pull
// request, or nowhere.
type FilingDestination struct {
	Kind       string `toml:"kind" json:"kind"`
	Repository string `toml:"repository" json:"repository,omitempty"`
	Path       string `toml:"path" json:"path,omitempty"`
}

const (
	FilingKindFile               = "file"
	FilingKindIssue              = "issue"
	FilingKindPullRequestComment = "pull-request-comment"
	FilingKindNone               = "none"
)

var filingKinds = []string{FilingKindFile, FilingKindIssue, FilingKindPullRequestComment, FilingKindNone}

// repositoryName is the owner/name form a secondary repository takes: one
// slash, no whitespace, nothing that could read as a path or URL.
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type RepoConfig struct {
	Path       string `toml:"-"`
	Forge      string `toml:"forge"`
	Owner      string `toml:"owner"`
	Repo       string `toml:"repo"`
	Adaptation struct {
		RunBody string `toml:"run-body"`
	} `toml:"adaptation"`
	// The embedded knobs hold the repository's own file values while
	// loading and the resolved values afterwards, so consumers read
	// repo.Review.Threshold and friends without knowing which level set them.
	RepositoryKnobs
}

func LoadServiceConfig(root string) (ServiceConfig, error) {
	if root == "" {
		root = DefaultConfigRoot
	}
	var cfg ServiceConfig
	metadata, err := decodeStrictTOML(filepath.Join(root, "service.toml"), &cfg)
	if err != nil {
		return ServiceConfig{}, err
	}
	cfg.Root = root
	if cfg.Service.BotLogin == "" || cfg.Listener.Bind == "" || cfg.Runs.Dir == "" || len(cfg.Forges) == 0 {
		return ServiceConfig{}, fmt.Errorf("service.toml: bot login, listener, runs directory and at least one forge are required")
	}
	if cfg.Service.CommitAuthorName == "" {
		cfg.Service.CommitAuthorName = cfg.Service.BotLogin
	}
	if cfg.Service.CommitAuthorEmail == "" {
		cfg.Service.CommitAuthorEmail = cfg.Service.BotLogin + "@minos.invalid"
	}
	if !metadata.IsDefined("service", "status-context") {
		cfg.Service.StatusContext = defaultStatusContext
	}
	if cfg.Service.StatusContext == "" || strings.TrimSpace(cfg.Service.StatusContext) != cfg.Service.StatusContext || strings.ContainsAny(cfg.Service.StatusContext, "\x00\n\r") {
		return ServiceConfig{}, fmt.Errorf("service.toml: service status-context %q must be a non-empty single line without surrounding whitespace", cfg.Service.StatusContext)
	}
	if err := validateRepositoryKnobs("service.toml: repositories", cfg.Repositories); err != nil {
		return ServiceConfig{}, err
	}
	if err := validateRouting(cfg.Routing); err != nil {
		return ServiceConfig{}, err
	}
	for name, forge := range cfg.Forges {
		if forge.WebBase != "" {
			webURL, err := url.Parse(forge.WebBase)
			if err != nil || (webURL.Scheme != "http" && webURL.Scheme != "https") || webURL.Hostname() == "" {
				return ServiceConfig{}, fmt.Errorf("service.toml: forge %s web-base must be an absolute http(s) URL", name)
			}
		}
		if forge.Adaptation == "" || forge.APIBase == "" || forge.WebhookSecretFile == "" || forge.CredentialFile == "" {
			return ServiceConfig{}, fmt.Errorf("service.toml: forge %s is incomplete", name)
		}
	}
	if cfg.Runs.FailuresForge == "" && len(cfg.Forges) == 1 {
		for name := range cfg.Forges {
			cfg.Runs.FailuresForge = name
		}
	}
	if cfg.Runs.FailuresForge != "" {
		if _, ok := cfg.Forges[cfg.Runs.FailuresForge]; !ok {
			return ServiceConfig{}, fmt.Errorf("service.toml: runs.failures-forge %q is not a configured forge", cfg.Runs.FailuresForge)
		}
	} else if cfg.Runs.FailuresRepo != "" {
		return ServiceConfig{}, fmt.Errorf("service.toml: runs.failures-forge is required when runs.failures-repo is set and several forges are configured")
	}
	if !metadata.IsDefined("runs", "memory-envelope-gib") {
		cfg.Runs.MemoryEnvelopeGiB = defaultRunMemoryEnvelopeGiB
	}
	if !metadata.IsDefined("runs", "duration-ceiling") {
		cfg.Runs.DurationCeiling = defaultRunDurationCeiling
	}
	if !metadata.IsDefined("runs", "pressure-threshold-percent") {
		cfg.Runs.PressureThresholdPercent = defaultRunPressureThresholdPercent
	}
	if cfg.Runs.MemoryEnvelopeGiB < 1 || int64(cfg.Runs.MemoryEnvelopeGiB) > (1<<63-1)/(1<<30) {
		return ServiceConfig{}, fmt.Errorf("service.toml: runs.memory-envelope-gib must be positive and fit a signed 64-bit byte ceiling")
	}
	duration, err := time.ParseDuration(cfg.Runs.DurationCeiling)
	if err != nil || duration < time.Microsecond {
		return ServiceConfig{}, fmt.Errorf("service.toml: runs.duration-ceiling must be a Go duration of at least 1us (for example 12h or 90m)")
	}
	// Resolve the duration once; run consumers receive only systemd seconds.
	cfg.Runs.DurationCeiling = fmt.Sprintf("%d.%09ds", duration/time.Second, duration%time.Second)
	if cfg.Runs.PressureThresholdPercent < 1 || cfg.Runs.PressureThresholdPercent > 100 {
		return ServiceConfig{}, fmt.Errorf("service.toml: runs.pressure-threshold-percent must be between 1 and 100")
	}
	if cfg.Runs.MaxConcurrent < 0 || cfg.MaxConcurrentRuns() > cfg.Runs.MemoryEnvelopeGiB {
		return ServiceConfig{}, fmt.Errorf("service.toml: runs max-concurrent must be between 1 and %d — beyond that the shared %dG run memory envelope cannot give each run a useful share", cfg.Runs.MemoryEnvelopeGiB, cfg.Runs.MemoryEnvelopeGiB)
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

// LoadRepoConfigs reads every opted-in repository under the configuration
// root and resolves its knobs against the service-level values, so a
// returned RepoConfig carries the values a run will honour.
func LoadRepoConfigs(cfg ServiceConfig) ([]RepoConfig, error) {
	paths, err := filepath.Glob(filepath.Join(cfg.Root, "repos", "*.toml"))
	if err != nil {
		return nil, err
	}
	repos := make([]RepoConfig, 0, len(paths))
	for _, path := range paths {
		var repo RepoConfig
		metadata, err := decodeStrictTOML(path, &repo)
		if err != nil {
			return nil, err
		}
		repo.Path = path
		if repo.Forge == "" || repo.Owner == "" || repo.Repo == "" || repo.Adaptation.RunBody == "" {
			return nil, fmt.Errorf("%s: forge, owner, repo and adaptation.run-body are required", path)
		}
		// The file's own values are validated before resolution fills a
		// default, so an incomplete table is an error, never a silent default.
		if err := validateRepositoryKnobs(path, repo.RepositoryKnobs); err != nil {
			return nil, err
		}
		repo.RepositoryKnobs = resolveRepositoryKnobs(cfg.Repositories, repo.RepositoryKnobs, metadata.IsDefined)
		if err := validateRepositoryKnobs(path, repo.RepositoryKnobs); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, nil
}

// resolveRepositoryKnobs layers a repository file's knobs over the service
// defaults. A key the file defines wins whole, even when it is empty — a
// repository can switch off a service-wide list by defining it empty — and
// a knob defined at neither level takes its shipped default. A new knob
// joins this function as one line per key, beside its struct field.
func resolveRepositoryKnobs(defaults, own RepositoryKnobs, defined func(...string) bool) RepositoryKnobs {
	knobs := defaults
	if defined("work-in-progress-branch-prefixes") {
		knobs.WorkInProgressBranchPrefixes = own.WorkInProgressBranchPrefixes
	}
	if defined("review", "threshold") {
		knobs.Review.Threshold = own.Review.Threshold
	}
	if defined("guidance-sources") {
		knobs.GuidanceSources = own.GuidanceSources
	}
	if defined("filing-destination") {
		knobs.FilingDestination = own.FilingDestination
	}
	if defined("markers") {
		knobs.Markers = own.Markers
	}
	if knobs.Review.Threshold == "" {
		knobs.Review.Threshold = "High"
	}
	if knobs.FilingDestination.Kind == "" {
		knobs.FilingDestination = FilingDestination{Kind: FilingKindPullRequestComment}
	}
	if knobs.Markers.InFlight == nil {
		knobs.Markers.InFlight = &forge.Marker{Reaction: "eyes"}
	}
	if knobs.Markers.Clean == nil {
		knobs.Markers.Clean = &forge.Marker{Reaction: "+1"}
	}
	return knobs
}

// validateRepositoryKnobs checks one level's values. The service level may
// leave a knob unset, so a threshold or filing kind is checked only when
// present; the resolved repository level always carries both.
func validateRepositoryKnobs(where string, knobs RepositoryKnobs) error {
	if knobs.Review.Threshold != "" && !validReviewThreshold(knobs.Review.Threshold) {
		return fmt.Errorf("%s: review.threshold must be Critical, High, Medium or Low", where)
	}
	for _, prefix := range knobs.WorkInProgressBranchPrefixes {
		if prefix == "" {
			return fmt.Errorf("%s: work-in-progress-branch-prefixes cannot contain an empty prefix", where)
		}
	}
	for index, source := range knobs.GuidanceSources {
		if err := validRepositoryPath(source.Path); err != nil {
			return fmt.Errorf("%s: guidance-sources[%d].path %w", where, index, err)
		}
		if source.Repository != "" && !repositoryName.MatchString(source.Repository) {
			return fmt.Errorf("%s: guidance-sources[%d].repository must be owner/name on the same forge", where, index)
		}
	}
	for _, role := range []string{"in-flight", "clean", "attention"} {
		if marker := knobs.Markers.Roles()[role]; marker != nil {
			if err := marker.Validate(); err != nil {
				return fmt.Errorf("%s: markers.%s %w", where, role, err)
			}
		}
	}
	destination := knobs.FilingDestination
	switch destination.Kind {
	case "":
		if destination.Repository != "" || destination.Path != "" {
			return fmt.Errorf("%s: filing-destination.kind is required when its repository or path is set", where)
		}
	case FilingKindFile:
		if err := validRepositoryPath(destination.Path); err != nil {
			return fmt.Errorf("%s: filing-destination.path %w", where, err)
		}
		if destination.Repository != "" && !repositoryName.MatchString(destination.Repository) {
			return fmt.Errorf("%s: filing-destination.repository must be owner/name on the same forge", where)
		}
	case FilingKindIssue:
		if !repositoryName.MatchString(destination.Repository) {
			return fmt.Errorf("%s: filing-destination.repository must name the owner/name that takes the issues", where)
		}
		if destination.Path != "" {
			return fmt.Errorf("%s: filing-destination.path does not apply to the issue kind", where)
		}
	case FilingKindPullRequestComment, FilingKindNone:
		if destination.Repository != "" || destination.Path != "" {
			return fmt.Errorf("%s: filing-destination.repository and .path do not apply to the %s kind", where, destination.Kind)
		}
	default:
		return fmt.Errorf("%s: filing-destination.kind must be one of %s", where, strings.Join(filingKinds, ", "))
	}
	return nil
}

// validRepositoryPath admits a relative path inside a repository: no empty
// value, no absolute path, no parent-directory segment.
func validRepositoryPath(path string) error {
	if path == "" {
		return errors.New("is required")
	}
	if strings.HasPrefix(path, "/") {
		return errors.New("must be relative to the repository root")
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return errors.New("cannot leave the repository")
		}
	}
	return nil
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
	repos, err := LoadRepoConfigs(cfg)
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

// decodeStrictTOML decodes one file, refusing unknown keys, and returns the
// metadata so a caller can tell a key the file defines from one it omits.
func decodeStrictTOML(path string, target any) (toml.MetaData, error) {
	metadata, err := toml.DecodeFile(path, target)
	if err != nil {
		return toml.MetaData{}, fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return toml.MetaData{}, fmt.Errorf("%s: unknown TOML keys: %s", path, strings.Join(keys, ", "))
	}
	return metadata, nil
}

func ReadSecret(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
