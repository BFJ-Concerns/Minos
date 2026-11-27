package webpage

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/shell"
)

var findingsPage = Page{
	Path:     "/repositories/{forge}/{owner}/{repo}/findings",
	Title:    "Finding responses",
	Template: "findings.html",
	Show:     showFindings,
}

type findingsView struct {
	Repository shell.RepoConfig
	Page       int
	Next       int
	Previous   int
	Pulls      []pullFindings
}

type pullFindings struct {
	shell.FindingHistoryPullRequest
	Resolved    int
	Rejected    int
	Open        int
	Unavailable bool
}

func showFindings(r *http.Request, read Readings) (any, error) {
	_, repos, err := read.Configuration()
	if err != nil {
		return nil, err
	}
	for _, repo := range repos {
		if repo.Forge == r.PathValue("forge") && repo.Owner == r.PathValue("owner") && repo.Repo == r.PathValue("repo") {
			return readFindings(r, read, repo)
		}
	}
	return nil, fmt.Errorf("repository is not enrolled: %w", ErrNotFound)
}

func readFindings(r *http.Request, read Readings, repo shell.RepoConfig) (findingsView, error) {
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		requested, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || requested < 1 {
			return findingsView{}, fmt.Errorf("invalid history page: %w", ErrNotFound)
		}
		page = int(requested)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	pulls, more, err := read.FindingHistoryPullRequests(ctx, repo, page)
	if err != nil {
		return findingsView{}, fmt.Errorf("read pull requests for %s/%s: %w", repo.Owner, repo.Repo, err)
	}
	view := findingsView{Repository: repo, Page: page}
	if page > 1 {
		view.Previous = page - 1
	}
	if more {
		view.Next = page + 1
	}
	for _, pull := range pulls {
		threads, err := read.ReviewThreads(ctx, repo, pull.Number)
		row := countFindingResponses(pull, threads)
		row.Unavailable = err != nil
		view.Pulls = append(view.Pulls, row)
	}
	return view, nil
}

func countFindingResponses(pull shell.FindingHistoryPullRequest, threads []forge.ReviewThread) pullFindings {
	counts := pullFindings{FindingHistoryPullRequest: pull}
	for _, thread := range threads {
		switch {
		case authorRejected(pull.Author, thread.Comments):
			counts.Rejected++
		case thread.Resolved:
			counts.Resolved++
		default:
			counts.Open++
		}
	}
	return counts
}

// An explicit rejection takes precedence over resolution. Other replies and
// outdated lines alone give no reliable answer about the underlying finding.
func authorRejected(author string, comments []forge.ReviewThreadComment) bool {
	if author == "" || len(comments) < 2 {
		return false
	}
	for _, reply := range comments[1:] {
		if !strings.EqualFold(reply.User, author) {
			continue
		}
		firstLine, _, _ := strings.Cut(reply.Body, "\n")
		decision, _, _ := strings.Cut(firstLine, ":")
		switch strings.ToLower(strings.TrimRight(strings.TrimSpace(decision), ".!")) {
		case "rejected", "i reject this finding":
			return true
		}
	}
	return false
}
