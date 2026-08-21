package shell

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Once a run ends its directory is swept, but the timing record it wrote
// survives: archive-run delivers it as a sidecar beside the run's archive,
// readable without extracting the tarball. This route reports the recent ones,
// so an operator surface can say what a finished run did as well as what a
// live one is doing. It is the same read-only projection as /status, one step
// further back in time.
const recentRunsKind = "minos-recent-runs-v1"

const (
	defaultRecentWindowHours = 24
	maximumRecentWindowHours = 24 * 7
	defaultRecentRecords     = 60
	maximumRecentRecords     = 500
	archiveTimestampLayout   = "20060102T150405Z"
)

// An archive is named "<timestamp>-<label>-<run name>.timings.json", and the
// run name carries the unit the run belonged to.
var archiveNamePattern = regexp.MustCompile(`^(\d{8}T\d{6}Z)-(.+)\.timings\.json$`)

type recentRunsDocument struct {
	Kind        string      `json:"kind"`
	GeneratedAt string      `json:"generated_at"`
	WindowHours int         `json:"window_hours"`
	Limit       int         `json:"limit"`
	Runs        []recentRun `json:"runs"`
}

type recentRun struct {
	Archive    string          `json:"archive"`
	ArchivedAt string          `json:"archived_at"`
	Unit       string          `json:"unit,omitempty"`
	Owner      string          `json:"owner,omitempty"`
	Repo       string          `json:"repo,omitempty"`
	PR         string          `json:"pr,omitempty"`
	Timings    json.RawMessage `json:"timings"`
}

// timingIdentity is the pull request a timing record names. collect-timings
// writes it from the run's own environment, which is a surer answer than the
// archive's filename.
type timingIdentity struct {
	PullRequest struct {
		Owner      string `json:"owner"`
		Repository string `json:"repository"`
		Number     string `json:"number"`
	} `json:"pull_request"`
}

func handleRecentRuns(ctx context.Context, cfg ServiceConfig, w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil
	}
	token, err := ReadSecret(cfg.Listener.StatusTokenFile)
	if err != nil {
		http.Error(w, "status token unavailable", http.StatusInternalServerError)
		return err
	}
	if !authorisedStatusRequest(r, token) {
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return nil
	}
	hours := boundedQuery(r.URL.Query().Get("hours"), defaultRecentWindowHours, maximumRecentWindowHours)
	limit := boundedQuery(r.URL.Query().Get("limit"), defaultRecentRecords, maximumRecentRecords)
	document := recentRunsDocument{
		Kind:        recentRunsKind,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		WindowHours: hours,
		Limit:       limit,
		Runs:        []recentRun{},
	}
	if cfg.Runs.RecentTimingsCommand != "" {
		runs, err := archivedRuns(ctx, cfg, hours, limit)
		if err != nil {
			http.Error(w, "archived runs unavailable", http.StatusBadGateway)
			return err
		}
		document.Runs = runs
	}
	body, err := json.Marshal(document)
	if err != nil {
		http.Error(w, "archived runs unavailable", http.StatusInternalServerError)
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(append(body, '\n'))
	return nil
}

// boundedQuery reads a positive integer from the query string, falling back to
// a default and holding it under a ceiling. A caller says how much history it
// wants; the ceiling is what keeps one request from asking for all of it.
func boundedQuery(raw string, fallback, ceiling int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return min(value, ceiling)
}

// archivedRuns asks the archive host for every timing sidecar delivered since
// the cutoff. A sidecar that cannot be decoded is skipped rather than failing
// the request: it is one run's presentation detail, and the rest are still
// worth reporting.
func archivedRuns(ctx context.Context, cfg ServiceConfig, hours, limit int) ([]recentRun, error) {
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(archiveTimestampLayout)
	out, err := commandCombinedOutput(ctx, cfg.Runs.RecentTimingsCommand,
		cutoff, strconv.Itoa(limit))
	if err != nil {
		return nil, fmt.Errorf("list archived timing records: %w: %s", err, strings.TrimSpace(string(out)))
	}
	runs := []recentRun{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, encoded, found := strings.Cut(line, "\t")
		if !found {
			continue
		}
		match := archiveNamePattern.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		record, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil || !json.Valid(record) {
			continue
		}
		run := recentRun{
			Archive: name,
			Unit:    unitFromArchiveName(match[2]),
			Timings: record,
		}
		if archivedAt, err := time.Parse(archiveTimestampLayout, match[1]); err == nil {
			run.ArchivedAt = archivedAt.UTC().Format(time.RFC3339)
		}
		var identity timingIdentity
		if err := json.Unmarshal(record, &identity); err == nil {
			run.Owner = identity.PullRequest.Owner
			run.Repo = identity.PullRequest.Repository
			run.PR = identity.PullRequest.Number
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// unitFromArchiveName recovers the run unit from the archive's trailing run
// name. The name is the run directory's, which is the unit plus the random
// suffix that made the directory unique.
func unitFromArchiveName(remainder string) string {
	index := strings.Index(remainder, "minos-run-")
	if index < 0 {
		return ""
	}
	name := remainder[index:]
	if cut := strings.LastIndex(name, "-"); cut > len("minos-run-") {
		return name[:cut]
	}
	return name
}
