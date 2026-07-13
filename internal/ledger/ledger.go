// Package ledger owns Minos's deployment-local coordination state.
//
// The ledger is intentionally not a product database. Durable review state is
// read from the forge; this package contains only ownership, liveness, pacing,
// waiting, incident identity, and guarded cleanup obligations.
package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
	lib "modernc.org/sqlite/lib"
)

var (
	ErrLedger   = errors.New("coordination ledger unavailable")
	ErrCapacity = errors.New("run capacity unavailable")
	ErrNotOwner = errors.New("attempt does not own lease")
)

const schemaVersion = 1

type Key struct {
	Forge string
	Owner string
	Repo  string
	PR    string
}

type Lease struct {
	Key
	Token           int64
	ObservedHead    string
	ObservedTarget  string
	HeartbeatAt     time.Time
	Unit            string
	Workspace       string
	ClearanceHead   string
	ClearanceTarget string
	CreatedAt       time.Time
}

type Wait struct {
	Key
	Fingerprint string
	FailsafeAt  *time.Time
	UpdatedAt   time.Time
}

type Backoff struct {
	Key
	Attempt       int
	LastFailureAt time.Time
	NextDueAt     time.Time
}

type Incident struct {
	Key
	Category       string
	FirstAt        time.Time
	LastAt         time.Time
	RetryCount     int
	ObservedHead   string
	ObservedTarget string
	LogLocation    string
	AcknowledgedAt *time.Time
}

type Cleanup struct {
	Key
	MergedHead string
	Branch     string
	CreatedAt  time.Time
	Attempts   int
}

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func Open(path string) (*Store, error) {
	return OpenWithClock(path, time.Now)
}

