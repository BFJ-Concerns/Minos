package webpage

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/shell"
)

var livePage = Page{
	Path:     "/live",
	Title:    "Live runs",
	Nav:      true,
	Template: "live.html",
	Show:     showLive,
	Refresh:  15 * time.Second,
}

type liveRun struct {
	shell.StatusRun
	Workers      []liveWorker
	TimingsError bool
}

type liveWorker struct {
	Label     string
	Phase     string
	Engine    string
	Model     string
	Status    string
	StartedAt string `json:"started_at"`
}

type liveView struct {
	Status shell.StatusDocument
	Runs   []liveRun
}

func showLive(r *http.Request, read Readings) (any, error) {
	status, err := read.Status(r.Context())
	if err != nil {
		return nil, err
	}
	view := liveView{Status: status}
	for _, run := range status.Runs {
		var timing struct{ Workers []liveWorker }
		err := json.Unmarshal(run.Timings, &timing)
		view.Runs = append(view.Runs, liveRun{
			StatusRun: run, Workers: timing.Workers, TimingsError: len(run.Timings) > 0 && err != nil,
		})
	}
	return view, nil
}
