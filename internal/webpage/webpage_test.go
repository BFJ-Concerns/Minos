package webpage

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/shell"
)

const testCredential = "correct horse battery staple"

// testSite serves the real pages, and any a test adds, over the readings the
// test supplies, behind a credential file the test may rewrite.
func testSite(t *testing.T, read Readings, write Settings, added ...Page) (*site, string) {
	t.Helper()
	credentialFile := filepath.Join(t.TempDir(), "page.credential")
	if err := os.WriteFile(credentialFile, []byte(testCredential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return newSite(credentialFile, read, write, append(append([]Page{}, pages...), added...)), credentialFile
}

// signIn signs in as the operator and returns the session to send with each
// request. A site checks one attempt a second, so a test signs in once.
func signIn(t *testing.T, site http.Handler) *http.Cookie {
	t.Helper()
	response := submit(site, "/sign-in", url.Values{"credential": {testCredential}, "next": {"/"}}, nil)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("sign-in answered %d, want a redirect to the page", response.Code)
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookie {
			return cookie
		}
	}
	t.Fatal("sign-in set no session")
	return nil
}

func visit(site http.Handler, path string, session *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	return serve(site, request, session)
}

func submit(site http.Handler, path string, form url.Values, session *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return serve(site, request, session)
}

func serve(site http.Handler, request *http.Request, session *http.Cookie) *httptest.ResponseRecorder {
	if session != nil {
		request.AddCookie(session)
	}
	response := httptest.NewRecorder()
	site.ServeHTTP(response, request)
	return response
}

func enrolled(repos ...shell.RepoConfig) Readings {
	return Readings{Configuration: func() (shell.ServiceConfig, []shell.RepoConfig, error) {
		var cfg shell.ServiceConfig
		cfg.Service.BotLogin = "minos-bot"
		return cfg, repos, nil
	}}
}

func TestPageSendsAVisitorWithoutASessionToSignIn(t *testing.T) {
	site, _ := testSite(t, enrolled(), Settings{})
	response := visit(site, "/", nil)
	if got := response.Header().Get("Location"); response.Code != http.StatusSeeOther || got != "/sign-in?next=%2F" {
		t.Fatalf("answered %d to %q, want a redirect to sign in and come back", response.Code, got)
	}
	if stylesheet := visit(site, "/static/page.css", nil); stylesheet.Code != http.StatusOK {
		t.Errorf("stylesheet answered %d, want it served to the sign-in page", stylesheet.Code)
	}
}

func TestSignedInOperatorSeesTheOverview(t *testing.T) {
	site, _ := testSite(t, enrolled(shell.RepoConfig{Forge: "forgejo", Owner: "bfj", Repo: "estate"}), Settings{})
	session := signIn(t, site)
	if !session.HttpOnly {
		t.Error("the session cookie is readable by script")
	}
	response := visit(site, "/", session)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "bfj/estate") || !strings.Contains(body, "minos-bot") {
		t.Fatalf("overview answered %d:\n%s", response.Code, body)
	}
	if !strings.Contains(body, `<a href="/" aria-current="page">Overview</a>`) {
		t.Errorf("the navigation does not mark the overview as the page shown:\n%s", body)
	}
}

func TestWrongCredentialIsRefusedAndTheNextAttemptHeldOff(t *testing.T) {
	site, _ := testSite(t, enrolled(), Settings{})
	// The clock stands still, so the second attempt falls inside the first
	// one's spacing however long the scheduler takes between them.
	moment := time.Now()
	site.now = func() time.Time { return moment }
	wrong := submit(site, "/sign-in", url.Values{"credential": {"guess"}}, nil)
	if wrong.Code != http.StatusUnauthorized || len(wrong.Result().Cookies()) != 0 {
		t.Fatalf("a wrong credential answered %d with cookies %v", wrong.Code, wrong.Result().Cookies())
	}
	if held := submit(site, "/sign-in", url.Values{"credential": {testCredential}}, nil); held.Code != http.StatusTooManyRequests {
		t.Fatalf("an attempt straight after another answered %d, want it held off unchecked", held.Code)
	}
	moment = moment.Add(attemptSpacing)
	if admitted := submit(site, "/sign-in", url.Values{"credential": {testCredential}}, nil); admitted.Code != http.StatusSeeOther {
		t.Fatalf("an attempt a spacing later answered %d, want it checked and signed in", admitted.Code)
	}
}