func OpenWithClock(path string, now func() time.Time) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: empty path", ErrLedger)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("%w: create parent: %v", ErrLedger, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("%w: open: %v", ErrLedger, err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, now: now}
	if err := store.bootstrap(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) bootstrap(ctx context.Context) error {
	for _, pragma := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
	} {
		if err := execBootstrapPragma(ctx, s.db, pragma); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrLedger, pragma, err)
		}
	}
	var integrity string
	if err := s.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("%w: integrity check: result=%q err=%v", ErrLedger, integrity, err)
	}
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("%w: read schema version: %v", ErrLedger, err)
	}
	if version > schemaVersion {
		return fmt.Errorf("%w: schema version %d is newer than binary version %d", ErrLedger, version, schemaVersion)
	}
	if version == 0 {
		if err := s.immediate(ctx, func(conn *sql.Conn) error {
			// Another process may have migrated while this opener waited for the
			// immediate write lock. Re-read under that lock before bootstrapping.
			var current int
			if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
				return err
			}
			if current == schemaVersion {
				return nil
			}
			var existing int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('leases','token_sequence')`).Scan(&existing); err != nil {
				return err
			}
			if existing != 0 {
				return fmt.Errorf("unversioned coordination tables exist")
			}
			_, err := conn.ExecContext(ctx, schemaSQL)
			return err
		}); err != nil {
			return fmt.Errorf("%w: migrate to version 1: %v", ErrLedger, err)
		}
	}
	return nil
}

func execBootstrapPragma(ctx context.Context, db *sql.DB, pragma string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := db.ExecContext(ctx, pragma)
		if err == nil || !sqliteBusy(err) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		// busy_timeout does not reliably cover a concurrent journal-mode change.
		// Retrying that idempotent pragma lets the winning opener finish bootstrap
		// without weakening the integrity and schema refusal checks which follow.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func sqliteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == lib.SQLITE_BUSY
}

const schemaSQL = `
CREATE TABLE token_sequence (id INTEGER PRIMARY KEY CHECK (id = 0), next INTEGER NOT NULL);
INSERT INTO token_sequence(id, next) VALUES (0, 1);
CREATE TABLE leases (
  forge TEXT NOT NULL, owner TEXT NOT NULL, repo TEXT NOT NULL, pr TEXT NOT NULL,
  token INTEGER NOT NULL, observed_head TEXT NOT NULL, observed_target TEXT NOT NULL,
  heartbeat_at TEXT NOT NULL, unit TEXT NOT NULL, workspace TEXT NOT NULL,
  clearance_head TEXT, clearance_target TEXT, created_at TEXT NOT NULL,
  PRIMARY KEY (forge, owner, repo, pr)
);
-- Waiting commonly outlives the lifecycle which discovered it. Keeping it in
-- its own PR-keyed row avoids retaining a lease merely to retain pacing state.
CREATE TABLE waits (
  forge TEXT NOT NULL, owner TEXT NOT NULL, repo TEXT NOT NULL, pr TEXT NOT NULL,
  fingerprint TEXT NOT NULL, failsafe_at TEXT, updated_at TEXT NOT NULL,
  PRIMARY KEY (forge, owner, repo, pr)
);
CREATE TABLE backoff (
  forge TEXT NOT NULL, owner TEXT NOT NULL, repo TEXT NOT NULL, pr TEXT NOT NULL,
  attempt INTEGER NOT NULL, last_failure_at TEXT NOT NULL, next_due_at TEXT NOT NULL,
  PRIMARY KEY (forge, owner, repo, pr)
);
CREATE TABLE incidents (
  forge TEXT NOT NULL, owner TEXT NOT NULL, repo TEXT NOT NULL, pr TEXT NOT NULL,
  category TEXT NOT NULL, first_at TEXT NOT NULL, last_at TEXT NOT NULL,
  retry_count INTEGER NOT NULL, observed_head TEXT, observed_target TEXT,
  log_location TEXT, acknowledged_at TEXT,
  PRIMARY KEY (forge, owner, repo, pr, category)
);
CREATE TABLE cleanup_obligations (
  forge TEXT NOT NULL, owner TEXT NOT NULL, repo TEXT NOT NULL, pr TEXT NOT NULL,
  merged_head TEXT NOT NULL, branch TEXT NOT NULL, created_at TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (forge, owner, repo, pr)
);
PRAGMA user_version = 1;`

func (s *Store) immediate(ctx context.Context, fn func(*sql.Conn) error) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err = fn(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseStamp(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }

func keyArgs(key Key) []any { return []any{key.Forge, key.Owner, key.Repo, key.PR} }

func allocateToken(ctx context.Context, conn *sql.Conn) (int64, error) {
	var token int64
	if err := conn.QueryRowContext(ctx, "UPDATE token_sequence SET next = next + 1 WHERE id = 0 RETURNING next - 1").Scan(&token); err != nil {
		return 0, err
	}
	return token, nil
}

func (s *Store) AcquireLease(ctx context.Context, lease Lease, maxConcurrent int) (Lease, error) {
	var acquired Lease
	err := s.immediate(ctx, func(conn *sql.Conn) error {
		var exists int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM leases WHERE forge=? AND owner=? AND repo=? AND pr=?", keyArgs(lease.Key)...).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return ErrNotOwner
		}
		var occupied int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM leases").Scan(&occupied); err != nil {
			return err
		}
		if occupied >= maxConcurrent {
			return ErrCapacity
		}
		token, err := allocateToken(ctx, conn)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		_, err = conn.ExecContext(ctx, `INSERT INTO leases
			(forge,owner,repo,pr,token,observed_head,observed_target,heartbeat_at,unit,workspace,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, lease.Forge, lease.Owner, lease.Repo, lease.PR, token,
			lease.ObservedHead, lease.ObservedTarget, stamp(now), lease.Unit, lease.Workspace, stamp(now))
		if err != nil {
			return err
		}
		lease.Token, lease.HeartbeatAt, lease.CreatedAt = token, now, now
		acquired = lease
		return nil
	})
	return acquired, err
}

func (s *Store) ReplaceLease(ctx context.Context, oldToken int64, lease Lease) (Lease, error) {
	var replacement Lease
	err := s.immediate(ctx, func(conn *sql.Conn) error {
		token, err := allocateToken(ctx, conn)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		result, err := conn.ExecContext(ctx, `UPDATE leases SET token=?,observed_head=?,observed_target=?,heartbeat_at=?,unit=?,workspace=?,clearance_head=NULL,clearance_target=NULL,created_at=?
			WHERE forge=? AND owner=? AND repo=? AND pr=? AND token=?`, token, lease.ObservedHead, lease.ObservedTarget,
			stamp(now), lease.Unit, lease.Workspace, stamp(now), lease.Forge, lease.Owner, lease.Repo, lease.PR, oldToken)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return ErrNotOwner
		}
		lease.Token, lease.HeartbeatAt, lease.CreatedAt = token, now, now
		replacement = lease
		return nil
	})
	return replacement, err
}

func (s *Store) Owns(ctx context.Context, key Key, token int64) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM leases WHERE forge=? AND owner=? AND repo=? AND pr=? AND token=?", append(keyArgs(key), token)...).Scan(&found)
	return found == 1, err
}

