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
	MaxConcurrent int
	Repositories  []overviewRepository
}

type overviewRepository struct {
	shell.RepoConfig
	BotLogin string
}

func showOverview(_ *http.Request, read Readings) (any, error) {
	cfg, repos, err := read.Configuration()
	if err != nil {
		return nil, err
	}
	view := overview{MaxConcurrent: cfg.MaxConcurrentRuns()}
	for _, repo := range repos {
		view.Repositories = append(view.Repositories, overviewRepository{RepoConfig: repo, BotLogin: cfg.BotLogin(repo.Forge)})
	}
	return view, nil
}
