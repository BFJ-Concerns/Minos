package shell

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A polled recent-runs route must not open a session to the archive host per
// request: within the TTL the held listing answers, and only once it has aged
// out is the host asked again.
func TestRecentRunsServesAWindowFromCacheWithinItsTTL(t *testing.T) {
	cfg, tally := countingArchiveListing(t)
	cache := newRecentRunsCache(time.Hour)

	for range 5 {
		readCachedRecentRuns(t, cfg, cache, "")
	}
	if listings := countListings(t, tally); listings != 1 {
		t.Fatalf("archive listings = %d, want 1", listings)
	}
}

// Each window is cached in its own right: asking for a different span is a
// reading the cache does not hold, not a stale answer to the wrong question.
func TestRecentRunsCachesEachWindowSeparately(t *testing.T) {
	cfg, tally := countingArchiveListing(t)
	cache := newRecentRunsCache(time.Hour)

	readCachedRecentRuns(t, cfg, cache, "hours=1")
	readCachedRecentRuns(t, cfg, cache, "hours=2")
	readCachedRecentRuns(t, cfg, cache, "hours=1")
	if listings := countListings(t, tally); listings != 2 {
		t.Fatalf("archive listings = %d, want 2", listings)
	}
}

// An expired reading is replaced rather than served, so the route still tells
// an operator about a run that finished after the last listing.
func TestRecentRunsListsAgainOnceAReadingHasExpired(t *testing.T) {
	cfg, tally := countingArchiveListing(t)
	cache := newRecentRunsCache(0)

	readCachedRecentRuns(t, cfg, cache, "")
	readCachedRecentRuns(t, cfg, cache, "")
	if listings := countListings(t, tally); listings != 2 {
		t.Fatalf("archive listings = %d, want 2", listings)
	}
}

// The document dates itself by when the archive host answered, not by when the
// request was served, so a cached reading does not claim to be current.
func TestRecentRunsDatesTheDocumentByItsListing(t *testing.T) {
	cfg, _ := countingArchiveListing(t)
	cache := newRecentRunsCache(time.Hour)

	first := readCachedRecentRuns(t, cfg, cache, "")
	time.Sleep(1100 * time.Millisecond)
	second := readCachedRecentRuns(t, cfg, cache, "")
	if first.GeneratedAt != second.GeneratedAt {
		t.Fatalf("generated_at moved without a new listing: %q then %q",
			first.GeneratedAt, second.GeneratedAt)
	}
}

// A failed listing is held for the TTL too: an unreachable archive host is
// when a polled route would otherwise retry hardest and gain least.
func TestRecentRunsHoldsAFailedListingForItsTTL(t *testing.T) {
	cfg := statusTestConfig(t)
	tally := filepath.Join(t.TempDir(), "listings")
	script := filepath.Join(t.TempDir(), "list-recent-timings")
	writeScript(t, script, "#!/bin/sh\necho x >> '"+tally+"'\nexit 1\n")
	cfg.Runs.RecentTimingsCommand = script
	cache := newRecentRunsCache(time.Hour)

	for range 3 {
		response := httptest.NewRecorder()
		if err := handleRecentRuns(context.Background(), cfg, cache, response,
			authorisedRecentRunsRequest("")); err == nil {
			t.Fatal("handleRecentRuns() error = nil, want the listing's failure")
		}
		if response.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
		}
	}
	if listings := countListings(t, tally); listings != 1 {
		t.Fatalf("archive listings = %d, want 1", listings)
	}
}

// countingArchiveListing stands in for the archive host with a listing that
// records every invocation, so a test can count the sessions the route would
// have opened.
func countingArchiveListing(t *testing.T) (ServiceConfig, string) {
	t.Helper()
	cfg := statusTestConfig(t)
	tally := filepath.Join(t.TempDir(), "listings")
	script := filepath.Join(t.TempDir(), "list-recent-timings")
	writeScript(t, script, "#!/bin/sh\necho x >> '"+tally+"'\n")
	cfg.Runs.RecentTimingsCommand = script
	return cfg, tally
}

func countListings(t *testing.T, tally string) int {
	t.Helper()
	data, err := os.ReadFile(tally)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, b := range data {
		if b == '\n' {
			count++
		}
	}
	return count
}

func authorisedRecentRunsRequest(query string) *http.Request {
	target := "/runs/recent"
	if query != "" {
		target += "?" + query
	}
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Authorization", "Bearer status-token")
	return request
}

func readCachedRecentRuns(t *testing.T, cfg ServiceConfig, cache *recentRunsCache, query string) recentRunsDocument {
	t.Helper()
	response := httptest.NewRecorder()
	if err := handleRecentRuns(context.Background(), cfg, cache, response,
		authorisedRecentRunsRequest(query)); err != nil {
		t.Fatalf("handleRecentRuns() error = %v", err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %q", response.Code, http.StatusOK, response.Body.String())
	}
	var document recentRunsDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode recent runs: %v", err)
	}
	return document
}
