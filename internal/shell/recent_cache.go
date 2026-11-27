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
	command string
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

func newRecentRunsCache() *recentRunsCache {
	return &recentRunsCache{entries: map[recentRunsWindow]*recentRunsEntry{}}
}

func (cache *recentRunsCache) recentRuns(ctx context.Context, cfg ServiceConfig, hours, limit int) ([]RecentRun, time.Time, error) {
	if cfg.Runs.RecentTimingsCommand == "" {
		return nil, time.Time{}, nil
	}
	if hours < 1 {
		hours = defaultRecentWindowHours
	}
	if limit < 1 {
		limit = defaultRecentRecords
	}
	hours, limit = min(hours, maximumRecentWindowHours), min(limit, maximumRecentRecords)
	reading, err := cache.read(ctx, cfg, hours, limit)
	return reading.runs, reading.takenAt, err
}

// read shares a listing only while its command still matches and its age
// is within the current TTL. A cancelled caller leaves the cache alone.
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
	if err := ctx.Err(); err != nil {
		return recentRunsReading{}, err
	}
	command := cfg.Runs.RecentTimingsCommand
	if entry.reading.command == command && time.Since(entry.reading.takenAt) < cfg.RecentTimingsCacheTTL() {
		return entry.reading, entry.reading.err
	}
	runs, err := archivedRuns(ctx, cfg, hours, limit)
	if ctx.Err() != nil && err != nil {
		return recentRunsReading{}, ctx.Err()
	}
	entry.reading = recentRunsReading{takenAt: time.Now(), command: command, runs: runs, err: err}
	return entry.reading, err
}
