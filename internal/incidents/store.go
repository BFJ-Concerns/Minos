// Package incidents records deduplicated deployment failures separately from
// pull-request conversation. Its identity is deliberately explicit so the
// same key can move into the coordination ledger at integration time.
package incidents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"bfj/minos/internal/atomicreplace"
)

type State string

const (
	StateOpen      State = "open"
	StateRecovered State = "recovered"
)

type Key struct {
	Forge       string `json:"forge"`
	Repository  string `json:"repository"`
	PullRequest int    `json:"pull_request"`
	Attempt     string `json:"attempt"`
	Family      string `json:"family"`
}

func (key Key) String() string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s", key.Forge, key.Repository, key.PullRequest, key.Attempt, key.Family)
}

func (key Key) validate() error {
	if strings.TrimSpace(key.Forge) == "" || strings.TrimSpace(key.Repository) == "" || key.PullRequest <= 0 || strings.TrimSpace(key.Attempt) == "" || strings.TrimSpace(key.Family) == "" {
		return errors.New("incident identity requires forge, repository, positive pull request, attempt, and family")
	}
	return nil
}

type Event struct {
	Key        Key
	Diagnostic string
	LogPath    string
	At         time.Time
}

type Incident struct {
	Key            Key        `json:"key"`
	State          State      `json:"state"`
	Diagnostic     string     `json:"diagnostic"`
	LogPath        string     `json:"log_path"`
	FirstSeenAt    time.Time  `json:"first_seen_at"`
	LastSeenAt     time.Time  `json:"last_seen_at"`
	Updates        int        `json:"updates"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	AcknowledgedBy string     `json:"acknowledged_by,omitempty"`
	RecoveredAt    *time.Time `json:"recovered_at,omitempty"`
}

type Store interface {
	Raise(context.Context, Event) (Incident, error)
	Acknowledge(context.Context, Key, string, time.Time) (Incident, error)
	Recover(context.Context, Key, time.Time) (Incident, error)
	List(context.Context) ([]Incident, error)
}

type FileStore struct{ root string }

func NewFileStore(root string) *FileStore { return &FileStore{root: root} }

func (store *FileStore) Raise(_ context.Context, event Event) (Incident, error) {
	if err := event.Key.validate(); err != nil {
		return Incident{}, err
	}
	if strings.TrimSpace(event.Diagnostic) == "" || strings.TrimSpace(event.LogPath) == "" {
		return Incident{}, errors.New("incident requires a diagnostic and diagnostic-log path")
	}
	if strings.ContainsAny(event.LogPath, "\r\n") {
		return Incident{}, errors.New("incident diagnostic-log path must be one line")
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	return store.update(event.Key, func(current *Incident) error {
		if current.Updates == 0 {
			current.Key = event.Key
			current.State = StateOpen
			current.FirstSeenAt = event.At.UTC()
		}
		current.Diagnostic = event.Diagnostic
		current.LogPath = event.LogPath
		current.LastSeenAt = event.At.UTC()
		current.Updates++
		// A recurrence after recovery is the same deduplicated incident, open
		// again. Retain acknowledgement history but clear the stale recovery.
		current.State = StateOpen
		current.RecoveredAt = nil
		return nil
	})
}

func (store *FileStore) Acknowledge(_ context.Context, key Key, actor string, at time.Time) (Incident, error) {
	if strings.TrimSpace(actor) == "" {
		return Incident{}, errors.New("acknowledgement actor is required")
	}
	return store.updateExisting(key, func(current *Incident) {
		value := at.UTC()
		current.AcknowledgedAt = &value
		current.AcknowledgedBy = actor
	})
}

func (store *FileStore) Recover(_ context.Context, key Key, at time.Time) (Incident, error) {
	return store.updateExisting(key, func(current *Incident) {
		value := at.UTC()
		current.State = StateRecovered
		current.RecoveredAt = &value
	})
}

func (store *FileStore) updateExisting(key Key, mutate func(*Incident)) (Incident, error) {
	return store.update(key, func(current *Incident) error {
		if current.Updates == 0 {
			return errors.New("incident not found")
		}
		mutate(current)
		return nil
	})
}

func (store *FileStore) update(key Key, mutate func(*Incident) error) (Incident, error) {
	if err := key.validate(); err != nil {
		return Incident{}, err
	}
	if err := os.MkdirAll(store.root, 0o750); err != nil {
		return Incident{}, err
	}
	lock, err := os.OpenFile(filepath.Join(store.root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return Incident{}, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return Incident{}, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck // Releasing a closing advisory lock cannot repair a completed update.
	path := store.path(key)
	current, err := read(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Incident{}, err
	}
	if err := mutate(&current); err != nil {
		return Incident{}, err
	}
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return Incident{}, err
	}
	data = append(data, '\n')
	if err := atomicreplace.Write(path, data, 0o640); err != nil {
		return Incident{}, err
	}
	return current, nil
}

func (store *FileStore) List(_ context.Context) ([]Incident, error) {
	paths, err := filepath.Glob(filepath.Join(store.root, "*.json"))
	if err != nil {
		return nil, err
	}
	incidents := make([]Incident, 0, len(paths))
	for _, path := range paths {
		incident, err := read(path)
		if err != nil {
			return nil, err
		}
		incidents = append(incidents, incident)
	}
	sort.Slice(incidents, func(i, j int) bool { return incidents[i].LastSeenAt.Before(incidents[j].LastSeenAt) })
	return incidents, nil
}

func (store *FileStore) path(key Key) string {
	digest := sha256.Sum256([]byte(key.String()))
	return filepath.Join(store.root, hex.EncodeToString(digest[:])+".json")
}

func read(path string) (Incident, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Incident{}, err
	}
	var incident Incident
	if err := json.Unmarshal(data, &incident); err != nil {
		return Incident{}, err
	}
	return incident, nil
}