func (s *Store) RenewHeartbeat(ctx context.Context, key Key, token int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, "UPDATE leases SET heartbeat_at=? WHERE forge=? AND owner=? AND repo=? AND pr=? AND token=?", append([]any{stamp(s.now())}, append(keyArgs(key), token)...)...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *Store) ReleaseLease(ctx context.Context, key Key, token int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM leases WHERE forge=? AND owner=? AND repo=? AND pr=? AND token=?", append(keyArgs(key), token)...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *Store) Lease(ctx context.Context, key Key) (Lease, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT token,observed_head,observed_target,heartbeat_at,unit,workspace,
		COALESCE(clearance_head,''),COALESCE(clearance_target,''),created_at FROM leases
		WHERE forge=? AND owner=? AND repo=? AND pr=?`, keyArgs(key)...)
	var lease Lease
	lease.Key = key
	var heartbeat, created string
	if err := row.Scan(&lease.Token, &lease.ObservedHead, &lease.ObservedTarget, &heartbeat, &lease.Unit, &lease.Workspace, &lease.ClearanceHead, &lease.ClearanceTarget, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Lease{}, false, nil
		}
		return Lease{}, false, err
	}
	var err error
	if lease.HeartbeatAt, err = parseStamp(heartbeat); err != nil {
		return Lease{}, false, err
	}
	if lease.CreatedAt, err = parseStamp(created); err != nil {
		return Lease{}, false, err
	}
	return lease, true, nil
}

func (s *Store) ListLeases(ctx context.Context) ([]Lease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT forge,owner,repo,pr,token,observed_head,observed_target,heartbeat_at,unit,workspace,
		COALESCE(clearance_head,''),COALESCE(clearance_target,''),created_at FROM leases ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var leases []Lease
	for rows.Next() {
		var lease Lease
		var heartbeat, created string
		if err := rows.Scan(&lease.Forge, &lease.Owner, &lease.Repo, &lease.PR, &lease.Token, &lease.ObservedHead, &lease.ObservedTarget, &heartbeat, &lease.Unit, &lease.Workspace, &lease.ClearanceHead, &lease.ClearanceTarget, &created); err != nil {
			return nil, err
		}
		if lease.HeartbeatAt, err = parseStamp(heartbeat); err != nil {
			return nil, err
		}
		if lease.CreatedAt, err = parseStamp(created); err != nil {
			return nil, err
		}
		leases = append(leases, lease)
	}
	return leases, rows.Err()
}

func (s *Store) SetClearance(ctx context.Context, key Key, token int64, head, target string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE leases SET clearance_head=?,clearance_target=? WHERE forge=? AND owner=? AND repo=? AND pr=? AND token=? AND observed_head=? AND observed_target=?`,
		append([]any{head, target}, append(keyArgs(key), token, head, target)...)...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *Store) ClearClearance(ctx context.Context, key Key, token int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE leases SET clearance_head=NULL,clearance_target=NULL WHERE forge=? AND owner=? AND repo=? AND pr=? AND token=?`, append(keyArgs(key), token)...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// UpdateObservedPair lets the current owner follow its own guarded repair or
// target-sync push without surrendering the lease. Foreign movement leaves the
// row unchanged, so the next current-state guard makes the owner yield.
func (s *Store) UpdateObservedPair(ctx context.Context, key Key, token int64, oldHead, oldTarget, newHead, newTarget string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE leases SET observed_head=?,observed_target=?,clearance_head=NULL,clearance_target=NULL
		WHERE forge=? AND owner=? AND repo=? AND pr=? AND token=? AND observed_head=? AND observed_target=?`,
		append([]any{newHead, newTarget}, append(keyArgs(key), token, oldHead, oldTarget)...)...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *Store) SetWait(ctx context.Context, wait Wait, token int64) (bool, error) {
	now := s.now().UTC()
	var failsafe any
	if wait.FailsafeAt != nil {
		failsafe = stamp(*wait.FailsafeAt)
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO waits(forge,owner,repo,pr,fingerprint,failsafe_at,updated_at)
		SELECT ?,?,?,?,?,?,? WHERE EXISTS (
			SELECT 1 FROM leases WHERE forge=? AND owner=? AND repo=? AND pr=? AND token=?)
		ON CONFLICT(forge,owner,repo,pr) DO UPDATE SET fingerprint=excluded.fingerprint,failsafe_at=excluded.failsafe_at,updated_at=excluded.updated_at`,
		wait.Forge, wait.Owner, wait.Repo, wait.PR, wait.Fingerprint, failsafe, stamp(now),
		wait.Forge, wait.Owner, wait.Repo, wait.PR, token)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *Store) Wait(ctx context.Context, key Key) (Wait, bool, error) {
	var wait Wait
	wait.Key = key
	var failsafe sql.NullString
	var updated string
	err := s.db.QueryRowContext(ctx, "SELECT fingerprint,failsafe_at,updated_at FROM waits WHERE forge=? AND owner=? AND repo=? AND pr=?", keyArgs(key)...).Scan(&wait.Fingerprint, &failsafe, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Wait{}, false, nil
	}
	if err != nil {
		return Wait{}, false, err
	}
	wait.UpdatedAt, err = parseStamp(updated)
	if err != nil {
		return Wait{}, false, err
	}
	if failsafe.Valid {
		value, parseErr := parseStamp(failsafe.String)
		if parseErr != nil {
			return Wait{}, false, parseErr
		}
		wait.FailsafeAt = &value
	}
	return wait, true, nil
}

func (s *Store) ClearWait(ctx context.Context, key Key, token int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM waits WHERE forge=? AND owner=? AND repo=? AND pr=?
		AND EXISTS (SELECT 1 FROM leases WHERE forge=? AND owner=? AND repo=? AND pr=? AND token=?)`,
		append(keyArgs(key), append(keyArgs(key), token)...)...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func backoffDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 15 * time.Minute
	for i := 1; i < attempt && delay < 6*time.Hour; i++ {
		delay *= 2
	}
	if delay > 6*time.Hour {
		return 6 * time.Hour
	}
	return delay
}

func (s *Store) RecordFailure(ctx context.Context, key Key, token int64) (Backoff, error) {
	var value Backoff
	err := s.immediate(ctx, func(conn *sql.Conn) error {
		var owned int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM leases WHERE forge=? AND owner=? AND repo=? AND pr=? AND token=?", append(keyArgs(key), token)...).Scan(&owned); err != nil {
			return err
		}
		if owned != 1 {
			return ErrNotOwner
		}
		var prior int
		err := conn.QueryRowContext(ctx, "SELECT attempt FROM backoff WHERE forge=? AND owner=? AND repo=? AND pr=?", keyArgs(key)...).Scan(&prior)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := s.now().UTC()
		attempt := prior + 1
		due := now.Add(backoffDelay(attempt))
		_, err = conn.ExecContext(ctx, `INSERT INTO backoff(forge,owner,repo,pr,attempt,last_failure_at,next_due_at) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(forge,owner,repo,pr) DO UPDATE SET attempt=excluded.attempt,last_failure_at=excluded.last_failure_at,next_due_at=excluded.next_due_at`,
			key.Forge, key.Owner, key.Repo, key.PR, attempt, stamp(now), stamp(due))
		value = Backoff{Key: key, Attempt: attempt, LastFailureAt: now, NextDueAt: due}
		return err
	})
	return value, err
}

