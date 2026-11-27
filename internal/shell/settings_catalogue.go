package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// The settings catalogue names every key an operator can set in
// service.toml or a repository's file, says what each does and what applies
// when nothing sets it, and reads where each one's value comes from. The web
// page lists settings from it and writes only the keys it names. A test holds
// it to the configuration's own struct tags, so a key the loader accepts and
// the catalogue does not describe turns the suite red: a new knob joins the
// catalogue as one entry beside its struct field.

// SettingScope says which files a setting is written in.
type SettingScope string

const (
	// ServiceScope: service.toml alone.
	ServiceScope SettingScope = "service"
	// RepositoryScope: a repository's own file, over a service-wide value in
	// service.toml at the setting's ServiceWideKey.
	RepositoryScope SettingScope = "repository"
	// RepositoryFileScope: a repository's own file alone.
	RepositoryFileScope SettingScope = "repository-file"
)

// SettingForm is how a value is typed in: the field a page offers and how
// ParseSettingValue reads what was typed.
type SettingForm string

const (
	// TextForm: the value is the text as typed.
	TextForm SettingForm = "text"
	// NumberForm: a whole number.
	NumberForm SettingForm = "number"
	// ChoiceForm: one of the setting's Choices.
	ChoiceForm SettingForm = "choice"
	// BoolForm: true or false.
	BoolForm SettingForm = "bool"
	// TOMLForm: any value, written as it would follow "key =" in the file:
	// a quoted string, a list, an inline table, a list of inline tables.
	TOMLForm SettingForm = "toml"
)

// When a setting written to the file takes effect. The sweep, a webhook
// delivery and each run read the configuration afresh, so most settings
// apply from the next run; these do not.
const (
	fromNextRun        = ""
	whenReceiverStarts = "The receiver reads this when it starts: the change takes effect once it is restarted."
	whenUnitsInstalled = "This sizes the runs slice: the change takes effect once `minos install-units` has been run again and the user manager reloaded."
)

// Setting describes one key.
type Setting struct {
	// Key is the key path in the file the setting is written to; for a
	// RepositoryScope setting, in a repository's own file.
	Key   []string
	Scope SettingScope
	// About says what the setting does, in a sentence for the operator.
	About string
	// Default is what applies when no file sets the key; empty for a key the
	// loader requires.
	Default string
	Form    SettingForm
	// Choices are the values a ChoiceForm or BoolForm setting takes.
	Choices []string
	// TakesEffect, when set, says when a change applies where that is not
	// the next run.
	TakesEffect string
}

// Name is the setting's dotted key, as the file spells it.
func (setting Setting) Name() string { return dottedTOMLKey(setting.Key) }

// Required reports whether the loader refuses a file without the key.
func (setting Setting) Required() bool { return setting.Default == "" }

func serviceSetting(key, about, fallback string, form SettingForm) Setting {
	return Setting{Key: strings.Split(key, "."), Scope: ServiceScope, About: about, Default: fallback, Form: form}
}

func repositorySetting(key, about, fallback string, form SettingForm) Setting {
	return Setting{Key: strings.Split(key, "."), Scope: RepositoryScope, About: about, Default: fallback, Form: form}
}

func (setting Setting) restartsWith(when string) Setting {
	setting.TakesEffect = when
	return setting
}

func (setting Setting) choosing(choices ...string) Setting {
	if setting.Form != BoolForm {
		setting.Form = ChoiceForm
	}
	setting.Choices = choices
	return setting
}

// forgeSettings are the keys of each configured forge's table, under
// forges.<name>. The catalogue lists them once for each forge service.toml
// configures.
var forgeSettings = []Setting{
	serviceSetting("adaptation", "The directory of this forge's adaptation scripts.", "", TextForm),
	serviceSetting("api-base", "The base URL of this forge's API.", "", TextForm),
	serviceSetting("web-base", "The base URL of this forge's web pages, which a status links from.", "The adaptation's own reading of the API base.", TextForm),
	serviceSetting("webhook-secret-file", "The file holding the secret this forge signs its webhooks with.", "", TextForm),
	serviceSetting("credential-file", "The file holding the credential Minos writes to this forge with.", "", TextForm),
	serviceSetting("signature-header", "The request header carrying this forge's webhook signature.", "X-Forgejo-Signature", TextForm),
	serviceSetting("bot-login", "The login this forge shows on every Minos write, where it differs from the service's.", "The service's bot login.", TextForm),
}

