package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

const testServiceConfig = `[service]
bot-login = "Minos"
[listener]
bind = ":8919"
[forges.local]
adaptation = "/tmp/adapt"
api-base = "http://forge.local"
webhook-secret-file = "/tmp/secret"
credential-file = "/tmp/token"
[runs]
dir = "/tmp/runs"
failure-log = "/tmp/failures.log"
max-concurrent = 2
[ensemble]
concurrency-claude = 10
concurrency-codex = 6
`

func TestLoadServiceConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(testServiceConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil || cfg.Service.BotLogin != "Minos" || cfg.Runs.Dir != "/tmp/runs" || cfg.Runs.FailureLog != "/tmp/failures.log" || cfg.Runs.MaxConcurrent != 2 || cfg.Ensemble.ConcurrencyClaude != 10 || cfg.Ensemble.ConcurrencyCodex != 6 {
		t.Fatalf("config = %#v, error = %v", cfg, err)
	}
}

func TestLoadServiceConfigDefaultsMaxConcurrentToOne(t *testing.T) {
	root := t.TempDir()
	contents := strings.Replace(testServiceConfig, "max-concurrent = 2\n", "", 1)
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil || cfg.MaxConcurrentRuns() != 1 {
		t.Fatalf("max-concurrent = %d, error = %v; want one live run", cfg.MaxConcurrentRuns(), err)
	}
}

func TestLoadServiceConfigRejectsUnusableMaxConcurrent(t *testing.T) {
	for _, setting := range []string{"max-concurrent = -1\n", "max-concurrent = 23\n"} {
		t.Run(strings.TrimSpace(setting), func(t *testing.T) {
			root := t.TempDir()
			contents := strings.Replace(testServiceConfig, "max-concurrent = 2\n", setting, 1)
			if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadServiceConfig(root)
			if err == nil || !strings.Contains(err.Error(), "max-concurrent") {
				t.Fatalf("error = %v, want max-concurrent validation", err)
			}
		})
	}
}

func TestLoadServiceConfigRequiresPositiveEnsembleConcurrency(t *testing.T) {
	for _, setting := range []string{
		"concurrency-claude = 0\nconcurrency-codex = 6\n",
		"concurrency-claude = 10\nconcurrency-codex = 0\n",
	} {
		t.Run(strings.TrimSpace(setting), func(t *testing.T) {
			root := t.TempDir()
			contents := strings.Replace(testServiceConfig,
				"concurrency-claude = 10\nconcurrency-codex = 6\n", setting, 1)
			if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadServiceConfig(root)
			if err == nil || !strings.Contains(err.Error(), "ensemble concurrency") {
				t.Fatalf("error = %v, want ensemble concurrency validation", err)
			}
		})
	}
}

func TestLoadServiceConfigDefaultsEnsembleConcurrency(t *testing.T) {
	root := t.TempDir()
	contents := strings.Replace(testServiceConfig,
		"[ensemble]\nconcurrency-claude = 10\nconcurrency-codex = 6\n", "", 1)
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil || cfg.Ensemble.ConcurrencyClaude != 2 || cfg.Ensemble.ConcurrencyCodex != 2 {
		t.Fatalf("ensemble defaults = %+v, error = %v", cfg.Ensemble, err)
	}
}

