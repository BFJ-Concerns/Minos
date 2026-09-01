package shell

import (
	"context"
	"sync"
	"time"
)

// Listing the archive host's timing sidecars costs a whole SSH session, and
// the answer only changes when a run finishes — minutes to hours apart. An
// operator surface polling on a screen-refresh cadence would otherwise open a
// session per request: six a minute, around the clock, for a reading that was
// already correct. recentRunsCache holds each window's last reading for a TTL
// so the poll rate no longer sets the session rate.
//
// A reading's error is cached alongside its runs. An unreachable archive host
// is exactly when the retries would be most frequent and least useful, so the
// TTL bounds them too, at the cost of recovering a window late.
type recentRunsCache struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[recentRunsWindow]*recentRunsEntry
}

// A window is the request's own (hours, limit) pair. Both are already bounded
// by boundedQuery, so a caller cannot grow the map without limit.
type recentRunsWindow struct {
	hours int
	limit int
}

// recentRunsReading is one listing of the archive host: when it was taken and
// what it said. takenAt is the document's honest freshness claim, which is the
// moment the archive host answered rather than the moment a request was served.
type recentRunsReading struct {
	takenAt time.Time
	runs    []recentRun
	err     error
}

// recentRunsEntry guards one window's reading. Its own mutex is what makes
// concurrent requests for the same window share a single listing rather than
// each opening a session; requests for other windows do not block on it.
type recentRunsEntry struct {
	mu      sync.Mutex
	reading recentRunsReading
}

func newRecentRunsCache(ttl time.Duration) *recentRunsCache {
	return &recentRunsCache{ttl: ttl, entries: map[recentRunsWindow]*recentRunsEntry{}}
}

// read returns the window's reading, listing the archive host only when the
// held one has aged past the TTL. A zero TTL lists on every call, which is the
// behaviour a test wants when the cache is not what it is exercising.
func (cache *recentRunsCache) read(ctx context.Context, cfg ServiceConfig, hours, limit int) (recentRunsReading, error) {
	window := recentRunsWindow{hours: hours, limit: limit}
	cache.mu.Lock()
	entry, held := cache.entries[window]
	if !held {
		entry = &recentRunsEntry{}
		cache.entries[window] = entry
	}
	cache.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if !entry.reading.takenAt.IsZero() && time.Since(entry.reading.takenAt) < cache.ttl {
		return entry.reading, entry.reading.err
	}
	runs, err := archivedRuns(ctx, cfg, hours, limit)
	entry.reading = recentRunsReading{takenAt: time.Now(), runs: runs, err: err}
	return entry.reading, err
}
