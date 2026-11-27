// Package webpage is the web page the service serves its operator: every
// screen of it, the one sign-in in front of them, and the layout and
// stylesheet they share.
//
// The page shows state the service already keeps and writes settings to the
// configuration the deployer edits by hand. It imports the service and the
// service does not import it: nothing a review does waits on, reads or
// depends on anything here.
//
// A screen is a Page: one Go file declaring it, one template under
// templates/, and one line in pages. A Page reads through the Readings it is
// handed and writes through the Settings, and reaches for nothing else, so
// its test hands it both and needs no systemd, forge or archive host.
// overview.go is the example to copy.
package webpage

import (
	"errors"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/shell"
)

// A Page is one screen of the web page.
type Page struct {
	// Path is where the page is served, as a ServeMux pattern without its
	// method: "/queue", "/repositories/{forge}/{owner}/{repo}". The overview
	// holds "/{$}". The listener's own routes (/hooks/, /status,
	// /runs/recent) are taken.
	Path string
	// Title names the page in the browser tab and, when Nav is set, in the
	// navigation every page carries. A page reached from another page, one
	// repository's settings say, leaves Nav unset.
	Title string
	Nav   bool
	// Template is the page's file under templates/. It defines "content",
	// which the layout places and hands whatever Show returned.
	Template string
	// Refresh, when set, has the browser reload the page at that interval:
	// how a page showing live state stays current with no script.
	Refresh time.Duration
	// Show gathers what the page shows for one request.
	Show func(r *http.Request, read Readings) (any, error)
	// Change is set by a page with a form and carries out a submitted one.
	// It returns the path to show next, which the browser is redirected to
	// so that reloading it repeats nothing. A Refused error shows the page
	// again, by Show with the submitted form still on the request, and its
	// message above the form.
	Change func(r *http.Request, read Readings, write Settings) (next string, err error)
}

// pages lists every page, in the order the navigation shows them. A new page
// is one line here.
var pages = []Page{
	overviewPage,
	queuePage,
	livePage,
	recentPage,
	findingsPage,
}

// ErrNotFound, wrapped by a Show or Change, answers 404: the path names a
// repository or a run that is not there.
var ErrNotFound = errors.New("not found")

// Refused is why a submitted change was not made, in words for the operator:
// a value the form cannot take, or the configuration loader's own refusal.
type Refused string

func (refusal Refused) Error() string { return string(refusal) }

// Routes mounts the page at the root of the receiver's listener when the
// deployment names an operator credential, and nothing otherwise. The
// listener's own routes are more specific patterns and keep their paths.
func Routes(mux *http.ServeMux, cfg shell.ServiceConfig, state *shell.StateReader) {
	if cfg.Listener.PageCredentialFile == "" {
		return
	}
	mux.Handle("/", newSite(cfg.Listener.PageCredentialFile, readingsFrom(state), SettingsAt(cfg.Root), pages))
}

type site struct {
	credentialFile string
	read           Readings
	write          Settings
	views          map[string]*template.Template
	navigation     []navigationEntry
	keys           sessionKeys
	attempts       attemptThrottle
	// now is the clock the sign-in throttle reads.
	now     func() time.Time
	handler http.Handler
}

func (site *site) ServeHTTP(w http.ResponseWriter, r *http.Request) { site.handler.ServeHTTP(w, r) }

// newSite serves pages behind the credential held in credentialFile.
func newSite(credentialFile string, read Readings, write Settings, pages []Page) *site {
	site := &site{credentialFile: credentialFile, read: read, write: write, views: map[string]*template.Template{}, now: time.Now}
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(files))
	mux.HandleFunc("GET /sign-in", site.showSignIn)
	mux.HandleFunc("POST /sign-in", site.signIn)
	mux.HandleFunc("POST /sign-out", site.signOut)
	for _, page := range append([]Page{signInPage, failurePage}, pages...) {
		site.views[page.Template] = parseView(page.Template)
		if page.Show == nil {
			continue
		}
		if page.Nav {
			site.navigation = append(site.navigation, navigationEntry{Path: strings.TrimSuffix(page.Path, "{$}"), Title: page.Title})
		}
		mux.Handle("GET "+page.Path, site.signedIn(site.show(page)))
		if page.Change != nil {
			mux.Handle("POST "+page.Path, site.signedIn(site.change(page)))
		}
	}
	// A form posted from another site is refused before it reaches a page,
	// so a form needs no token and a page cannot forget one.
	site.handler = guarded(http.NewCrossOriginProtection().Handler(mux))
	return site
}

// guarded sets what every response of the page carries: a policy under which
// the browser loads the page's own stylesheet and images and nothing else,
// runs no script and frames the page nowhere, and a bound on a posted form.
func guarded(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "same-origin")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		next.ServeHTTP(w, r)
	})
}

func (site *site) show(page Page) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := page.Show(r, site.read)
		if err != nil {
			site.fail(w, r, err)
			return
		}
		site.render(w, http.StatusOK, page, data, "")
	})
}

func (site *site) change(page Page) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next, err := page.Change(r, site.read, site.write)
		var refusal Refused
		switch {
		case errors.As(err, &refusal):
			data, err := page.Show(r, site.read)
			if err != nil {
				site.fail(w, r, err)
				return
			}
			site.render(w, http.StatusUnprocessableEntity, page, data, string(refusal))
		case err != nil:
			site.fail(w, r, err)
		default:
			http.Redirect(w, r, next, http.StatusSeeOther)
		}
	})
}

// fail shows a signed-in operator why a page could not be shown, in the
// error's own words: a configuration file that will not load says which key.
func (site *site) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, ErrNotFound) {
		status = http.StatusNotFound
	} else {
		log.Printf("web page %s: %v", r.URL.Path, err)
	}
	site.render(w, status, failurePage, err.Error(), "")
}
