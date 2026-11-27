package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// settingsRoot is a configuration root as a deployer keeps one: comments,
// their own layout, and a literal string the encoder would spell otherwise.
func settingsRoot(t *testing.T) (string, RepoConfig) {
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

# Every repository parks its long-lived branches here.
[repositories]
work-in-progress-branch-prefixes = ["structural/"]

[runs]
dir = "/var/lib/minos/runs"
`,
		"repos/estate.toml": `forge = "forgejo"
owner = "bfj"
repo = "estate"

# The team label a pull request when it is ready.
[admission]
labels = ["Ready"]        # agreed in October
title-pattern = '^(feat|fix)\b'

[adaptation]
run-body = "/opt/minos/run-body/run-body"
`,
	} {
		// The mode is set apart from the write, which the umask narrows.
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return root, loadSettingsRepo(t, root)
}

func loadSettingsRepo(t *testing.T, root string) RepoConfig {
	t.Helper()
	cfg, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	repos, err := LoadRepoConfigs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Fatalf("repositories = %d, want one", len(repos))
	}
	return repos[0]
}

func readSettingsFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestRepositorySettingIsWrittenBesideTheDeployersOwnText(t *testing.T) {
	root, repo := settingsRoot(t)
	rules := repo.Admission
	rules.ExcludeLabels = []string{"Blocked"}
	if err := WriteRepositorySetting(root, repo, []string{"admission"}, rules); err != nil {
		t.Fatal(err)
	}
	if err := WriteRepositorySetting(root, repo, []string{"review", "threshold"}, "Medium"); err != nil {
		t.Fatal(err)
	}

	written := readSettingsFile(t, repo.Path)
	for _, kept := range []string{
		"# The team label a pull request when it is ready.\n[admission]\n",
		`labels = ["Ready"]        # agreed in October`,
		`title-pattern = '^(feat|fix)\b'`,
		"[review]\nthreshold = \"Medium\"\n",
	} {
		if !strings.Contains(written, kept) {
			t.Errorf("the file lost or lacks %q:\n%s", kept, written)
		}
	}
	reloaded := loadSettingsRepo(t, root)
	if got := reloaded.Admission.ExcludeLabels; len(got) != 1 || got[0] != "Blocked" {
		t.Errorf("exclude-labels = %v, want [Blocked]", got)
	}
	if reloaded.Review.Threshold != "Medium" {
		t.Errorf("threshold = %q, want Medium", reloaded.Review.Threshold)
	}
	if info, err := os.Stat(repo.Path); err != nil || info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v (%v), want the file's own 0640", info.Mode().Perm(), err)
	}
}

func TestRemovedRepositorySettingFallsBackToTheServiceWideValue(t *testing.T) {
	root, repo := settingsRoot(t)
	key := []string{"work-in-progress-branch-prefixes"}
	if err := WriteRepositorySetting(root, repo, key, []string{}); err != nil {
		t.Fatal(err)
	}
	if level, err := RepositorySettingLevel(root, repo, key); err != nil || level != SetForRepository {
		t.Fatalf("level = %q (%v), want the repository's own", level, err)
	}
	if got := loadSettingsRepo(t, root).WorkInProgressBranchPrefixes; len(got) != 0 {
		t.Fatalf("prefixes = %v, want the repository's empty list", got)
	}

	if err := WriteRepositorySetting(root, repo, key, nil); err != nil {
		t.Fatal(err)
	}
	if level, err := RepositorySettingLevel(root, repo, key); err != nil || level != SetServiceWide {
		t.Fatalf("level = %q (%v), want service-wide", level, err)
	}
	if got := loadSettingsRepo(t, root).WorkInProgressBranchPrefixes; len(got) != 1 || got[0] != "structural/" {
		t.Fatalf("prefixes = %v, want the service-wide [structural/]", got)
	}

	if err := WriteServiceSetting(root, append([]string{"repositories"}, key...), nil); err != nil {
		t.Fatal(err)
	}
	if level, err := RepositorySettingLevel(root, repo, key); err != nil || level != SetNowhere {
		t.Fatalf("level = %q (%v), want the default", level, err)
	}
	if service := readSettingsFile(t, filepath.Join(root, "service.toml")); !strings.Contains(service, "# Every repository parks its long-lived branches here.\n[repositories]\n") {
		t.Errorf("service.toml lost the deployer's comment or table:\n%s", service)
	}
}

