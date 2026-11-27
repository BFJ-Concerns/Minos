package shell

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

// Each adaptation answers default-branch-tip from the repository's own
// default branch: it asks for the branch the repository names, and reports
// that branch's commit, or none for a repository with no commit there.
func TestDefaultBranchTipReadsTheDefaultBranchOfEachForge(t *testing.T) {
	const tip = "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff"
	forgejoAdaptation, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	var askedBranch string
	empty := false
	forgejo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token forgejo-secret-311" {
			http.Error(w, "missing fixture credential", http.StatusUnauthorized)
			return
		}
		switch r.URL.EscapedPath() {
		case "/api/v1/repos/acme/widgets":
			writeFixtureJSON(t, w, map[string]any{"clone_url": "https://forgejo.example/acme/widgets.git", "default_branch": "release/2"})
		case "/api/v1/repos/acme/widgets/branches/release%2F2":
			askedBranch = "release/2"
			if empty {
				http.NotFound(w, r)
				return
			}
			writeFixtureJSON(t, w, map[string]any{"name": "release/2", "commit": map[string]any{"id": tip}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer forgejo.Close()
	forgejoForge, err := forge.NewAdapter(forge.ScriptRunner{Directory: forgejoAdaptation, APIBase: forgejo.URL, Credential: "forgejo-secret-311"}, "minos", "Minos") // gitleaks:allow (local HTTP fixture)
	if err != nil {
		t.Fatal(err)
	}
	repository := forge.Repository{Owner: "acme", Name: "widgets"}

	got, err := forgejoForge.DefaultBranchTip(t.Context(), repository)
	if err != nil || got != (forge.BranchTip{DefaultBranch: "release/2", Commit: tip}) || askedBranch != "release/2" {
		t.Fatalf("forgejo tip = %+v, error %v, asked for %q", got, err, askedBranch)
	}
	empty = true
	got, err = forgejoForge.DefaultBranchTip(t.Context(), repository)
	if err != nil || got != (forge.BranchTip{DefaultBranch: "release/2"}) {
		t.Fatalf("forgejo tip of an empty repository = %+v, error %v; want the branch and no commit", got, err)
	}

	github := newGitHubFixtureState(t)
	got, err = github.adapter(t).DefaultBranchTip(t.Context(), repository)
	if err != nil || got != (forge.BranchTip{DefaultBranch: "trunk", Commit: github.baseTip}) {
		t.Fatalf("github tip = %+v, error %v", got, err)
	}
}
