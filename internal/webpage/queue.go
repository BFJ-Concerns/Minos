package webpage

import (
	"net/http"
	"slices"
	"sort"
	"time"
)

var queuePage = Page{
	Path:     "/queue",
	Title:    "Queue",
	Nav:      true,
	Template: "queue.html",
	Show:     showQueue,
	Refresh:  30 * time.Second,
}

type queueEntry struct {
	Name    string
	Forge   string
	Reason  string
	Reasons []string
}

type queueView struct {
	CompletedAt string
	Partial     bool
	Waiting     []queueEntry
	Deferred    []queueEntry
	Skipped     []queueEntry
}

func showQueue(_ *http.Request, read Readings) (any, error) {
	sweep, err := read.Sweep()
	if err != nil {
		return nil, err
	}
	if sweep == nil {
		return nil, nil
	}
	view := queueView{CompletedAt: sweep.CompletedAt, Partial: sweep.Partial}
	for _, row := range sweep.Deferrals {
		entry := queueEntry{Name: row.Owner + "/" + row.Repo + " #" + row.PR, Forge: row.Forge, Reason: deferralLabel(row.Reason)}
		for _, reason := range row.Reasons {
			if label := deferralLabel(reason); label != entry.Reason && !slices.Contains(entry.Reasons, label) {
				entry.Reasons = append(entry.Reasons, label)
			}
		}
		view.Deferred = append(view.Deferred, entry)
	}
	for _, row := range sweep.Suppressed {
		entry := queueEntry{Name: row.Owner + "/" + row.Repo + " #" + row.PR, Forge: row.Forge}
		if row.Detail != "" {
			entry.Reason = row.Detail
			view.Waiting = append(view.Waiting, entry)
		} else {
			entry.Reason = "Already running (" + row.BlockingUnit + ")"
			view.Deferred = append(view.Deferred, entry)
		}
	}
	for name, reason := range sweep.SkippedRepositories {
		view.Skipped = append(view.Skipped, queueEntry{Name: name, Reason: reason})
	}
	sort.Slice(view.Skipped, func(i, j int) bool { return view.Skipped[i].Name < view.Skipped[j].Name })
	return view, nil
}

func deferralLabel(reason string) string {
	switch reason {
	case "completed-marker":
		return "Current head already reviewed"
	case "draft":
		return "Draft pull request"
	default:
		return reason
	}
}