func TestArrayOfTablesSettingIsReplacedWhole(t *testing.T) {
	root, repo := settingsRoot(t)
	key := []string{"guidance-sources"}
	for _, sources := range [][]GuidanceSource{
		{{Path: "AGENTS.md"}, {Repository: "bfj/estate-annexe", Path: "README.md"}},
		{{Path: "docs/intent.md"}},
	} {
		if err := WriteRepositorySetting(root, repo, key, sources); err != nil {
			t.Fatal(err)
		}
		got := loadSettingsRepo(t, root).GuidanceSources
		if len(got) != len(sources) || got[0] != sources[0] {
			t.Fatalf("guidance sources = %v, want %v", got, sources)
		}
	}
	if written := readSettingsFile(t, repo.Path); strings.Count(written, "[[guidance-sources]]") != 1 {
		t.Errorf("want one guidance-sources block:\n%s", written)
	}
}

func TestSettingThatWouldNotLoadIsRefusedAndNothingWritten(t *testing.T) {
	root, repo := settingsRoot(t)
	before := readSettingsFile(t, repo.Path)
	err := WriteRepositorySetting(root, repo, []string{"review", "threshold"}, "Urgent")
	if err == nil || !strings.Contains(err.Error(), "review.threshold must be") || !strings.Contains(err.Error(), repo.Path) {
		t.Fatalf("error = %v, want the loader's refusal naming the repository file", err)
	}
	if after := readSettingsFile(t, repo.Path); after != before {
		t.Errorf("the refused change reached the file:\n%s", after)
	}
	leftovers, _ := filepath.Glob(filepath.Join(root, "repos", ".*"))
	if len(leftovers) != 0 {
		t.Errorf("candidate files left behind: %v", leftovers)
	}
}

func TestEditTOMLKeepsEveryOtherByte(t *testing.T) {
	for name, test := range map[string]struct {
		text  string
		key   []string
		value any
		want  string
	}{
		"a value changes where it stands, its comment kept": {
			text:  "a = 1 # one\nb = 2\n",
			key:   []string{"a"},
			value: 3,
			want:  "a = 3 # one\nb = 2\n",
		},
		"a key inside an inline table changes within its braces": {
			text:  "[markers]\nclean = {reaction = \"+1\"}\n",
			key:   []string{"markers", "clean", "reaction"},
			value: "heart",
			want:  "[markers]\nclean = {reaction = \"heart\"}\n",
		},
		"a new root key goes before the first table": {
			text:  "a = 1\n\n# the table\n[t]\nb = 2\n",
			key:   []string{"c"},
			value: []string{"x"},
			want:  "a = 1\nc = [\"x\"]\n\n# the table\n[t]\nb = 2\n",
		},
		"a removed table takes its header and leaves its comment": {
			text:  "a = 1\n# the table\n[t]\nb = 2\n[u]\nc = 3\n",
			key:   []string{"t"},
			value: nil,
			want:  "a = 1\n# the table\n[u]\nc = 3\n",
		},
		"an empty table is still defined": {
			text:  "a = 1\n",
			key:   []string{"t"},
			value: map[string]any{},
			want:  "a = 1\n\n[t]\n",
		},
		"a multi-line array is replaced whole": {
			text:  "a = [\n  \"x\", # first\n  \"y\",\n]\nb = 2\n",
			key:   []string{"a"},
			value: []string{"z"},
			want:  "a = [\"z\"]\nb = 2\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := editTOML(test.text, test.key, test.value)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, test.want)
			}
		})
	}
}

