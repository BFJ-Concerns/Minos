package webpage

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryDescriptionPageReadsKeptRecord(t *testing.T) {
	root, read, write := configurationRoot(t)
	servicePath := filepath.Join(root, "service.toml")
	runsDir := filepath.Join(root, "runs")
	service := strings.Replace(fileText(t, servicePath), "/var/lib/minos/runs", runsDir, 1)
	if err := os.WriteFile(servicePath, []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}
	records := filepath.Join(runsDir, ".descriptions")
	if err := os.MkdirAll(records, 0o700); err != nil {
		t.Fatal(err)
	}
	const page = "/repositories/forgejo/bfj/estate/description"
	site, _ := testSite(t, read, write)
	if response := visit(site, page, nil); response.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated description answered %d", response.Code)
	}
	session := signIn(t, site)
	overview := visit(site, "/", session)
	assertMonitoringPage(t, overview.Code, overview.Body.String(), `href="`+page+`"`)
	empty := visit(site, page, session)
	assertMonitoringPage(t, empty.Code, empty.Body.String(), "Description of bfj/estate", "Minos has no description for this repository.")

	path := filepath.Join(records, "forgejo%2Fbfj%2Festate.json")
	record := `{"kind":"minos-repository-description-v1","commit":"aabbcc","written_at":"2026-11-27T08:40:00Z","description":"## Layout\n\n<script>alert('test')</script>"}`
	if err := os.WriteFile(path, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	kept := visit(site, page, session)
	assertMonitoringPage(t, kept.Code, kept.Body.String(), "aabbcc", `datetime="2026-11-27T08:40:00Z"`,
		"## Layout\n\n&lt;script&gt;")
	if strings.Contains(kept.Body.String(), "<script>") {
		t.Fatal("description rendered active HTML")
	}

	missing := visit(site, "/repositories/forgejo/bfj/unknown/description", session)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown repository answered %d", missing.Code)
	}
}
