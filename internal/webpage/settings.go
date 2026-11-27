package webpage

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"

	"github.com/BFJ-Concerns/Minos/internal/shell"
)

// The settings pages show every setting with the value that applies and
// where it comes from, and change one at a time. The service's page holds
// service.toml: the service's own settings, each forge's, and the
// service-wide value of every per-repository setting. A repository's page
// holds that repository's file, each per-repository setting shown over the
// service-wide value it overrides or falls back to. A change is written to
// the file the deployer edits by hand, and the sweep, a webhook and each run
// read that file afresh.
var serviceSettingsPage = Page{
	Path:     "/settings",
	Title:    "Settings",
	Nav:      true,
	Template: "settings.html",
	Show:     showServiceSettings,
	Change:   changeServiceSetting,
}

var repositorySettingsPage = Page{
	Path:     "/repositories/{forge}/{owner}/{repo}/settings",
	Title:    "Repository settings",
	Template: "settings.html",
	Show:     showRepositorySettings,
	Change:   changeRepositorySetting,
}

type settingsView struct {
	// Repository is the repository whose settings are shown, nil on the
	// service's page, which lists Repositories instead.
	Repository   *shell.RepoConfig
	Repositories []shell.RepoConfig
	Unavailable  []string
	Path         string
	Saved        string
	Groups       []settingsGroup
}

type settingsGroup struct {
	Title    string
	Settings []settingRow
}

type settingRow struct {
	shell.SettingState
	// Source says where the value shown comes from.
	Source string
	// Own is whether this page's file sets the value, so that removing it
	// lets the next level out apply.
	Own bool
	// Fallback names what applies once this page's file no longer sets it.
	Fallback string
	// Field is what the form's field holds: the value that applies, or what
	// was just submitted and refused.
	Field         string
	InvalidChoice string
	Refused       bool
}

// Anchor is the row's fragment, which a saved change returns to.
func (row settingRow) Anchor() string { return "setting-" + row.Name() }

func showServiceSettings(r *http.Request, read Readings) (any, error) {
	repos, unavailable, err := read.SettingsRepositories()
	if err != nil {
		return nil, err
	}
	states, err := read.ServiceSettings()
	if err != nil {
		return nil, err
	}
	view := settingsView{Repositories: repos, Unavailable: unavailable}
	for _, state := range states {
		row := settingRow{SettingState: state, Own: state.Level == shell.SetServiceWide, Fallback: "the default"}
		switch {
		case state.Level == shell.SetNowhere:
			row.Source = "default"
		case state.Scope == shell.RepositoryScope:
			row.Source = "service.toml, for every repository that sets none"
		default:
			row.Source = "service.toml"
		}
		view.add(serviceGroup(state), row)
	}
	return view.finish(r), nil
}

func showRepositorySettings(r *http.Request, read Readings) (any, error) {
	repo, err := enrolledRepository(r, read)
	if err != nil {
		return nil, err
	}
	states, err := read.RepositorySettings(repo)
	if err != nil {
		return nil, err
	}
	view := settingsView{Repository: &repo}
	file := filepath.Base(repo.Path)
	for _, state := range states {
		row := settingRow{SettingState: state, Own: state.Level == shell.SetForRepository, Fallback: "the service-wide value"}
		if state.ServiceWide == "" {
			row.Fallback = "the default"
		}
		switch state.Level {
		case shell.SetForRepository:
			row.Source = "this repository's " + file
		case shell.SetServiceWide:
			row.Source = "service-wide, in service.toml"
		default:
			row.Source = "default"
		}
		title := "Review"
		switch {
		case state.Scope == shell.RepositoryFileScope:
			title = "Run"
		case state.Key[0] == "routing":
			title = "Routing"
		}
		view.add(title, row)
	}
	return view.finish(r), nil
}

// serviceGroup is the heading a setting of service.toml is listed under.
func serviceGroup(state shell.SettingState) string {
	switch {
	case state.Scope == shell.RepositoryScope && state.Key[0] == "routing":
		return "Routing for every repository"
	case state.Scope == shell.RepositoryScope:
		return "Review for every repository"
	}
	switch state.Key[0] {
	case "service", "listener":
		return "Service"
	case "forges":
		return "Forge " + state.Key[1]
	case "runs":
		return "Runs"
	}
	return "Agents"
}