func TestEditTOMLLeavesWhatTheEditDidNotAskToChange(t *testing.T) {
	for name, test := range map[string]struct {
		text  string
		key   []string
		value any
		want  string
	}{
		"a removed key leaves the comment beside it": {
			text:  "a = 1 # kept for the audit\nb = 2\n",
			key:   []string{"a"},
			value: nil,
			want:  "# kept for the audit\nb = 2\n",
		},
		"a key dropped from a table leaves the comment beside it": {
			text:  "[t]\na = 1 # why a\nb = 2\n",
			key:   []string{"t"},
			value: map[string]any{"b": 2},
			want:  "[t]\n# why a\nb = 2\n",
		},
		"one field of an inline table changes alone": {
			text:  "[markers]\nclean = { reaction = '+1', label = \"x\" }\n",
			key:   []string{"markers", "clean", "reaction"},
			value: "heart",
			want:  "[markers]\nclean = { reaction = \"heart\", label = \"x\" }\n",
		},
		"a field leaves an inline table and the rest stay as written": {
			text:  "m = {b = 'two', a = 1}\n",
			key:   []string{"m"},
			value: map[string]any{"b": "two"},
			want:  "m = {b = 'two'}\n",
		},
		"a field joins an inline table after the others": {
			text:  "m = {b = 'two'}\n",
			key:   []string{"m", "a"},
			value: 1,
			want:  "m = {b = 'two', a = 1}\n",
		},
		"a changed name of dotted fields leaves the other names as written": {
			text:  "routing = { proposer.engine = 'codex', verifier.engine = 'claude' }\n",
			key:   []string{"routing", "proposer"},
			value: map[string]any{"engine": "claude"},
			want:  "routing = { proposer = {engine = \"claude\"}, verifier.engine = 'claude' }\n",
		},
		"a name of several dotted fields changes once where it first stood": {
			text:  "r = { p.engine = 'codex', v = 1, p.effort = 'low' }\n",
			key:   []string{"r", "p"},
			value: map[string]any{"engine": "claude"},
			want:  "r = { p = {engine = \"claude\"}, v = 1 }\n",
		},
		"each separator between fields stays as written": {
			text:  "routing = { proposer.engine = 'codex',  verifier.engine = 'claude',\tjudgement.engine = 'claude' }\n",
			key:   []string{"routing", "proposer"},
			value: map[string]any{"engine": "claude"},
			want:  "routing = { proposer = {engine = \"claude\"},  verifier.engine = 'claude',\tjudgement.engine = 'claude' }\n",
		},
		"a field joins after the last separator written": {
			text:  "m = {a = 1,b = 2,\tc = 3}\n",
			key:   []string{"m", "d"},
			value: 4,
			want:  "m = {a = 1,b = 2,\tc = 3,\td = 4}\n",
		},
		"a file without a final newline keeps none": {
			text:  "a = 1",
			key:   []string{"a"},
			value: 2,
			want:  "a = 2",
		},
		"an edit that changes nothing changes no byte": {
			text:  "a = 1",
			key:   []string{"a"},
			value: 1,
			want:  "a = 1",
		},
		"the last dotted key of a table goes with the table": {
			text:  "forge = \"f\"\nreview.threshold = \"Low\"\n",
			key:   []string{"review", "threshold"},
			value: nil,
			want:  "forge = \"f\"\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := editTOML(test.text, test.key, test.value)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, test.want)
			}
		})
	}
}

func TestServiceWideChangeThatWouldNotLoadIsRefusedAndNothingWritten(t *testing.T) {
	root, _ := settingsRoot(t)
	path := filepath.Join(root, "service.toml")
	before := readSettingsFile(t, path)
	for _, change := range []struct {
		key   []string
		value any
		want  string
	}{
		{[]string{"repositories", "review", "threshold"}, "Urgent", "review.threshold must be"},
		{[]string{"service", "bot-login"}, nil, "bot login"},
		{[]string{"runs", "max-concurrent"}, 64, "max-concurrent must be"},
	} {
		err := WriteServiceSetting(root, change.key, change.value)
		if err == nil || !strings.Contains(err.Error(), change.want) {
			t.Errorf("%v = %v: error = %v, want the loader's refusal %q", change.key, change.value, err, change.want)
		}
	}
	if after := readSettingsFile(t, path); after != before {
		t.Errorf("a refused change reached service.toml:\n%s", after)
	}
}

func TestServiceWideRoutingIsWrittenAtTheRootOfServiceTOML(t *testing.T) {
	root, repo := settingsRoot(t)
	key := []string{"routing", "verifier"}
	if got := ServiceWideKey(key); strings.Join(got, ".") != "routing.verifier" {
		t.Fatalf("service-wide key = %v, want routing.verifier", got)
	}
	if got := ServiceWideKey([]string{"admission"}); strings.Join(got, ".") != "repositories.admission" {
		t.Fatalf("service-wide key = %v, want repositories.admission", got)
	}
	if err := WriteServiceSetting(root, ServiceWideKey(key), RoleRouting{Engine: "claude", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	if level, err := RepositorySettingLevel(root, repo, key); err != nil || level != SetServiceWide {
		t.Fatalf("level = %q (%v), want service-wide", level, err)
	}
	if got := loadSettingsRepo(t, root).Routing.Verifier; got != (RoleRouting{Engine: "claude", Effort: "high"}) {
		t.Fatalf("verifier routing = %+v, want the service-wide route", got)
	}
}