func (s *Store) Backoff(ctx context.Context, key Key) (Backoff, bool, error) {
	var value Backoff
	value.Key = key
	var last, due string
	err := s.db.QueryRowContext(ctx, "SELECT attempt,last_failure_at,next_due_at FROM backoff WHERE forge=? AND owner=? AND repo=? AND pr=?", keyArgs(key)...).Scan(&value.Attempt, &last, &due)
	if errors.Is(err, sql.ErrNoRows) {
		return Backoff{}, false, nil
	}
	if err != nil {
		return Backoff{}, false, err
	}
	value.LastFailureAt, err = parseStamp(last)
	if err != nil {
		return Backoff{}, false, err
	}
	value.NextDueAt, err = parseStamp(due)
	return value, true, err
}

func (s *Store) ClearBackoff(ctx context.Context, key Key, token int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM backoff WHERE forge=? AND owner=? AND repo=? AND pr=?
		AND EXISTS (SELECT 1 FROM leases WHERE forge=? AND owner=? AND repo=? AND pr=? AND token=?)`,
		append(keyArgs(key), append(keyArgs(key), token)...)...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *Store) UpsertIncident(ctx context.Context, incident Incident) (Incident, error) {
	now := s.now().UTC()
	err := s.immediate(ctx, func(conn *sql.Conn) error {
		var first string
		var retries int
		err := conn.QueryRowContext(ctx, "SELECT first_at,retry_count FROM incidents WHERE forge=? AND owner=? AND repo=? AND pr=? AND category=?", append(keyArgs(incident.Key), incident.Category)...).Scan(&first, &retries)
		if errors.Is(err, sql.ErrNoRows) {
			incident.FirstAt = now
		} else if err != nil {
			return err
		} else if incident.FirstAt, err = parseStamp(first); err != nil {
			return err
		}
		incident.LastAt = now
		incident.RetryCount = retries + 1
		_, err = conn.ExecContext(ctx, `INSERT INTO incidents(forge,owner,repo,pr,category,first_at,last_at,retry_count,observed_head,observed_target,log_location) VALUES(?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(forge,owner,repo,pr,category) DO UPDATE SET last_at=excluded.last_at,retry_count=excluded.retry_count,observed_head=excluded.observed_head,observed_target=excluded.observed_target,log_location=excluded.log_location`,
			incident.Forge, incident.Owner, incident.Repo, incident.PR, incident.Category, stamp(incident.FirstAt), stamp(now), incident.RetryCount, incident.ObservedHead, incident.ObservedTarget, incident.LogLocation)
		return err
	})
	return incident, err
}

func (s *Store) AckIncident(ctx context.Context, key Key, category string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE incidents SET acknowledged_at=? WHERE forge=? AND owner=? AND repo=? AND pr=? AND category=?`, append([]any{stamp(s.now())}, append(keyArgs(key), category)...)...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *Store) ListIncidents(ctx context.Context) ([]Incident, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT forge,owner,repo,pr,category,first_at,last_at,retry_count,
		COALESCE(observed_head,''),COALESCE(observed_target,''),COALESCE(log_location,''),acknowledged_at
		FROM incidents ORDER BY first_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var incidents []Incident
	for rows.Next() {
		var incident Incident
		var first, last string
		var acknowledged sql.NullString
		if err := rows.Scan(&incident.Forge, &incident.Owner, &incident.Repo, &incident.PR, &incident.Category,
			&first, &last, &incident.RetryCount, &incident.ObservedHead, &incident.ObservedTarget, &incident.LogLocation, &acknowledged); err != nil {
			return nil, err
		}
		if incident.FirstAt, err = parseStamp(first); err != nil {
			return nil, err
		}
		if incident.LastAt, err = parseStamp(last); err != nil {
			return nil, err
		}
		if acknowledged.Valid {
			value, parseErr := parseStamp(acknowledged.String)
			if parseErr != nil {
				return nil, parseErr
			}
			incident.AcknowledgedAt = &value
		}
		incidents = append(incidents, incident)
	}
	return incidents, rows.Err()
}

func (s *Store) AddCleanup(ctx context.Context, cleanup Cleanup) error {
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO cleanup_obligations(forge,owner,repo,pr,merged_head,branch,created_at,attempts) VALUES(?,?,?,?,?,?,?,0)
		ON CONFLICT(forge,owner,repo,pr) DO UPDATE SET merged_head=excluded.merged_head,branch=excluded.branch,created_at=excluded.created_at,attempts=0`, cleanup.Forge, cleanup.Owner, cleanup.Repo, cleanup.PR, cleanup.MergedHead, cleanup.Branch, stamp(now))
	return err
}

func (s *Store) ListCleanup(ctx context.Context) ([]Cleanup, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT forge,owner,repo,pr,merged_head,branch,created_at,attempts FROM cleanup_obligations ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []Cleanup
	for rows.Next() {
		var value Cleanup
		var created string
		if err := rows.Scan(&value.Forge, &value.Owner, &value.Repo, &value.PR, &value.MergedHead, &value.Branch, &created, &value.Attempts); err != nil {
			return nil, err
		}
		value.CreatedAt, err = parseStamp(created)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) Cleanup(ctx context.Context, key Key) (Cleanup, bool, error) {
	var value Cleanup
	var created string
	err := s.db.QueryRowContext(ctx, "SELECT forge,owner,repo,pr,merged_head,branch,created_at,attempts FROM cleanup_obligations WHERE forge=? AND owner=? AND repo=? AND pr=?", keyArgs(key)...).
		Scan(&value.Forge, &value.Owner, &value.Repo, &value.PR, &value.MergedHead, &value.Branch, &created, &value.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return Cleanup{}, false, nil
	}
	if err != nil {
		return Cleanup{}, false, err
	}
	value.CreatedAt, err = parseStamp(created)
	if err != nil {
		return Cleanup{}, false, err
	}
	return value, true, nil
}

func (s *Store) RemoveCleanup(ctx context.Context, key Key) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM cleanup_obligations WHERE forge=? AND owner=? AND repo=? AND pr=?", keyArgs(key)...)
	return err
}

func (s *Store) BumpCleanupAttempt(ctx context.Context, key Key) error {
	_, err := s.db.ExecContext(ctx, "UPDATE cleanup_obligations SET attempts=attempts+1 WHERE forge=? AND owner=? AND repo=? AND pr=?", keyArgs(key)...)
	return err
}
