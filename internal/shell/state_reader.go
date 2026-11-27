package shell

import (
	"context"
	"net/http"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

// A Surface mounts routes on the receiver's listener beside the webhook and
// status routes. It is handed the configuration the receiver started with and
// a StateReader, and nothing else of the service: a surface shows state and
// never takes part in admission or a run. The dependency runs one way, so the
// service builds and behaves the same with no surface mounted.
type Surface func(mux *http.ServeMux, cfg ServiceConfig, state *StateReader)

// The records a surface reads keep the names the service wrote them under.
// A type a reader outside this package must name is exported here as an
// alias, the same name capitalised, so the record has one definition.
type (
	StatusDocument        = statusDocument
	StatusRun             = statusRun
	SweepDeferralDocument = sweepDeferralDocument
	RecentRun             = recentRun
)

// StateReader reads the state the service already keeps, for a surface that
// shows it: the configuration on disk, the runs directory, the sweep's
// record of its last completed pass, the archived timing records, the
// repository descriptions and the forge. It writes nothing and holds nothing but the recent-runs cache, so
// what a surface shows is what the files and the forge say at that moment,
// and the JSON status routes and a surface reading through it cannot
// disagree. One is made for the listener's lifetime.
type StateReader struct {
	root   string
	recent *recentRunsCache
}

func newStateReader(root string) *StateReader {
	return &StateReader{root: root, recent: newRecentRunsCache()}
}

// Configuration reads service.toml and every repository file as they stand
// on disk now, each repository's settings resolved as the next run will
// honour them. It is read afresh on every call, so a change saved a moment
// ago, by hand or from a surface, is what comes back.
func (reader *StateReader) Configuration() (ServiceConfig, []RepoConfig, error) {
	cfg, err := LoadServiceConfig(reader.root)
	if err != nil {
		return ServiceConfig{}, nil, err
	}
	repos, err := LoadRepoConfigs(cfg)
	return cfg, repos, err
}

// ServiceSettings reads every setting of service.toml, as it stands on disk
// now, with where each value comes from (ServiceSettings).
func (reader *StateReader) ServiceSettings() ([]SettingState, error) {
	return ServiceSettings(reader.root)
}

// RepositorySettings reads every setting of one enrolled repository, as its
// file and service.toml stand on disk now, with where each value comes from
// (RepositorySettings).
func (reader *StateReader) RepositorySettings(repo RepoConfig) ([]SettingState, error) {
	return RepositorySettings(reader.root, repo)
}

// Status reads what /status serves: every live run with the pull request and
// head it serves, its stages and its timing record with the workers it has
// dispatched; the sweep's record; and the receiver's heartbeat.
//
// A run's Stage and Stages are whatever runStages reads from the run
// directory. That function is the one place that knows how a run records the
// stage it is at; a surface takes the stage from here and never opens a run
// directory itself, so the record can change form without a surface changing.
func (reader *StateReader) Status(ctx context.Context) (StatusDocument, error) {
	cfg, err := LoadServiceConfig(reader.root)
	if err != nil {
		return StatusDocument{}, err
	}
	return statusSnapshot(ctx, cfg)
}

// Sweep reads the record of the last completed sweep alone, which costs one
// small file where Status collects a timing record for each live run. The
// record carries every pull request the pass left unclaimed with its reasons,
// those held back by the concurrency cap, and CompletedAt, the sweep's own
// stamp: a sweep that has stopped running shows as that stamp ageing. Nil
// means no whole record is there to read.
func (reader *StateReader) Sweep() (*SweepDeferralDocument, error) {
	cfg, err := LoadServiceConfig(reader.root)
	if err != nil {
		return nil, err
	}
	return readSweepDeferrals(cfg), nil
}

// RecentRuns reads the timing records archived within the last hours, at
// most limit of them, and when the archive host was last listed for that
// window. The listing is cached as /runs/recent's is, and shares its cache.
// A deployment with no recent-timings-command has none to report.
func (reader *StateReader) RecentRuns(ctx context.Context, hours, limit int) ([]RecentRun, time.Time, error) {
	cfg, err := LoadServiceConfig(reader.root)
	if err != nil {
		return nil, time.Time{}, err
	}
	return reader.recent.recentRuns(ctx, cfg, hours, limit)
}

// RepositoryDescription reads the description kept of one enrolled
// repository, with the commit of its default branch it was made from and
// when it was kept. Nil means none is kept: the description is off for the
// repository, or its first has not been written yet.
func (reader *StateReader) RepositoryDescription(repo RepoConfig) (*RepositoryDescription, error) {
	cfg, err := LoadServiceConfig(reader.root)
	if err != nil {
		return nil, err
	}
	return readRepositoryDescription(repositoryDescriptionPath(cfg, repo))
}

// ReviewThreads reads the inline threads Minos opened on one pull request as
// the forge holds them now: where each sits, whether it is resolved, and its
// comments. It is a read through the service's own forge adaptation and
// credential, and each call reaches the forge.
func (reader *StateReader) ReviewThreads(ctx context.Context, repo RepoConfig, pullRequest int64) ([]forge.ReviewThread, error) {
	cfg, err := LoadServiceConfig(reader.root)
	if err != nil {
		return nil, err
	}
	adapter, err := newBehaviouralForge(cfg, repo.Forge)
	if err != nil {
		return nil, err
	}
	return adapter.ReviewThreads(ctx, forge.Repository{Owner: repo.Owner, Name: repo.Repo}, pullRequest)
}