func settingsCatalogue() []Setting {
	settings := []Setting{
		serviceSetting("service.bot-login", "The forge login Minos reviews as.", "", TextForm),
		serviceSetting("service.commit-author-name", "The author name of every commit Minos makes.", "The bot login.", TextForm),
		serviceSetting("service.commit-author-email", "The author email of every commit Minos makes.", "The bot login at minos.invalid.", TextForm),
		serviceSetting("service.status-context", "The context of every commit status Minos writes, and the only one it reads back as its completion marker.", defaultStatusContext, TextForm),
		serviceSetting("listener.bind", "The address the receiver listens on for webhooks, the status routes and this page.", "", TextForm).restartsWith(whenReceiverStarts),
		serviceSetting("listener.status-token-file", "The file holding the bearer token the status routes ask for.", "Unset: the status routes are not served.", TextForm).restartsWith(whenReceiverStarts),
		serviceSetting("listener.page-credential-file", "The file holding the operator credential this page's sign-in asks for. Its content is read at each use; the path is read when the receiver starts.", "Unset: this page is not served.", TextForm).restartsWith(whenReceiverStarts),
		serviceSetting("runs.dir", "The directory each run works in.", "", TextForm),
		serviceSetting("runs.failure-log", "The local file a failure is recorded in when no failures repository is configured, outside the runs directory.", "Unset: without a failures repository, crash salvage has nowhere to record.", TextForm),
		serviceSetting("runs.failures-repo", "The local checkout of the failure-ledger repository, whose FAILURES.md each failure is appended to.", "Unset: failures go to the local failure log.", TextForm),
		serviceSetting("runs.failures-forge", "The forge the failures repository is on.", "The one configured forge, where only one is.", TextForm),
		serviceSetting("runs.failures-credential-file", "The credential the failure ledger is published to its forge with.", "Unset: the ledger is kept in the local checkout and not published.", TextForm),
		serviceSetting("runs.archive-command", "The command each terminal run is archived by before its directory is deleted.", "Unset: runs are deleted unarchived.", TextForm),
		serviceSetting("runs.timings-command", "The command that assembles a run's timing record from what the run has written.", "Unset: no timing records.", TextForm),
		serviceSetting("runs.recent-timings-command", "The command that lists the timing records archived since a cutoff, for recent runs.", "Unset: no recent runs are reported.", TextForm),
		serviceSetting("runs.recent-timings-cache-seconds", "How long a listing of recent runs is served before the archive host is asked again.", strconv.Itoa(defaultRecentTimingsCacheSeconds), NumberForm),
		serviceSetting("runs.max-concurrent", "How many runs may be live at once.", "1", NumberForm),
		serviceSetting("runs.memory-envelope-gib", "The memory, in GiB, all runs share through the runs slice.", strconv.Itoa(defaultRunMemoryEnvelopeGiB), NumberForm).restartsWith(whenUnitsInstalled),
		serviceSetting("runs.duration-ceiling", "How long a run may last before it is stopped, as a Go duration (12h, 90m).", defaultRunDurationCeiling, TextForm),
		serviceSetting("runs.pressure-threshold-percent", "The share of the memory envelope at which a run is asked to wind down.", strconv.Itoa(defaultRunPressureThresholdPercent), NumberForm),
		serviceSetting("ensemble.concurrency-claude", "How many Claude agents a workflow runs at once.", strconv.Itoa(defaultEngineConcurrency), NumberForm),
		serviceSetting("ensemble.concurrency-codex", "How many Codex agents a workflow runs at once.", strconv.Itoa(defaultEngineConcurrency), NumberForm),
		serviceSetting("ensemble.agent-ceiling", "How many agents a workflow runs at once across both engines.", "The runtime's own ceiling.", NumberForm),

		Setting{Key: []string{"adaptation", "run-body"}, Scope: RepositoryFileScope, About: "The program each run of this repository starts.", Form: TextForm},
		repositorySetting("admission", "Which pull requests are reviewed: by label (labels, exclude-labels), author (authors), base branch (base-branches), draft state (drafts: ready, any or only), title (title-pattern), touched paths (paths) and whether forks are admitted (allow-forks).", "Every pull request that is not a draft and not from a fork.", TOMLForm),
		repositorySetting("work-in-progress-branch-prefixes", "Branch name prefixes whose pull requests are not reviewed.", "None.", TOMLForm),
		repositorySetting("review.threshold", "The severity at and above which a confirmed finding blocks the pull request.", "High", ChoiceForm).choosing("Critical", "High", "Medium", "Low"),
		repositorySetting("review.depth", "How widely exploration plans and how deeply specialists read.", defaultReviewDepth, ChoiceForm).choosing(slices.Clone(reviewDepths)...),
		repositorySetting("review.file-safeguard-requests", "Whether confirmed requests for a safeguard against a failure nobody has observed go to the filing destination, each marked as a safeguard request; they never block.", "false", BoolForm).choosing("false", "true"),
		repositorySetting("push-inspection.outcomes", "What a run may decide to review after a push: no-review, increment or full; full is always among them.", `["no-review", "increment", "full"]`, TOMLForm),
		repositorySetting("guidance-sources", "The ordered documents that carry the project's declared intent, each a path and, for another repository on the same forge, its owner/name.", "The repository's own guidance: the first present of AGENTS.md, CLAUDE.md and README.md.", TOMLForm),
		repositorySetting("filing-destination", "Where confirmed findings that do not block go: kind file (with path, and repository for another), issue (with repository), pull-request-comment or none.", `{kind = "pull-request-comment"}`, TOMLForm),
		repositorySetting("markers", "The marks Minos sets on a pull request (in-flight, clean, attention), each a reaction or a label.", `👀 in flight, 👍 clean and no attention marker.`, TOMLForm),
	}
	for _, role := range routingRoles {
		settings = append(settings, repositorySetting("routing."+role, "The engine, model and effort the "+role+" role runs on, as {engine, model, effort}; the whole role is one setting.", "The shipped pairing: proposing and planning roles on Codex, the verifier and judgement on Claude, where both engines are provisioned; the one provisioned engine otherwise.", TOMLForm))
	}
	for _, concern := range specialistConcerns {
		settings = append(settings, repositorySetting("routing.concerns."+concern, "The engine, model and effort the "+concern+" specialist proposes on, as {engine, model, effort}.", "The proposer's routing.", TOMLForm))
	}
	return settings
}

