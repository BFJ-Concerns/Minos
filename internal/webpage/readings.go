package webpage

import (
	"context"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/shell"
)

// Readings is everything a page can learn: one function for each kind of
// state the service keeps. The page holds no state of its own beyond a
// request, and each reading is taken when a page asks for it, so a page
// shows what the configuration, the runs directory, the archive and the
// forge say now.
//
// A page calls only these. A reading the page lacks is added here and to
// shell.StateReader, where it reads through the function the service itself
// reads through; a page test sets the fields its page calls and leaves the
// rest nil.
type Readings struct {
	// Configuration is service.toml and each repository's file as they
	// stand on disk, each repository's settings resolved as the next run
	// will honour them.
	Configuration func() (shell.ServiceConfig, []shell.RepoConfig, error)
	// Status is every live run with its pull request, head, stage, stages
	// and timing record, which lists the workers it has dispatched; with the
	// sweep's record and the receiver's heartbeat. It runs the timings
	// command once for each live run.
	Status func(ctx context.Context) (shell.StatusDocument, error)
	// Sweep is the last completed sweep's record alone, nil when there is
	// none: each pull request it left unclaimed and why, those waiting for
	// a slot, and the sweep's own stamp of when it completed.
	Sweep func() (*shell.SweepDeferralDocument, error)
	// RecentRuns is the timing records archived in the last hours, at most
	// limit, and when the archive host was last listed for them.
	RecentRuns func(ctx context.Context, hours, limit int) ([]shell.RecentRun, time.Time, error)
	// FindingHistoryPullRequests lists a page of pull requests in every state.
	FindingHistoryPullRequests func(ctx context.Context, repo shell.RepoConfig, page int) ([]shell.FindingHistoryPullRequest, bool, error)
	// ServiceSettings is every setting service.toml holds, the service-wide
	// value of each per-repository setting among them, and where each
	// value comes from.
	ServiceSettings func() ([]shell.SettingState, error)
	// RepositorySettings is every setting of one enrolled repository and
	// whether its own file, service.toml or the documented default sets it.
	RepositorySettings func(repo shell.RepoConfig) ([]shell.SettingState, error)
	// ReviewThreads is the threads Minos opened on one pull request, as the
	// forge holds them now. Each call reaches the forge.
	ReviewThreads func(ctx context.Context, repo shell.RepoConfig, pullRequest int64) ([]forge.ReviewThread, error)
}

func readingsFrom(state *shell.StateReader) Readings {
	return Readings{
		Configuration:              state.Configuration,
		Status:                     state.Status,
		Sweep:                      state.Sweep,
		RecentRuns:                 state.RecentRuns,
		FindingHistoryPullRequests: state.FindingHistoryPullRequests,
		ReviewThreads:              state.ReviewThreads,
		ServiceSettings:            state.ServiceSettings,
		RepositorySettings:         state.RepositorySettings,
	}
}

// Settings is the page's one way to change anything: a setting written to
// the configuration file the deployer would have edited, taking effect for
// the next run. Key is the setting's key path in that file and value the
// whole setting; nil removes the key, so the next level out applies again.
// A change the configuration loader would not accept is refused and nothing
// written, and a Change hands that refusal to the operator as a Refused.
type Settings struct {
	// Service writes a key of service.toml. The service-wide value of a
	// per-repository setting is at that setting's shell.ServiceWideKey.
	Service func(key []string, value any) error
	// Repository writes a key of one enrolled repository's own file.
	Repository func(repo shell.RepoConfig, key []string, value any) error
}

// SettingsAt writes to the configuration rooted at root.
func SettingsAt(root string) Settings {
	return Settings{
		Service: func(key []string, value any) error {
			return shell.WriteServiceSetting(root, key, value)
		},
		Repository: func(repo shell.RepoConfig, key []string, value any) error {
			return shell.WriteRepositorySetting(root, repo, key, value)
		},
	}
}
