package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// FindingHistoryPullRequest is a pull request that can carry Minos threads.
// Closed and merged pull requests are included in the repository history.
type FindingHistoryPullRequest struct {
	Number int64
	Author string
	URL    string
}

// FindingHistoryPullRequests reads one page of a repository's pull requests.
// Each page bounds the number of individual thread reads the view can make.
func (reader *StateReader) FindingHistoryPullRequests(ctx context.Context, repo RepoConfig, page int) ([]FindingHistoryPullRequest, bool, error) {
	const pageSize = 10
	configuration, ok := reader.cfg.Forges[repo.Forge]
	if !ok {
		return nil, false, fmt.Errorf("unknown forge %q", repo.Forge)
	}
	adaptation, err := NewAdaptation(configuration)
	if err != nil {
		return nil, false, err
	}
	output, err := adaptation.Run(ctx, "list-pull-requests", nil, nil, repo.Owner, repo.Repo, strconv.Itoa(page), strconv.Itoa(pageSize))
	if err != nil {
		return nil, false, err
	}
	var pulls []FindingHistoryPullRequest
	if err := json.Unmarshal(output, &pulls); err != nil {
		return nil, false, fmt.Errorf("decode pull request history: %w", err)
	}
	if pulls == nil || len(pulls) > pageSize {
		return nil, false, fmt.Errorf("invalid pull request history page")
	}
	for _, pull := range pulls {
		if pull.Number <= 0 {
			return nil, false, fmt.Errorf("pull request history omitted a pull request number")
		}
	}
	return pulls, len(pulls) == pageSize, nil
}