func TestLoadServiceConfigRejectsUnknownKeys(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(testServiceConfig+"unknown = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadServiceConfig(root)
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRepoConfigDecodesWorkInProgressBranchPrefixes(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "forge = \"local\"\nowner = \"owner\"\nrepo = \"repo\"\nwork-in-progress-branch-prefixes = [\"structural/\"]\n[adaptation]\nrun-body = \"/tmp/run-body\"\n"
	if err := os.WriteFile(filepath.Join(root, "repos", "repo.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	repos, err := LoadRepoConfigs(ServiceConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || len(repos[0].WorkInProgressBranchPrefixes) != 1 || repos[0].WorkInProgressBranchPrefixes[0] != "structural/" {
		t.Fatalf("work-in-progress prefixes = %+v", repos)
	}
}

func TestLoadRepoConfigRejectsEmptyWorkInProgressBranchPrefix(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "forge = \"local\"\nowner = \"owner\"\nrepo = \"repo\"\nwork-in-progress-branch-prefixes = [\"\"]\n[adaptation]\nrun-body = \"/tmp/run-body\"\n"
	if err := os.WriteFile(filepath.Join(root, "repos", "repo.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRepoConfigs(ServiceConfig{Root: root})
	if err == nil || !strings.Contains(err.Error(), "work-in-progress-branch-prefixes") {
		t.Fatalf("error = %v, want empty prefix validation", err)
	}
}

func TestLoadRepoConfigDefaultsReviewThreshold(t *testing.T) {
	for _, test := range []struct {
		name          string
		block         string
		wantThreshold string
	}{
		{name: "unset", wantThreshold: "High"},
		{
			name:          "explicit threshold survives",
			block:         "[review]\nthreshold = \"Medium\"\n",
			wantThreshold: "Medium",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
				t.Fatal(err)
			}
			contents := "forge = \"local\"\nowner = \"owner\"\nrepo = \"repo\"\n[adaptation]\nrun-body = \"/tmp/run-body\"\n" + test.block
			if err := os.WriteFile(filepath.Join(root, "repos", "repo.toml"), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			repos, err := LoadRepoConfigs(ServiceConfig{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			if len(repos) != 1 {
				t.Fatalf("repos = %d, want 1", len(repos))
			}
			if repos[0].Review.Threshold != test.wantThreshold {
				t.Fatalf("threshold = %q, want %q", repos[0].Review.Threshold, test.wantThreshold)
			}
		})
	}
}

func TestLoadRepoConfigValidatesReviewThreshold(t *testing.T) {
	for _, test := range []struct {
		name  string
		block string
		want  string
	}{
		{name: "threshold", block: "[review]\nthreshold = \"Urgent\"", want: "review.threshold"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
				t.Fatal(err)
			}
			contents := "forge = \"local\"\nowner = \"owner\"\nrepo = \"repo\"\n[adaptation]\nrun-body = \"/tmp/run-body\"\n" + test.block + "\n"
			if err := os.WriteFile(filepath.Join(root, "repos", "repo.toml"), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadRepoConfigs(ServiceConfig{Root: root})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

const testRepositoryDefaults = `[repositories]
work-in-progress-branch-prefixes = ["structural/"]
[repositories.review]
threshold = "Medium"
[[repositories.guidance-sources]]
path = "docs/intent.md"
[repositories.filing-destination]
kind = "file"
path = "ISSUES.md"
[repositories.markers]
in-flight = {label = "minos/reviewing"}
attention = {reaction = "confused"}
`

func writeRepoConfig(t *testing.T, root, name, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "repos", name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repoConfig composes a repository file: the identity keys, the test's own
// knob text (top-level keys before any table), then the adaptation table.
func repoConfig(knobs string) string {
	return "forge = \"local\"\nowner = \"owner\"\nrepo = \"repo\"\n" + knobs + "\n[adaptation]\nrun-body = \"/tmp/run-body\"\n"
}

func loadServiceConfigWith(t *testing.T, root, extra string) ServiceConfig {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(testServiceConfig+extra), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestRepositoryKnobsLayerServiceDefaultsUnderEachRepository(t *testing.T) {
	root := t.TempDir()
	cfg := loadServiceConfigWith(t, root, testRepositoryDefaults)
	writeRepoConfig(t, root, "inherits.toml", repoConfig(""))
	writeRepoConfig(t, root, "overrides.toml", repoConfig(
		"work-in-progress-branch-prefixes = []\n"+
			"[review]\nthreshold = \"Low\"\n"+
			"[[guidance-sources]]\nrepository = \"owner/repo-plans\"\npath = \"README.md\"\n"+
			"[[guidance-sources]]\npath = \"AGENTS.md\"\n"+
			"[filing-destination]\nkind = \"none\"\n"+
			"[markers]\nclean = {label = \"minos/approved\"}\n"))
	repos, err := LoadRepoConfigs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]RepoConfig{}
	for _, repo := range repos {
		byPath[filepath.Base(repo.Path)] = repo
	}

	inherits := byPath["inherits.toml"]
	if inherits.Review.Threshold != "Medium" ||
		!slices.Equal(inherits.WorkInProgressBranchPrefixes, []string{"structural/"}) ||
		!slices.Equal(inherits.GuidanceSources, []GuidanceSource{{Path: "docs/intent.md"}}) ||
		inherits.FilingDestination != (FilingDestination{Kind: FilingKindFile, Path: "ISSUES.md"}) ||
		*inherits.Markers.InFlight != (forge.Marker{Label: "minos/reviewing"}) ||
		*inherits.Markers.Clean != (forge.Marker{Reaction: "+1"}) ||
		inherits.Markers.Attention == nil || *inherits.Markers.Attention != (forge.Marker{Reaction: "confused"}) {
		t.Fatalf("repository without its own values = %+v, want the service defaults", inherits.RepositoryKnobs)
	}

	overrides := byPath["overrides.toml"]
	if overrides.Review.Threshold != "Low" ||
		len(overrides.WorkInProgressBranchPrefixes) != 0 ||
		!slices.Equal(overrides.GuidanceSources, []GuidanceSource{{Repository: "owner/repo-plans", Path: "README.md"}, {Path: "AGENTS.md"}}) ||
		overrides.FilingDestination != (FilingDestination{Kind: FilingKindNone}) ||
		// The markers table replaces the service's whole: its in-flight
		// label and attention reaction do not carry through.
		*overrides.Markers.InFlight != (forge.Marker{Reaction: "eyes"}) ||
		*overrides.Markers.Clean != (forge.Marker{Label: "minos/approved"}) ||
		overrides.Markers.Attention != nil {
		t.Fatalf("repository with its own values = %+v, want each override to replace the service value whole", overrides.RepositoryKnobs)
	}
}

func TestRepositoryKnobsUnsetAtBothLevelsTakeTheShippedDefaults(t *testing.T) {
	root := t.TempDir()
	cfg := loadServiceConfigWith(t, root, "")
	writeRepoConfig(t, root, "bare.toml", repoConfig(""))
	repos, err := LoadRepoConfigs(cfg)
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos = %+v, error = %v", repos, err)
	}
	knobs := repos[0].RepositoryKnobs
	if knobs.Review.Threshold != "High" || len(knobs.GuidanceSources) != 0 || len(knobs.WorkInProgressBranchPrefixes) != 0 ||
		knobs.FilingDestination != (FilingDestination{Kind: FilingKindPullRequestComment}) ||
		knobs.Markers.InFlight == nil || *knobs.Markers.InFlight != (forge.Marker{Reaction: "eyes"}) ||
		knobs.Markers.Clean == nil || *knobs.Markers.Clean != (forge.Marker{Reaction: "+1"}) ||
		knobs.Markers.Attention != nil {
		t.Fatalf("knobs = %+v, want High threshold, checked-in guidance, no prefixes, pull-request-comment filing, eyes and +1 markers and no attention marker", knobs)
	}
}

func TestRepositoryKnobsAreValidatedAtBothLevels(t *testing.T) {
	for _, test := range []struct {
		name string
		toml string
		want string
	}{
		{name: "guidance source without a path", toml: "[[guidance-sources]]\nrepository = \"owner/plans\"\n", want: "guidance-sources[0].path is required"},
		{name: "guidance source leaving the repository", toml: "[[guidance-sources]]\npath = \"../secrets.md\"\n", want: "cannot leave the repository"},
		{name: "guidance source with an absolute path", toml: "[[guidance-sources]]\npath = \"/etc/passwd\"\n", want: "must be relative"},
		{name: "guidance repository that is not owner/name", toml: "[[guidance-sources]]\nrepository = \"https://forge/x/y\"\npath = \"a.md\"\n", want: "guidance-sources[0].repository must be owner/name"},
		{name: "unknown filing kind", toml: "[filing-destination]\nkind = \"email\"\n", want: "filing-destination.kind must be one of file, issue, pull-request-comment, none"},
		{name: "kind-less destination with a path", toml: "[filing-destination]\npath = \"ISSUES.md\"\n", want: "filing-destination.kind is required when its repository or path is set"},
		{name: "file kind without a path", toml: "[filing-destination]\nkind = \"file\"\n", want: "filing-destination.path is required"},
		{name: "issue kind without a repository", toml: "[filing-destination]\nkind = \"issue\"\n", want: "filing-destination.repository must name"},
		{name: "issue kind with a path", toml: "[filing-destination]\nkind = \"issue\"\nrepository = \"owner/plans\"\npath = \"x\"\n", want: "filing-destination.path does not apply"},
		{name: "none kind with a repository", toml: "[filing-destination]\nkind = \"none\"\nrepository = \"owner/plans\"\n", want: "do not apply to the none kind"},
		{name: "marker with both forms", toml: "[markers]\nclean = {reaction = \"+1\", label = \"minos/approved\"}\n", want: "markers.clean must set exactly one of reaction or label"},
		{name: "marker with neither form", toml: "[markers]\nattention = {}\n", want: "markers.attention must set exactly one of reaction or label"},
		{name: "marker reaction that is not a name", toml: "[markers]\nin-flight = {reaction = \"thumbs up\"}\n", want: "markers.in-flight reaction must be a reaction name"},
		{name: "marker label with surrounding space", toml: "[markers]\nin-flight = {label = \" minos\"}\n", want: "markers.in-flight label must be a label name"},
		{name: "comment kind with a path", toml: "[filing-destination]\nkind = \"pull-request-comment\"\npath = \"x\"\n", want: "do not apply to the pull-request-comment kind"},
	} {
		t.Run("repository: "+test.name, func(t *testing.T) {
			root := t.TempDir()
			cfg := loadServiceConfigWith(t, root, "")
			writeRepoConfig(t, root, "repo.toml", repoConfig(test.toml))
			_, err := LoadRepoConfigs(cfg)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "repo.toml") {
				t.Fatalf("error = %v, want %q naming the repository file", err, test.want)
			}
		})
		t.Run("service: "+test.name, func(t *testing.T) {
			root := t.TempDir()
			serviceLevel := strings.ReplaceAll(test.toml, "[[guidance-sources]]", "[[repositories.guidance-sources]]")
			serviceLevel = strings.ReplaceAll(serviceLevel, "[filing-destination]", "[repositories.filing-destination]")
			serviceLevel = strings.ReplaceAll(serviceLevel, "[markers]", "[repositories.markers]")
			if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(testServiceConfig+serviceLevel), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadServiceConfig(root)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "service.toml: repositories") {
				t.Fatalf("error = %v, want %q naming the service level", err, test.want)
			}
		})
	}
}

func TestLoadServiceConfigDefaultsCommitIdentityToTheBotLogin(t *testing.T) {
	root := t.TempDir()
	cfg := loadServiceConfigWith(t, root, "")
	if cfg.Service.CommitAuthorName != "Minos" || cfg.Service.CommitAuthorEmail != "Minos@minos.invalid" {
		t.Fatalf("identity = %q <%q>, want the bot login and a reserved-domain address", cfg.Service.CommitAuthorName, cfg.Service.CommitAuthorEmail)
	}
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(strings.Replace(testServiceConfig,
		"bot-login = \"Minos\"\n", "bot-login = \"Minos\"\ncommit-author-name = \"Review Bot\"\ncommit-author-email = \"bot@example.org\"\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	explicit, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Service.CommitAuthorName != "Review Bot" || explicit.Service.CommitAuthorEmail != "bot@example.org" {
		t.Fatalf("identity = %q <%q>, want the configured values", explicit.Service.CommitAuthorName, explicit.Service.CommitAuthorEmail)
	}
}

func TestLoadServiceConfigStatusContextDefaultsToMinosAndHonoursAnExplicitValue(t *testing.T) {
	root := t.TempDir()
	if cfg := loadServiceConfigWith(t, root, ""); cfg.Service.StatusContext != "Minos" {
		t.Fatalf("status context = %q, want the default Minos", cfg.Service.StatusContext)
	}
	writeServiceStatusContext(t, root, `"Review Bot"`)
	explicit, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Service.StatusContext != "Review Bot" {
		t.Fatalf("status context = %q, want the configured Review Bot", explicit.Service.StatusContext)
	}
}

func TestLoadServiceConfigRejectsMalformedStatusContext(t *testing.T) {
	for _, value := range []string{`""`, `" Review Bot"`, `"Review\nBot"`, `"   "`} {
		root := t.TempDir()
		writeServiceStatusContext(t, root, value)
		if _, err := LoadServiceConfig(root); err == nil || !strings.Contains(err.Error(), "status-context") {
			t.Fatalf("status-context = %s: error = %v, want a status-context rejection", value, err)
		}
	}
}

func writeServiceStatusContext(t *testing.T, root, value string) {
	t.Helper()
	body := strings.Replace(testServiceConfig, "bot-login = \"Minos\"\n", "bot-login = \"Minos\"\nstatus-context = "+value+"\n", 1)
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadServiceConfigDecodesAndValidatesRouting(t *testing.T) {
	root := t.TempDir()
	cfg := loadServiceConfigWith(t, root, "[routing.verifier]\nengine = \"codex\"\nmodel = \"gpt-6-astra\"\neffort = \"low\"\n[routing.brief-planner]\neffort = \"medium\"\n")
	want := map[string]RoleRouting{
		"verifier":      {Engine: "codex", Model: "gpt-6-astra", Effort: "low"},
		"brief-planner": {Effort: "medium"},
	}
	if got := cfg.Routing.Roles(); len(got) != 2 || got["verifier"] != want["verifier"] || got["brief-planner"] != want["brief-planner"] {
		t.Fatalf("routing roles = %+v, want %+v", got, want)
	}
	if roles := loadServiceConfigWith(t, root, "").Routing.Roles(); len(roles) != 0 {
		t.Fatalf("unset routing exports roles %+v, want none", roles)
	}
	for _, test := range []struct{ name, toml, want string }{
		{name: "unknown engine", toml: "[routing.proposer]\nengine = \"opencode\"\n", want: "routing.proposer.engine must be one of claude, codex"},
		{name: "model without engine", toml: "[routing.proposer]\nmodel = \"gpt-6-astra\"\n", want: "routing.proposer.model needs routing.proposer.engine"},
		{name: "unknown effort", toml: "[routing.exploration]\neffort = \"maximal\"\n", want: "routing.exploration.effort must be one of minimal, low, medium, high, xhigh"},
		{name: "unknown role", toml: "[routing.judge]\nengine = \"claude\"\n", want: "unknown TOML keys: routing.judge"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(testServiceConfig+test.toml), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadServiceConfig(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunCeilingConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, settings, wantError string
		envelope, threshold       int
		duration                  string
	}{
		{name: "defaults", envelope: 22, threshold: 85, duration: "43200.000000000s"},
		{name: "configured", settings: "memory-envelope-gib = 8\nduration-ceiling = \"90m\"\npressure-threshold-percent = 60\n", envelope: 8, threshold: 60, duration: "5400.000000000s"},
		{name: "minimum bounds", settings: "memory-envelope-gib = 2\npressure-threshold-percent = 1\n", envelope: 2, threshold: 1, duration: "43200.000000000s"},
		{name: "maximum percent", settings: "pressure-threshold-percent = 100\n", envelope: 22, threshold: 100, duration: "43200.000000000s"},
		{name: "zero envelope", settings: "memory-envelope-gib = 0\n", wantError: "runs.memory-envelope-gib"},
		{name: "negative envelope", settings: "memory-envelope-gib = -1\n", wantError: "runs.memory-envelope-gib"},
		{name: "overflowing byte ceiling", settings: "memory-envelope-gib = 8589934592\n", wantError: "runs.memory-envelope-gib"},
		{name: "concurrency exceeds envelope", settings: "memory-envelope-gib = 1\n", wantError: "max-concurrent"},
		{name: "malformed duration", settings: "duration-ceiling = \"tomorrow\"\n", wantError: "runs.duration-ceiling"},
		{name: "empty duration", settings: "duration-ceiling = \"\"\n", wantError: "runs.duration-ceiling"},
		{name: "zero duration", settings: "duration-ceiling = \"0s\"\n", wantError: "runs.duration-ceiling"},
		{name: "sub-microsecond duration", settings: "duration-ceiling = \"1ns\"\n", wantError: "runs.duration-ceiling"},
		{name: "negative duration", settings: "duration-ceiling = \"-1h\"\n", wantError: "runs.duration-ceiling"},
		{name: "zero percent", settings: "pressure-threshold-percent = 0\n", wantError: "runs.pressure-threshold-percent"},
		{name: "negative percent", settings: "pressure-threshold-percent = -1\n", wantError: "runs.pressure-threshold-percent"},
		{name: "percent above 100", settings: "pressure-threshold-percent = 101\n", wantError: "runs.pressure-threshold-percent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			contents := strings.Replace(testServiceConfig, "[runs]\n", "[runs]\n"+test.settings, 1)
			if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadServiceConfig(root)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), "service.toml") || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want service.toml and %s", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Runs.MemoryEnvelopeGiB != test.envelope || cfg.Runs.DurationCeiling != test.duration || cfg.Runs.PressureThresholdPercent != test.threshold {
				t.Fatalf("run ceilings = %d / %s / %d, want %d / %s / %d", cfg.Runs.MemoryEnvelopeGiB, cfg.Runs.DurationCeiling, cfg.Runs.PressureThresholdPercent, test.envelope, test.duration, test.threshold)
			}
		})
	}
}

func TestForgeWebBaseConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, value  string
		set, invalid bool
	}{
		{name: "unset"},
		{name: "empty", set: true},
		{name: "https", value: "https://forge.example/forge/", set: true},
		{name: "http", value: "http://forge.example:3000", set: true},
		{name: "relative", value: "/forge", set: true, invalid: true},
		{name: "scheme relative", value: "//forge.example", set: true, invalid: true},
		{name: "missing host", value: "https:///forge", set: true, invalid: true},
		{name: "unsupported scheme", value: "ftp://forge.example", set: true, invalid: true},
		{name: "malformed", value: "https://forge.example/%zz", set: true, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			body := testServiceConfig
			if test.set {
				body = strings.Replace(body, "[forges.local]\n", fmt.Sprintf("[forges.local]\nweb-base = %q\n", test.value), 1)
			}
			if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadServiceConfig(root)
			if test.invalid {
				if err == nil || !strings.Contains(err.Error(), "forge local web-base") {
					t.Fatalf("error = %v, want web-base refusal naming forge local", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Forges["local"].WebBase; got != test.value {
				t.Fatalf("web-base = %q, want %q", got, test.value)
			}
		})
	}
}
