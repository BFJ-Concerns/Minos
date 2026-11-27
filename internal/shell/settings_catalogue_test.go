package shell

import (
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// wholeSettings are the struct types the catalogue describes as one setting
// each, written whole, rather than field by field.
var wholeSettings = []reflect.Type{
	reflect.TypeFor[AdmissionRules](),
	reflect.TypeFor[Markers](),
	reflect.TypeFor[FilingDestination](),
	reflect.TypeFor[RoleRouting](),
}

// tomlKeys lists the dotted keys a configuration struct decodes, a map's
// entries under "*".
func tomlKeys(t reflect.Type, prefix string) []string {
	var keys []string
	for field := range t.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("toml"), ",")
		if name == "-" {
			continue
		}
		key := prefix + name
		if field.Anonymous {
			key = strings.TrimSuffix(prefix, ".")
		}
		switch {
		case field.Type.Kind() == reflect.Struct && !slices.Contains(wholeSettings, field.Type):
			inner := key + "."
			if key == "" {
				inner = ""
			}
			keys = append(keys, tomlKeys(field.Type, inner)...)
		case field.Type.Kind() == reflect.Map && field.Type.Elem().Kind() == reflect.Struct && !slices.Contains(wholeSettings, field.Type.Elem()):
			keys = append(keys, tomlKeys(field.Type.Elem(), key+".*.")...)
		case field.Type.Kind() == reflect.Map:
			keys = append(keys, key+".*")
		default:
			keys = append(keys, key)
		}
	}
	return keys
}

func TestSettingsCatalogueDescribesEveryKeyTheLoaderAccepts(t *testing.T) {
	want := map[string]bool{}
	for _, key := range tomlKeys(reflect.TypeFor[ServiceConfig](), "") {
		// The service-wide value of a per-repository setting is the
		// repository setting's own entry.
		key = strings.TrimPrefix(key, "repositories.")
		if key == "routing.concerns.*" {
			for _, concern := range specialistConcerns {
				want["routing.concerns."+concern] = true
			}
			continue
		}
		want[key] = true
	}
	for _, key := range tomlKeys(reflect.TypeFor[RepoConfig](), "") {
		switch key {
		case "forge", "owner", "repo":
			// The repository's identity names its page; it is not edited.
		case "routing.concerns.*":
		default:
			want[key] = true
		}
	}
	var wanted []string
	for key := range want {
		wanted = append(wanted, key)
	}
	sort.Strings(wanted)
	if got := catalogueKeys(); !slices.Equal(got, wanted) {
		t.Errorf("the catalogue and the configuration disagree\ncatalogue: %v\nloader:    %v", got, wanted)
	}
}

func TestEveryChoiceDefaultIsOneOfItsChoices(t *testing.T) {
	for _, setting := range settingsCatalogue() {
		if len(setting.Choices) > 0 && !slices.Contains(setting.Choices, setting.Default) {
			t.Errorf("%s defaults to %q, which is not among %v", setting.Name(), setting.Default, setting.Choices)
		}
	}
}