// ParseSettingValue reads a value typed for setting into what the writer
// takes.
func ParseSettingValue(setting Setting, typed string) (any, error) {
	switch setting.Form {
	case TextForm:
		return typed, nil
	case NumberForm:
		number, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s takes a whole number", setting.Name())
		}
		return number, nil
	case BoolForm:
		value, err := strconv.ParseBool(typed)
		if err != nil {
			return nil, fmt.Errorf("%s takes true or false", setting.Name())
		}
		return value, nil
	case ChoiceForm:
		if !slices.Contains(setting.Choices, typed) {
			return nil, fmt.Errorf("%s takes one of %s", setting.Name(), strings.Join(setting.Choices, ", "))
		}
		return typed, nil
	}
	var decoded map[string]any
	if _, err := toml.Decode("value = "+typed, &decoded); err != nil {
		return nil, fmt.Errorf("%s: %q is not a TOML value as the file would hold it: %v", setting.Name(), typed, err)
	}
	if len(decoded) != 1 {
		// Lines after the value decode as keys of their own, which would be
		// dropped unwritten.
		return nil, fmt.Errorf("%s takes one value: %q holds more than one statement", setting.Name(), typed)
	}
	return copyTOMLValue(decoded["value"]), nil
}

// SettingState is one setting with where its value comes from.
type SettingState struct {
	Setting
	// Level is where the value that applies is set: the repository's file,
	// service.toml, or nowhere, when Default applies.
	Level SettingLevel
	// Value is the value at Level as the file holds it, spelled in TOML;
	// empty at SetNowhere.
	Value any
	// Typed is that value as the setting's form shows it for editing.
	Typed string
	// ServiceWide is the service-wide value a repository's own value
	// overrides, spelled in TOML, or empty where service.toml sets none.
	ServiceWide string
	// File is the path a change to this setting is written to.
	File string
}

// Spelled is Value in TOML.
func (state SettingState) Spelled() string {
	if state.Level == SetNowhere {
		return ""
	}
	spelled, _ := inlineTOML(state.Value)
	return spelled
}