func (view *settingsView) add(title string, row settingRow) {
	if count := len(view.Groups); count == 0 || view.Groups[count-1].Title != title {
		view.Groups = append(view.Groups, settingsGroup{Title: title})
	}
	group := &view.Groups[len(view.Groups)-1]
	group.Settings = append(group.Settings, row)
}

// finish fills each form's field, with what was just submitted where a
// change was refused, and says what a change just saved did.
func (view settingsView) finish(r *http.Request) settingsView {
	view.Path = r.URL.EscapedPath()
	submitted := r.PostFormValue("setting")
	for g := range view.Groups {
		for s := range view.Groups[g].Settings {
			row := &view.Groups[g].Settings[s]
			row.Field = row.Typed
			if row.UsesDefault() && len(row.Choices) > 0 {
				row.Field = row.Default
			}
			if submitted != "" && row.Name() == submitted {
				row.Field, row.Refused = r.PostFormValue("value"), true
			}
			if len(row.Choices) > 0 && !slices.Contains(row.Choices, row.Field) {
				row.InvalidChoice = fmt.Sprintf("Invalid value %q. Choose a replacement.", row.Field)
			}
			if r.Method != http.MethodGet {
				continue
			}
			if name := r.URL.Query().Get("saved"); name == row.Name() {
				view.Saved = fmt.Sprintf("Saved %s in %s. %s", name, row.File, takesEffect(row.Setting))
			}
			if name := r.URL.Query().Get("removed"); name == row.Name() {
				view.Saved = fmt.Sprintf("Removed %s from %s, so %s applies. %s", name, row.File, row.Fallback, takesEffect(row.Setting))
			}
		}
	}
	return view
}

func takesEffect(setting shell.Setting) string {
	if setting.TakesEffect != "" {
		return setting.TakesEffect
	}
	return "It takes effect from the next run."
}

func changeServiceSetting(r *http.Request, read Readings, write Settings) (string, error) {
	states, err := read.ServiceSettings()
	if err != nil {
		return "", err
	}
	return changeSetting(r, states, "/settings", func(state shell.SettingState, value any) error {
		key := state.Key
		if state.Scope == shell.RepositoryScope {
			key = shell.ServiceWideKey(key)
		}
		return write.Service(key, value)
	})
}

func changeRepositorySetting(r *http.Request, read Readings, write Settings) (string, error) {
	repo, err := enrolledRepository(r, read)
	if err != nil {
		return "", err
	}
	states, err := read.RepositorySettings(repo)
	if err != nil {
		return "", err
	}
	return changeSetting(r, states, r.URL.EscapedPath(), func(state shell.SettingState, value any) error {
		return write.Repository(repo, state.Key, value)
	})
}

// changeSetting carries out a submitted form: the setting it names set to
// the value typed, or removed. Anything the writer refuses, the loader's
// refusal among it, is shown to the operator above the form.
func changeSetting(r *http.Request, states []shell.SettingState, page string, write func(shell.SettingState, any) error) (string, error) {
	name := r.PostFormValue("setting")
	state, found := shell.FindSetting(states, name)
	if !found {
		return "", Refused(fmt.Sprintf("There is no setting %q on this page.", name))
	}
	var value any
	outcome := "saved"
	if r.PostFormValue("action") == "remove" {
		if state.Required() {
			return "", Refused(fmt.Sprintf("%s is required, so it cannot be removed.", name))
		}
		outcome = "removed"
	} else {
		parsed, err := shell.ParseSettingValue(state.Setting, r.PostFormValue("value"))
		if err != nil {
			return "", Refused(err.Error())
		}
		value = parsed
	}
	if err := write(state, value); err != nil {
		return "", Refused(err.Error())
	}
	return page + "?" + outcome + "=" + url.QueryEscape(name) + "#setting-" + url.PathEscape(name), nil
}

// enrolledRepository is the enrolled repository the request's path names.
func enrolledRepository(r *http.Request, read Readings) (shell.RepoConfig, error) {
	repos, _, err := read.SettingsRepositories()
	if err != nil {
		return shell.RepoConfig{}, err
	}
	for _, repo := range repos {
		if repo.Forge == r.PathValue("forge") && repo.Owner == r.PathValue("owner") && repo.Repo == r.PathValue("repo") {
			return repo, nil
		}
	}
	return shell.RepoConfig{}, fmt.Errorf("repository is not enrolled: %w", ErrNotFound)
}
