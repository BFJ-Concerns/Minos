package webpage

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/shell"
)

// configurationRoot is a configuration directory as a deployer keeps one,
// with one enrolled repository, read and written through the service's own
// loader and settings writer.
func configurationRoot(t *testing.T) (string, Readings, Settings) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"service.toml": `[service]
bot-login = "minos"

[listener]
bind = ":8919"

[forges.forgejo]
adaptation = "/opt/minos/adaptations/forgejo"
api-base = "https://forge.example"
webhook-secret-file = "/etc/minos/webhook.secret"
credential-file = "/etc/minos/forgejo.token"

# Long-lived branches everywhere.
[repositories]
work-in-progress-branch-prefixes = ["structural/"]

[runs]
dir = "/var/lib/minos/runs"
`,
		"repos/estate.toml": `forge = "forgejo"
owner = "bfj"
repo = "estate"

[review]
threshold = "Low" # the team want everything

[adaptation]
run-body = "/opt/minos/run-body/run-body"
`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := Readings{
		Configuration: func() (shell.ServiceConfig, []shell.RepoConfig, error) {
			cfg, err := shell.LoadServiceConfig(root)
			if err != nil {
				return shell.ServiceConfig{}, nil, err
			}
			repos, err := shell.LoadRepoConfigs(cfg)
			return cfg, repos, err
		},
		ServiceSettings:    func() ([]shell.SettingState, error) { return shell.ServiceSettings(root) },
		RepositorySettings: func(repo shell.RepoConfig) ([]shell.SettingState, error) { return shell.RepositorySettings(root, repo) },
	}
	return root, read, SettingsAt(root)
}

