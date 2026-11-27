package webpage

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/shell"
)

const (
	recentHours = 24
	recentLimit = 60
)

var recentPage = Page{
	Path:     "/recent",
	Title:    "Recent runs",
	Nav:      true,
	Template: "recent.html",
	Show:     showRecent,
}

type recentView struct {
	TakenAt time.Time
	Hours   int
	Limit   int
	Runs    []archivedRunView
}

type archivedRunView struct {
	shell.RecentRun
	Outcome      string
	Duration     string
	Skipped      bool
	SkipReason   string
	Failure      string
	Report       string
	RecordedSpan string
}

func showRecent(r *http.Request, read Readings) (any, error) {
	runs, takenAt, err := read.RecentRuns(r.Context(), recentHours, recentLimit)
	if err != nil {
		return nil, err
	}
	view := recentView{TakenAt: takenAt, Hours: recentHours, Limit: recentLimit}
	for _, run := range runs {
		view.Runs = append(view.Runs, describeArchivedRun(run))
	}
	return view, nil
}

type archivedSummary struct {
	Outcome         string `json:"outcome"`
	Salvaged        bool   `json:"salvaged"`
	DurationSeconds *int64 `json:"duration_seconds"`
	Skipped         bool   `json:"review_skipped"`
	SkipReason      string `json:"skip_reason"`
	Failure         string `json:"failure_reason"`
	Report          string `json:"report_excerpt"`
}

type archivedSpan struct {
	Start time.Time `json:"started_at"`
	End   time.Time `json:"ended_at"`
}

func describeArchivedRun(run shell.RecentRun) archivedRunView {
	view := archivedRunView{RecentRun: run, Outcome: "Unknown"}
	var details archivedSummary
	// A malformed span can coexist with a valid run summary. Decode the
	// summary separately so it survives presentation failures elsewhere.
	var summary struct {
		Summary json.RawMessage `json:"run_summary"`
	}
	if json.Unmarshal(run.Timings, &summary) == nil && json.Unmarshal(summary.Summary, &details) == nil {
		view.Outcome = archivedOutcome(details.Outcome)
		view.Failure = archiveFailureLabel(details.Failure)
		if !details.Salvaged {
			view.Skipped = details.Skipped
			view.SkipReason = details.SkipReason
			view.Report = details.Report
		}
		if seconds := details.DurationSeconds; seconds != nil && *seconds >= 0 && *seconds <= int64((1<<63-1)/time.Second) {
			view.Duration = duration(time.Duration(*seconds) * time.Second)
		}
	}
	if view.Duration == "" && !details.Salvaged {
		var spans struct {
			Commands []archivedSpan
			Workers  []archivedSpan
		}
		if json.Unmarshal(run.Timings, &spans) == nil {
			view.RecordedSpan = recordedSpan(spans.Commands, spans.Workers)
		}
	}
	return view
}

func archiveFailureLabel(evidence string) string {
	if entry, ok := strings.CutPrefix(evidence, "stage="); ok {
		if stage, cause, ok := strings.Cut(entry, " cause="); ok {
			return "During " + stage + " — " + cause
		}
	}
	return evidence
}

func archivedOutcome(outcome string) string {
	switch outcome {
	case "clean":
		return "Clean"
	case "non-clean":
		return "Non-clean"
	case "attention":
		return "Changes need attention"
	case "incomplete":
		return "Incomplete"
	case "failed":
		return "Failed"
	case "continuation":
		return "Continued in another run"
	default:
		return "Unknown"
	}
}

func recordedSpan(groups ...[]archivedSpan) string {
	var first, last time.Time
	for _, group := range groups {
		for _, span := range group {
			if span.Start.IsZero() || span.End.IsZero() || span.End.Before(span.Start) {
				continue
			}
			if first.IsZero() || span.Start.Before(first) {
				first = span.Start
			}
			if span.End.After(last) {
				last = span.End
			}
		}
	}
	if first.IsZero() {
		return ""
	}
	return duration(last.Sub(first))
}
