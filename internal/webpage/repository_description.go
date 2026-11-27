package webpage

import (
	"net/http"

	"github.com/BFJ-Concerns/Minos/internal/shell"
)

var repositoryDescriptionPage = Page{
	Path:     "/repositories/{forge}/{owner}/{repo}/description",
	Title:    "Repository description",
	Template: "repository-description.html",
	Show:     showRepositoryDescription,
}

type repositoryDescriptionView struct {
	Repository  shell.RepoConfig
	Description *shell.RepositoryDescription
	Failure     *shell.DescriptionFailure
}

func showRepositoryDescription(r *http.Request, read Readings) (any, error) {
	repo, err := enrolledRepository(r, read)
	if err != nil {
		return nil, err
	}
	description, failure, err := read.RepositoryDescription(repo)
	if err != nil {
		return nil, err
	}
	return repositoryDescriptionView{Repository: repo, Description: description, Failure: failure}, nil
}
