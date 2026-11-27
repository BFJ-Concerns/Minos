package webpage

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"
)

// Every page is the one layout around its own template, styled by the one
// stylesheet, both embedded in the binary: the page needs no file on the box
// and loads nothing from anywhere else. Templates are html/template, which
// escapes what a page shows for where it is shown.
//
//go:embed templates static
var files embed.FS

var failurePage = Page{Title: "This page could not be shown", Template: "failure.html"}

// frame is what the layout is handed. The content template sees Data alone by default.
type frame struct {
	Title      string
	Navigation []navigationEntry
	Refresh    int
	// Refusal is why the form just submitted was not acted on.
	Refusal string
	Data    any
}

type navigationEntry struct {
	Path  string
	Title string
}

// parseView panics on a template that does not parse, which is at start-up
// and in any test that builds the site, never in front of the operator.
func parseView(name string) *template.Template {
	return template.Must(template.New("layout.html").Funcs(template.FuncMap{
		"ago":      ago,
		"duration": duration,
	}).ParseFS(files, "templates/layout.html", "templates/"+name))
}

// render writes a whole page or, when its template fails, a plain error:
// the template runs into a buffer first, so no half page is ever sent.
func (site *site) render(w http.ResponseWriter, status int, page Page, data any, refusal string) {
	shown := frame{Title: page.Title, Refresh: int(page.Refresh / time.Second), Refusal: refusal, Data: data}
	// Sign-in is the one page shown to a visitor who is not signed in, and
	// it names no other page.
	if page.Template != signInPage.Template {
		shown.Navigation = site.navigation
	}
	var body bytes.Buffer
	if err := site.views[page.Template].Execute(&body, shown); err != nil {
		log.Printf("web page: render %s: %v", page.Template, err)
		http.Error(w, "this page could not be rendered; the service log says why", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

// ago shows a moment as how long ago it was, with the moment itself a hover
// away. It takes a time.Time or the RFC 3339 text the service's records
// carry; a moment that is missing or unreadable shows as a dash.
func ago(moment any) template.HTML {
	var when time.Time
	switch moment := moment.(type) {
	case time.Time:
		when = moment
	case string:
		when, _ = time.Parse(time.RFC3339, moment)
	}
	if when.IsZero() {
		return "—"
	}
	elapsed := time.Since(when)
	phrase := "just now"
	switch {
	case elapsed < 0:
		phrase = "in " + roughly(-elapsed)
	case elapsed >= time.Minute:
		phrase = roughly(elapsed) + " ago"
	}
	return template.HTML(fmt.Sprintf(`<time datetime="%s" title="%s">%s</time>`,
		when.UTC().Format(time.RFC3339), when.UTC().Format("2 January 2006, 15:04 UTC"), phrase))
}

func roughly(elapsed time.Duration) string {
	for _, unit := range []struct {
		span time.Duration
		name string
	}{{24 * time.Hour, "day"}, {time.Hour, "hour"}, {time.Minute, "minute"}} {
		if count := int(elapsed / unit.span); count > 1 {
			return fmt.Sprintf("%d %ss", count, unit.name)
		} else if count == 1 {
			return "1 " + unit.name
		}
	}
	return "a moment"
}

// duration shows how long something took: "42s", "3m 20s", "1h 04m".
func duration(elapsed time.Duration) string {
	elapsed = elapsed.Round(time.Second)
	hours, minutes, seconds := int(elapsed/time.Hour), int(elapsed/time.Minute)%60, int(elapsed/time.Second)%60
	switch {
	case hours > 0:
		return fmt.Sprintf("%dh %02dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm %02ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}