// followed is where a redirect sends the browser, as the browser asks for it:
// without the fragment, which stays in the browser.
func followed(response *httptest.ResponseRecorder) string {
	location, _, _ := strings.Cut(response.Header().Get("Location"), "#")
	return location
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// settingRowHTML is the part of a settings page that shows the named setting.
func settingRowHTML(t *testing.T, body, name string) string {
	t.Helper()
	start := strings.Index(body, `id="setting-`+name+`"`)
	if start < 0 {
		t.Fatalf("the page shows no setting %s:\n%s", name, body)
	}
	end := strings.Index(body[start:], "</details>")
	return body[start : start+end]
}

func TestSettingsPagesShowWhereEachValueComesFrom(t *testing.T) {
	_, read, write := configurationRoot(t)
	site, _ := testSite(t, read, write)
	session := signIn(t, site)

	service := visit(site, "/settings", session)
	if service.Code != http.StatusOK {
		t.Fatalf("the service's settings answered %d:\n%s", service.Code, service.Body)
	}
	body := service.Body.String()
	if !strings.Contains(body, `<a href="/settings" aria-current="page">Settings</a>`) {
		t.Error("the navigation does not lead to the settings")
	}
	for name, shows := range map[string][]string{
		"service.bot-login":                {"service.toml", `value="minos"`},
		"service.status-context":           {">default<", "Minos"},
		"forges.forgejo.api-base":          {"https://forge.example"},
		"work-in-progress-branch-prefixes": {"service.toml, for every repository that sets none", "[&#34;structural/&#34;]"},
		"routing.verifier":                 {">default<", "The shipped pairing"},
		"listener.bind":                    {"restarted"},
	} {
		row := settingRowHTML(t, body, name)
		for _, want := range shows {
			if !strings.Contains(row, want) {
				t.Errorf("service %s does not show %q:\n%s", name, want, row)
			}
		}
	}
	if !strings.Contains(body, `href="/repositories/forgejo/bfj/estate/settings"`) {
		t.Error("the service's settings do not lead to the repository's")
	}

	repository := visit(site, "/repositories/forgejo/bfj/estate/settings", session)
	if repository.Code != http.StatusOK {
		t.Fatalf("the repository's settings answered %d:\n%s", repository.Code, repository.Body)
	}
	body = repository.Body.String()
	for name, shows := range map[string][]string{
		"review.threshold":                 {"this repository&#39;s estate.toml", "<option selected>Low</option>", "Remove, so the default applies"},
		"work-in-progress-branch-prefixes": {"service-wide, in service.toml", "[&#34;structural/&#34;]", "Set it here"},
		"filing-destination":               {">default<", "pull-request-comment"},
	} {
		row := settingRowHTML(t, body, name)
		for _, want := range shows {
			if !strings.Contains(row, want) {
				t.Errorf("repository %s does not show %q:\n%s", name, want, row)
			}
		}
	}
	if strings.Contains(settingRowHTML(t, body, "adaptation.run-body"), `value="remove"`) {
		t.Error("a required setting offers to be removed")
	}
	if missing := visit(site, "/repositories/forgejo/bfj/elsewhere/settings", session); missing.Code != http.StatusNotFound {
		t.Errorf("an unenrolled repository's settings answered %d, want 404", missing.Code)
	}
}

func TestRepositorySettingChangedOnThePageIsWrittenToItsFile(t *testing.T) {
	root, read, write := configurationRoot(t)
	site, _ := testSite(t, read, write)
	session := signIn(t, site)
	page := "/repositories/forgejo/bfj/estate/settings"
	path := filepath.Join(root, "repos", "estate.toml")

	saved := submit(site, page, url.Values{"setting": {"review.threshold"}, "value": {"Medium"}, "action": {"set"}}, session)
	if saved.Code != http.StatusSeeOther || saved.Header().Get("Location") != page+"?saved=review.threshold#setting-review.threshold" {
		t.Fatalf("a saved change answered %d to %q:\n%s", saved.Code, saved.Header().Get("Location"), saved.Body)
	}
	if got := fileText(t, path); !strings.Contains(got, `threshold = "Medium" # the team want everything`) {
		t.Errorf("the repository file does not hold the change beside the deployer's comment:\n%s", got)
	}
	shown := visit(site, followed(saved), session).Body.String()
	if !strings.Contains(shown, "Saved review.threshold in "+path+". It takes effect from the next run.") {
		t.Errorf("the page does not say the change was saved:\n%s", shown)
	}

	override := submit(site, page, url.Values{"setting": {"work-in-progress-branch-prefixes"}, "value": {`["wip/"]`}, "action": {"set"}}, session)
	if override.Code != http.StatusSeeOther {
		t.Fatalf("an override answered %d:\n%s", override.Code, override.Body)
	}
	row := settingRowHTML(t, visit(site, page, session).Body.String(), "work-in-progress-branch-prefixes")
	if !strings.Contains(row, "this repository&#39;s estate.toml") || !strings.Contains(row, "which this repository's value overrides") {
		t.Errorf("the override is not shown over the service-wide value:\n%s", row)
	}

	removed := submit(site, page, url.Values{"setting": {"work-in-progress-branch-prefixes"}, "action": {"remove"}}, session)
	if removed.Code != http.StatusSeeOther {
		t.Fatalf("a removal answered %d:\n%s", removed.Code, removed.Body)
	}
	if got := fileText(t, path); strings.Contains(got, "wip/") {
		t.Errorf("the removed override is still in the file:\n%s", got)
	}
	if shown := visit(site, followed(removed), session).Body.String(); !strings.Contains(shown, "so the service-wide value applies") {
		t.Errorf("the page does not say what applies after the removal:\n%s", shown)
	}
}

func TestSettingTheLoaderRefusesIsShownBackAndNothingWritten(t *testing.T) {
	root, read, write := configurationRoot(t)
	site, _ := testSite(t, read, write)
	session := signIn(t, site)
	service := filepath.Join(root, "service.toml")
	before := fileText(t, service)

	for _, refused := range []struct {
		form url.Values
		says string
	}{
		{url.Values{"setting": {"runs.max-concurrent"}, "value": {"64"}, "action": {"set"}}, "max-concurrent must be between 1 and 8"},
		{url.Values{"setting": {"runs.max-concurrent"}, "value": {"several"}, "action": {"set"}}, "runs.max-concurrent takes a whole number"},
		{url.Values{"setting": {"service.bot-login"}, "action": {"remove"}}, "service.bot-login is required"},
		{url.Values{"setting": {"filing-destination"}, "value": {`{kind = "file"`}, "action": {"set"}}, "is not a TOML value"},
		{url.Values{"setting": {"runs.unknown"}, "value": {"1"}, "action": {"set"}}, "There is no setting"},
	} {
		response := submit(site, "/settings", refused.form, session)
		body := response.Body.String()
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(body, refused.says) {
			t.Errorf("%v answered %d, want the refusal %q:\n%s", refused.form, response.Code, refused.says, body)
			continue
		}
		if name := refused.form.Get("setting"); name == "runs.max-concurrent" {
			row := settingRowHTML(t, body, name)
			if !strings.HasPrefix(row, `id="setting-runs.max-concurrent" open>`) || !strings.Contains(row, `aria-invalid="true"`) {
				t.Errorf("the refused setting is not shown open with what was typed:\n%s", row)
			}
			if !strings.Contains(row, `value="`+refused.form.Get("value")+`"`) {
				t.Errorf("the refused value is not kept in its field:\n%s", row)
			}
		}
	}
	if after := fileText(t, service); after != before {
		t.Errorf("a refused change reached service.toml:\n%s", after)
	}
}

func TestServiceWideRoutingChangedOnThePageReachesEveryRepository(t *testing.T) {
	root, read, write := configurationRoot(t)
	site, _ := testSite(t, read, write)
	session := signIn(t, site)
	response := submit(site, "/settings", url.Values{"setting": {"routing.verifier"}, "value": {`{engine = "claude", effort = "high"}`}, "action": {"set"}}, session)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("a service-wide route answered %d:\n%s", response.Code, response.Body)
	}
	if got := fileText(t, filepath.Join(root, "service.toml")); !strings.Contains(got, "[routing.verifier]\neffort = \"high\"\nengine = \"claude\"\n") || strings.Contains(got, "repositories.routing") {
		t.Errorf("service.toml does not hold the route at its root:\n%s", got)
	}
	_, repos, err := read.Configuration()
	if err != nil {
		t.Fatal(err)
	}
	if got := repos[0].Routing.Verifier; got != (shell.RoleRouting{Engine: "claude", Effort: "high"}) {
		t.Errorf("the repository's verifier routing = %+v, want the service-wide route", got)
	}
	row := settingRowHTML(t, visit(site, "/repositories/forgejo/bfj/estate/settings", session).Body.String(), "routing.verifier")
	if !strings.Contains(row, "service-wide, in service.toml") {
		t.Errorf("the repository does not show the route as service-wide:\n%s", row)
	}
}

