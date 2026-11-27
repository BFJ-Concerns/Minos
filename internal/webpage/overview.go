package webpage

import (
	"net/http"

	"github.com/BFJ-Concerns/Minos/internal/shell"
)

// The overview is where a signed-in operator lands: who Minos reviews as and
// which repositories are enrolled.
var overviewPage = Page{
	Path:     "/{$}",
	Title:    "Overview",
	Nav:      true,
	Template: "overview.html",
	Show:     showOverview,
}

type overview struct {
	BotLogin      string
	MaxConcurrent int
	Repositories  []shell.RepoConfig
}

func showOverview(_ *http.Request, read Readings) (any, error) {
	cfg, repos, err := read.Configuration()
	if err != nil {
		return nil, err
	}
	return overview{
		BotLogin:      cfg.Service.BotLogin,
		MaxConcurrent: cfg.MaxConcurrentRuns(),
		Repositories:  repos,
	}, nil
}
