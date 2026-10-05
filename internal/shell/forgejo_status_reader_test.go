package shell

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

// The fixture serves raw forge responses and records actual branch reads;
// all projections and refusal decisions come from the shipped scripts.
type statusReaderFixture struct {
	runner       forge.ScriptRunner
	mu           sync.Mutex
	branches     []string
	refuseSource bool
}

func newStatusReaderFixture(t *testing.T, pages []string, repeat bool, refuseSource bool) *statusReaderFixture {
	t.Helper()
	f := &statusReaderFixture{refuseSource: refuseSource}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := "[]"
		switch r.URL.Path {
		case "/api/v1/user":
			response = `{"login":"Minos"}`
		case "/api/v1/repos/owner/repo":
			response = `{"full_name":"owner/repo"}`
		case "/api/v1/repos/owner/repo/pulls/17":
			response = `{"number":17,"state":"open","merged":false,"user":{"login":"author"},"head":{"sha":"head","ref":"topic","repo":{"full_name":"owner/repo"}},"base":{"ref":"main","repo":{"full_name":"owner/repo"}}}`
		case "/api/v1/repos/owner/repo/branches/main", "/api/v1/repos/owner/repo/branches/topic":
			f.mu.Lock()
			f.branches = append(f.branches, r.URL.Path)
			f.mu.Unlock()
			if f.refuseSource && strings.HasSuffix(r.URL.Path, "/topic") {
				http.Error(w, "source branch unavailable", http.StatusForbidden)
				return
			}
			response = `{"commit":{"id":"target"}}`
		case "/api/v1/repos/owner/repo/commits/head/statuses":
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if repeat && page > len(pages) {
				page = len(pages)
			}
			if page > 0 && page <= len(pages) {
				response = pages[page-1]
			}
		case "/api/v1/repos/owner/repo/issues/17/dependencies", "/api/v1/repos/owner/repo/pulls/17/reviews":
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	directory, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	f.runner = forge.ScriptRunner{Directory: directory, APIBase: server.URL, Credential: "fixture-token"}
	return f
}

func TestForgejoStatusReaderProjectsBothEntrypoints(t *testing.T) {
	pages := []string{
		`[{"id":2,"context":"ci","status":"failure","creator":{"username":"legacy"}},{"id":9,"context":"lint","state":"success","creator":{"login":"bot"},"description":"checked","target_url":"https://ci.invalid/9"}]`,
		`[{"id":4,"context":"build","status":"pending"}]`,
	}
	want := []forge.Status{
		{ID: 9, Provider: "forgejo", Context: "lint", State: "success", Creator: "bot", Description: "checked", TargetURL: "https://ci.invalid/9"},
		{ID: 4, Provider: "forgejo", Context: "build", State: "pending"},
		{ID: 2, Provider: "forgejo", Context: "ci", State: "failure", Creator: "legacy"},
	}
	for _, test := range []struct {
		name   string
		pages  []string
		repeat bool
		want   []forge.Status
	}{
		{"paged", pages, false, want}, {"repeated", pages, true, want}, {"empty", nil, false, []forge.Status{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newStatusReaderFixture(t, test.pages, test.repeat, false)
			adapter, err := forge.NewAdapter(f.runner, "Minos", "Minos")
			if err != nil {
				t.Fatal(err)
			}
			repo := forge.Repository{Owner: "owner", Name: "repo"}
			snapshot, err := adapter.Snapshot(t.Context(), repo, 17)
			if err != nil {
				t.Fatal(err)
			}
			statuses, err := adapter.CommitStatuses(t.Context(), repo, "head")
			if err != nil {
				t.Fatal(err)
			}
			for name, got := range map[string][]forge.Status{"snapshot": snapshot.Statuses, "commit-statuses": statuses} {
				t.Run(name, func(t *testing.T) {
					if !reflect.DeepEqual(got, test.want) {
						t.Fatalf("projection = %#v, want %#v", got, test.want)
					}
				})
			}
		})
	}
}

func TestForgejoStatusReaderRejectsInvalidIDsOnBothEntrypoints(t *testing.T) {
	for _, test := range []struct{ name, status string }{
		{"missing", `{"context":"ci","status":"success"}`},
		{"zero", `{"id":0,"context":"ci","status":"success"}`},
		{"negative", `{"id":-1,"context":"ci","status":"success"}`},
		{"string", `{"id":"3","context":"ci","status":"success"}`},
	} {
		for _, operation := range []string{"snapshot", "commit-statuses"} {
			t.Run(test.name+"/"+operation, func(t *testing.T) {
				f := newStatusReaderFixture(t, []string{"[" + test.status + "]"}, false, false)
				args := []string{"owner", "repo", "head"}
				if operation == "snapshot" {
					args[2] = "17"
				}
				_, err := f.runner.Run(t.Context(), forge.RunRequest{Operation: operation, Arguments: args})
				if err == nil || !strings.Contains(err.Error(), "Forgejo status is missing a positive id") {
					t.Fatalf("%s accepted invalid status id or returned wrong error: %v", operation, err)
				}
			})
		}
	}
}