func TestExpiredSessionIsSentToSignIn(t *testing.T) {
	site, _ := testSite(t, enrolled(), Settings{})
	key, err := (&sessionKeys{}).derive(testCredential)
	if err != nil {
		t.Fatal(err)
	}
	expired := &http.Cookie{Name: sessionCookie, Value: mintSession(key, time.Now().Add(-time.Minute))}
	if response := visit(site, "/", expired); response.Code != http.StatusSeeOther || !strings.HasPrefix(response.Header().Get("Location"), "/sign-in") {
		t.Fatalf("a correctly signed session past its expiry answered %d to %q, want sign-in", response.Code, response.Header().Get("Location"))
	}
	current := &http.Cookie{Name: sessionCookie, Value: mintSession(key, time.Now().Add(time.Hour))}
	if response := visit(site, "/", current); response.Code != http.StatusOK {
		t.Fatalf("the same session before its expiry answered %d, want the page", response.Code)
	}
}

func TestSignInReturnsOnlyToAPathOnThisSite(t *testing.T) {
	for next, want := range map[string]string{
		"/queue":                    "/queue",
		"/repositories/f/o/r?x=1#y": "/repositories/f/o/r?x=1#y",
		"":                          "/",
		"queue":                     "/",
		"https://elsewhere.example": "/",
		"//elsewhere.example":       "/",
		"/\\elsewhere.example":      "/",
		"\\/elsewhere.example":      "/",
		"/\t/elsewhere.example":     "/",
		"/\n/elsewhere.example":     "/",
		"/\r/elsewhere.example":     "/",
		"\t//elsewhere.example":     "/",
		"/\x7f/elsewhere.example":   "/",
	} {
		if got := localPath(next); got != want {
			t.Errorf("localPath(%q) = %q, want %q", next, got, want)
		}
	}
}

func TestReplacingTheCredentialEndsEverySession(t *testing.T) {
	site, credentialFile := testSite(t, enrolled(), Settings{})
	session := signIn(t, site)
	if err := os.WriteFile(credentialFile, []byte("a new credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if response := visit(site, "/", session); response.Code != http.StatusSeeOther {
		t.Fatalf("a session from the old credential answered %d, want sign-in", response.Code)
	}
}

func TestFormPostedFromAnotherSiteIsRefused(t *testing.T) {
	site, _ := testSite(t, enrolled(), Settings{})
	request := httptest.NewRequest(http.MethodPost, "/sign-out", nil)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	if response := serve(site, request, nil); response.Code != http.StatusForbidden {
		t.Fatalf("a cross-site post answered %d, want it refused", response.Code)
	}
}

func TestRefusedChangeShowsThePageAgainAndAnAcceptedOneMovesOn(t *testing.T) {
	site, _ := testSite(t, enrolled(), Settings{}, Page{
		Path:     "/rename",
		Title:    "Rename",
		Template: "overview.html",
		Show: func(r *http.Request, _ Readings) (any, error) {
			return overview{}, nil
		},
		Change: func(r *http.Request, _ Readings, _ Settings) (string, error) {
			if r.PostFormValue("name") == "" {
				return "", Refused("A name is needed.")
			}
			return "/", nil
		},
	})
	session := signIn(t, site)
	refused := submit(site, "/rename", url.Values{"name": {""}}, session)
	if refused.Code != http.StatusUnprocessableEntity || !strings.Contains(refused.Body.String(), "A name is needed.") {
		t.Fatalf("a refused change answered %d:\n%s", refused.Code, refused.Body)
	}
	accepted := submit(site, "/rename", url.Values{"name": {"minos"}}, session)
	if accepted.Code != http.StatusSeeOther || accepted.Header().Get("Location") != "/" {
		t.Fatalf("an accepted change answered %d to %q", accepted.Code, accepted.Header().Get("Location"))
	}
	if unsigned := submit(site, "/rename", url.Values{"name": {"minos"}}, nil); unsigned.Header().Get("Location") != "/sign-in?next=%2Frename" {
		t.Fatalf("a change without a session went to %q", unsigned.Header().Get("Location"))
	}
}