// ServiceSettings reads every setting service.toml holds: those of the service
// itself, each configured forge's, and the service-wide value of each
// per-repository setting, with where each value comes from.
func ServiceSettings(root string) ([]SettingState, error) {
	path := filepath.Join(root, "service.toml")
	service, err := decodeTOMLFile(path)
	if err != nil {
		return nil, err
	}
	var states []SettingState
	for _, setting := range settingsCatalogue() {
		switch setting.Scope {
		case ServiceScope:
			states = append(states, readSetting(setting, path, setting.Key, service))
			if slices.Equal(setting.Key, []string{"listener", "page-credential-file"}) {
				states = append(states, forgeStates(path, service)...)
			}
		case RepositoryScope:
			states = append(states, readSetting(setting, path, ServiceWideKey(setting.Key), service))
		}
	}
	return states, nil
}

// forgeStates reads the table of each forge service.toml configures, by
// forge name.
func forgeStates(path string, service map[string]any) []SettingState {
	forges, _ := service["forges"].(map[string]any)
	var states []SettingState
	for _, name := range sortedTOMLKeys(forges) {
		for _, setting := range forgeSettings {
			setting.Key = append([]string{"forges", name}, setting.Key...)
			states = append(states, readSetting(setting, path, setting.Key, service))
		}
	}
	return states
}

// RepositorySettings reads every setting of one enrolled repository: each
// per-repository setting where the repository's file, service.toml or
// neither sets it, by the rule the loader resolves it by, and the settings of
// the repository's file alone.
func RepositorySettings(root string, repo RepoConfig) ([]SettingState, error) {
	servicePath := filepath.Join(root, "service.toml")
	service, err := decodeTOMLFile(servicePath)
	if err != nil {
		return nil, err
	}
	own, err := decodeTOMLFile(repo.Path)
	if err != nil {
		return nil, err
	}
	var states []SettingState
	for _, setting := range settingsCatalogue() {
		switch setting.Scope {
		case RepositoryFileScope:
			state := readSetting(setting, repo.Path, setting.Key, own)
			if state.Level != SetNowhere {
				state.Level = SetForRepository
			}
			states = append(states, state)
		case RepositoryScope:
			serviceWide := readSetting(setting, servicePath, ServiceWideKey(setting.Key), service)
			state := readSetting(setting, repo.Path, setting.Key, own)
			if state.Level == SetNowhere {
				// The service-wide value applies, and the field offers it as
				// the start of an override.
				state.Level, state.Value, state.Typed = serviceWide.Level, serviceWide.Value, serviceWide.Typed
			} else {
				state.Level = SetForRepository
			}
			state.ServiceWide = serviceWide.Spelled()
			states = append(states, state)
		}
	}
	return states, nil
}

// readSetting reads setting at key in the decoded file at path.
func readSetting(setting Setting, path string, key []string, decoded map[string]any) SettingState {
	state := SettingState{Setting: setting, Level: SetNowhere, File: path}
	value, defined := lookupTOMLPath(decoded, key)
	if !defined {
		return state
	}
	state.Level, state.Value = SetServiceWide, value
	switch typed := value.(type) {
	case string:
		if setting.Form != TOMLForm {
			state.Typed = typed
			return state
		}
	case bool:
		if setting.Form == BoolForm {
			state.Typed = strconv.FormatBool(typed)
			return state
		}
	case int64:
		if setting.Form == NumberForm {
			state.Typed = strconv.FormatInt(typed, 10)
			return state
		}
	}
	state.Typed, _ = inlineTOML(value)
	return state
}

func decodeTOMLFile(path string) (map[string]any, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeTOMLGeneric(string(content))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return decoded, nil
}

// FindSetting is the setting named name among states, as a submitted form
// names it.
func FindSetting(states []SettingState, name string) (SettingState, bool) {
	index := slices.IndexFunc(states, func(state SettingState) bool { return state.Name() == name })
	if index < 0 {
		return SettingState{}, false
	}
	return states[index], true
}

// catalogueKeys is every dotted key the catalogue names, a forge's under
// forges.*, for the test that holds it to the configuration's struct tags.
func catalogueKeys() []string {
	var keys []string
	for _, setting := range settingsCatalogue() {
		keys = append(keys, setting.Name())
	}
	for _, setting := range forgeSettings {
		keys = append(keys, "forges.*."+setting.Name())
	}
	sort.Strings(keys)
	return keys
}