func TestSettingsSayWhereEachValueComesFrom(t *testing.T) {
	root, repo := settingsRoot(t)
	if err := WriteServiceSetting(root, ServiceWideKey([]string{"routing", "verifier"}), map[string]any{"engine": "claude"}); err != nil {
		t.Fatal(err)
	}
	service, err := ServiceSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]struct {
		level SettingLevel
		typed string
	}{
		"service.bot-login":                {SetServiceWide, "minos"},
		"service.status-context":           {SetNowhere, ""},
		"forges.forgejo.api-base":          {SetServiceWide, "https://forge.example"},
		"work-in-progress-branch-prefixes": {SetServiceWide, `["structural/"]`},
		"routing.verifier":                 {SetServiceWide, `{engine = "claude"}`},
		"review.threshold":                 {SetNowhere, ""},
	} {
		state, found := FindSetting(service, name)
		if !found || state.Level != want.level || state.Typed != want.typed {
			t.Errorf("service %s = %+v (found %v), want %s %q", name, state, found, want.level, want.typed)
		}
	}
	if _, found := FindSetting(service, "admission"); !found {
		t.Error("the service-wide admission rules are not listed")
	}
	if _, found := FindSetting(service, "adaptation.run-body"); found {
		t.Error("a repository file's own setting is listed for the service")
	}

	repository, err := RepositorySettings(root, repo)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]struct {
		level       SettingLevel
		typed       string
		serviceWide string
		file        string
	}{
		"admission":                        {SetForRepository, `{labels = ["Ready"], title-pattern = "^(feat|fix)\\b"}`, "", repo.Path},
		"adaptation.run-body":              {SetForRepository, "/opt/minos/run-body/run-body", "", repo.Path},
		"work-in-progress-branch-prefixes": {SetServiceWide, `["structural/"]`, `["structural/"]`, repo.Path},
		"routing.verifier":                 {SetServiceWide, `{engine = "claude"}`, `{engine = "claude"}`, repo.Path},
		"review.threshold":                 {SetNowhere, "", "", repo.Path},
	} {
		state, found := FindSetting(repository, name)
		if !found || state.Level != want.level || state.Typed != want.typed || state.ServiceWide != want.serviceWide || state.File != want.file {
			t.Errorf("repository %s = %+v (found %v), want %s %q over %q in %s", name, state, found, want.level, want.typed, want.serviceWide, want.file)
		}
	}
	if level, err := RepositorySettingLevel(root, repo, []string{"routing", "verifier"}); err != nil || level != SetServiceWide {
		t.Errorf("verifier level = %q (%v), want service-wide", level, err)
	}
}

func TestTypedSettingValuesAreReadByTheirForm(t *testing.T) {
	catalogue := settingsCatalogue()
	setting := func(name string) Setting {
		index := slices.IndexFunc(catalogue, func(setting Setting) bool { return setting.Name() == name })
		if index < 0 {
			t.Fatalf("no setting %s", name)
		}
		return catalogue[index]
	}
	for _, test := range []struct {
		setting string
		typed   string
		want    any
		refused bool
	}{
		{"service.bot-login", "minos bot", "minos bot", false},
		{"runs.max-concurrent", " 4 ", int64(4), false},
		{"runs.max-concurrent", "four", nil, true},
		{"review.threshold", "Medium", "Medium", false},
		{"review.threshold", "Urgent", nil, true},
		{"review.file-safeguard-requests", "true", true, false},
		{"review.file-safeguard-requests", "yes please", nil, true},
		{"work-in-progress-branch-prefixes", `["wip/", 'draft/']`, []any{"wip/", "draft/"}, false},
		{"filing-destination", `{kind = "none"}`, map[string]any{"kind": "none"}, false},
		{"guidance-sources", `[{path = "AGENTS.md"}]`, []any{map[string]any{"path": "AGENTS.md"}}, false},
		{"markers", `{clean = `, nil, true},
	} {
		got, err := ParseSettingValue(setting(test.setting), test.typed)
		if test.refused {
			if err == nil || !strings.Contains(err.Error(), test.setting) {
				t.Errorf("%s %q: error = %v, want a refusal naming the setting", test.setting, test.typed, err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s %q = %#v (%v), want %#v", test.setting, test.typed, got, err, test.want)
		}
	}
}

func TestTypedListOfTablesIsWrittenAsTheLoaderReadsIt(t *testing.T) {
	root, repo := settingsRoot(t)
	value, err := ParseSettingValue(Setting{Key: []string{"guidance-sources"}, Form: TOMLForm}, `[{path = "AGENTS.md"}, {repository = "bfj/annexe", path = "README.md"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteRepositorySetting(root, repo, []string{"guidance-sources"}, value); err != nil {
		t.Fatal(err)
	}
	if got := loadSettingsRepo(t, root).GuidanceSources; len(got) != 2 || got[1] != (GuidanceSource{Repository: "bfj/annexe", Path: "README.md"}) {
		t.Fatalf("guidance sources = %+v", got)
	}
}