func TestChoiceAtItsDefaultOpensOnTheDefault(t *testing.T) {
	_, read, write := configurationRoot(t)
	site, _ := testSite(t, read, write)
	session := signIn(t, site)
	row := settingRowHTML(t, visit(site, "/settings", session).Body.String(), "review.threshold")
	if !strings.Contains(row, "<option selected>High</option>") || strings.Contains(row, "<option selected>Critical</option>") {
		t.Errorf("the threshold at its default does not open on High:\n%s", row)
	}
}

func TestChoiceSetEmptyOpensOnTheDefaultItResolvesTo(t *testing.T) {
	root, read, write := configurationRoot(t)
	path := filepath.Join(root, "repos", "estate.toml")
	if err := os.WriteFile(path, []byte(strings.Replace(fileText(t, path), `threshold = "Low"`, `threshold = ""`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	site, _ := testSite(t, read, write)
	session := signIn(t, site)
	row := settingRowHTML(t, visit(site, "/repositories/forgejo/bfj/estate/settings", session).Body.String(), "review.threshold")
	if !strings.Contains(row, "<option selected>High</option>") || strings.Contains(row, "<option selected>Critical</option>") {
		t.Errorf("an empty threshold, which the loader resolves to High, does not open on High:\n%s", row)
	}
}

func TestSwitchSetOnThePageIsWrittenAsABoolean(t *testing.T) {
	root, read, write := configurationRoot(t)
	site, _ := testSite(t, read, write)
	session := signIn(t, site)
	page := "/repositories/forgejo/bfj/estate/settings"
	if row := settingRowHTML(t, visit(site, page, session).Body.String(), "review.file-safeguard-requests"); !strings.Contains(row, "<option selected>false</option>") {
		t.Errorf("the switch at its default does not open on false:\n%s", row)
	}
	response := submit(site, page, url.Values{"setting": {"review.file-safeguard-requests"}, "value": {"true"}, "action": {"set"}}, session)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("turning the switch on answered %d:\n%s", response.Code, response.Body)
	}
	if got := fileText(t, filepath.Join(root, "repos", "estate.toml")); !strings.Contains(got, "file-safeguard-requests = true\n") {
		t.Errorf("the repository file does not hold the switch as a boolean:\n%s", got)
	}
}

func TestOneEngineConcurrencySetFromTheDefaultsIsSaved(t *testing.T) {
	root, read, write := configurationRoot(t)
	site, _ := testSite(t, read, write)
	session := signIn(t, site)
	response := submit(site, "/settings", url.Values{"setting": {"ensemble.concurrency-claude"}, "value": {"3"}, "action": {"set"}}, session)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("setting Claude's concurrency alone answered %d:\n%s", response.Code, response.Body)
	}
	cfg, err := shell.LoadServiceConfig(root)
	if err != nil || cfg.Ensemble.ConcurrencyClaude != 3 || cfg.Ensemble.ConcurrencyCodex != 2 {
		t.Fatalf("concurrency = %d/%d (%v), want 3 and the default 2", cfg.Ensemble.ConcurrencyClaude, cfg.Ensemble.ConcurrencyCodex, err)
	}
}
