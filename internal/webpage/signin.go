package webpage

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/shell"
)

// One operator credential signs a browser in. It is a secret in a file the
// deployer names, as the webhook secret and the status token are, read at
// each use so that replacing the file's content changes the credential with
// no restart.
//
// A signed-in browser holds a cookie: an expiry, signed with a key derived
// from the credential. The service keeps no session state, so a session
// outlasts a restart of the receiver, and a session signed with a credential
// that has since been replaced no longer verifies: changing the credential
// signs every browser out. Each request made in the second half of a
// session's life renews it, so an operator who keeps coming back is not
// asked again.
const (
	sessionCookie   = "minos-session"
	sessionLifetime = 30 * 24 * time.Hour
	sessionFormat   = "v1"
	// keyIterations makes guessing the credential from a captured cookie
	// slow. The key is derived once for each credential the process meets.
	keyIterations = 600_000
	// attemptSpacing is the least time between two sign-in attempts the
	// service will check, whoever makes them: a guess a second at most.
	attemptSpacing = time.Second
)

var signInPage = Page{Title: "Sign in", Template: "sign-in.html"}

type signInForm struct {
	Next string
}

func (site *site) credential() (string, error) {
	credential, err := shell.ReadSecret(site.credentialFile)
	if err != nil {
		return "", err
	}
	if credential == "" {
		return "", errors.New("the operator credential file is empty")
	}
	return credential, nil
}

// sessionKey is the key that signs sessions under the credential now on
// disk. An unreadable or empty credential file signs nobody in.
func (site *site) sessionKey() ([]byte, error) {
	credential, err := site.credential()
	if err != nil {
		return nil, err
	}
	return site.keys.derive(credential)
}

// signedIn lets a request through to a page only with a session that
// verifies, and sends any other to sign in and come back.
func (site *site) signedIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, err := site.sessionKey()
		if err != nil {
			site.credentialUnavailable(w, err)
			return
		}
		expires, valid := site.session(r, key)
		if !valid {
			http.Redirect(w, r, "/sign-in?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		if time.Until(expires) < sessionLifetime/2 {
			startSession(w, r, key)
		}
		next.ServeHTTP(w, r)
	})
}

func (site *site) session(r *http.Request, key []byte) (time.Time, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return time.Time{}, false
	}
	return sessionExpiry(key, cookie.Value, time.Now())
}

func (site *site) showSignIn(w http.ResponseWriter, r *http.Request) {
	next := localPath(r.URL.Query().Get("next"))
	if key, err := site.sessionKey(); err == nil {
		if _, valid := site.session(r, key); valid {
			http.Redirect(w, r, next, http.StatusSeeOther)
			return
		}
	}
	site.render(w, http.StatusOK, signInPage, signInForm{Next: next}, "")
}

func (site *site) signIn(w http.ResponseWriter, r *http.Request) {
	form := signInForm{Next: localPath(r.PostFormValue("next"))}
	if !site.attempts.admit(time.Now()) {
		site.render(w, http.StatusTooManyRequests, signInPage, form, "Too many attempts at once. Wait a moment and try again.")
		return
	}
	credential, err := site.credential()
	if err != nil {
		site.credentialUnavailable(w, err)
		return
	}
	if !sameSecret(r.PostFormValue("credential"), credential) {
		site.render(w, http.StatusUnauthorized, signInPage, form, "That is not the operator credential.")
		return
	}
	key, err := site.keys.derive(credential)
	if err != nil {
		site.credentialUnavailable(w, err)
		return
	}
	startSession(w, r, key)
	http.Redirect(w, r, form.Next, http.StatusSeeOther)
}

// signOut ends the session in this browser. A copy of the cookie held
// elsewhere stays good until it expires or the credential changes.
func (site *site) signOut(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/sign-in", http.StatusSeeOther)
}

// credentialUnavailable answers a visitor who is not signed in, so it says
// that sign-in is not possible and leaves the reason, which names a path on
// the box, to the service log.
func (site *site) credentialUnavailable(w http.ResponseWriter, err error) {
	log.Printf("web page: operator credential %s: %v", site.credentialFile, err)
	site.render(w, http.StatusServiceUnavailable, signInPage, signInForm{Next: "/"},
		"Nobody can sign in: the service could not read its operator credential. Its log says why.")
}

func startSession(w http.ResponseWriter, r *http.Request, key []byte) {
	expires := time.Now().Add(sessionLifetime)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    mintSession(key, expires),
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(sessionLifetime / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// The listener itself speaks plain HTTP; a deployment that puts TLS
		// in front of it gets a cookie the browser sends over TLS alone.
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
}

func mintSession(key []byte, expires time.Time) string {
	payload := sessionFormat + "." + strconv.FormatInt(expires.Unix(), 10)
	return payload + "." + base64.RawURLEncoding.EncodeToString(sessionSignature(key, payload))
}

func sessionExpiry(key []byte, token string, now time.Time) (time.Time, bool) {
	cut := strings.LastIndexByte(token, '.')
	if cut < 0 {
		return time.Time{}, false
	}
	payload := token[:cut]
	presented, err := base64.RawURLEncoding.DecodeString(token[cut+1:])
	if err != nil || !hmac.Equal(presented, sessionSignature(key, payload)) {
		return time.Time{}, false
	}
	format, seconds, _ := strings.Cut(payload, ".")
	unix, err := strconv.ParseInt(seconds, 10, 64)
	if format != sessionFormat || err != nil {
		return time.Time{}, false
	}
	expires := time.Unix(unix, 0)
	return expires, now.Before(expires)
}

func sessionSignature(key []byte, payload string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// sessionKeys remembers the key derived from the credential last seen, so
// the derivation's cost is paid once and not on every request.
type sessionKeys struct {
	mu         sync.Mutex
	credential [sha256.Size]byte
	key        []byte
}

func (keys *sessionKeys) derive(credential string) ([]byte, error) {
	digest := sha256.Sum256([]byte(credential))
	keys.mu.Lock()
	defer keys.mu.Unlock()
	if keys.key == nil || keys.credential != digest {
		key, err := pbkdf2.Key(sha256.New, credential, []byte("minos web page session"), keyIterations, sha256.Size)
		if err != nil {
			return nil, err
		}
		keys.credential, keys.key = digest, key
	}
	return keys.key, nil
}

// attemptThrottle admits one sign-in attempt in each attemptSpacing and
// turns the rest away unchecked, so the credential cannot be guessed at
// faster than that however many connections try.
type attemptThrottle struct {
	mu        sync.Mutex
	notBefore time.Time
}

func (throttle *attemptThrottle) admit(now time.Time) bool {
	throttle.mu.Lock()
	defer throttle.mu.Unlock()
	if now.Before(throttle.notBefore) {
		return false
	}
	throttle.notBefore = now.Add(attemptSpacing)
	return true
}

func sameSecret(presented, held string) bool {
	a, b := sha256.Sum256([]byte(presented)), sha256.Sum256([]byte(held))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// localPath is next when it is a path on this site, and the overview
// otherwise: sign-in sends the browser on to nowhere else.
func localPath(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, `\`) {
		return "/"
	}
	return next
}
