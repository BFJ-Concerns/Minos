package shell

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

const retryBackoffBase = 15 * time.Minute
const retryBackoffMaximum = 6 * time.Hour

func retryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := retryBackoffBase
	for step := 1; step < attempt && delay < retryBackoffMaximum; step++ {
		delay *= 2
		if delay > retryBackoffMaximum {
			delay = retryBackoffMaximum
		}
	}
	return delay
}

func retryDue(runDir string, attempt int, now time.Time) (bool, error) {
	path := filepath.Join(runDir, "retry.env")
	values, err := readMetaFile(path)
	if err != nil {
		return false, err
	}
	failureAt, err := time.Parse(time.RFC3339Nano, values["PUMP19_FAILURE_AT"])
	if err != nil {
		// Legacy retry markers predate cadence timestamps. They have already
		// survived at least one deployment interval, so admit their next attempt
		// once rather than inventing a recent timestamp and delaying recovery.
		return true, nil
	}
	return !now.Before(failureAt.Add(retryBackoff(attempt))), nil
}

func hasRetryableFailureMarker(runDir string) bool {
	values, err := readMetaFile(filepath.Join(runDir, "retry.env"))
	return err == nil && values["PUMP19_RETRYABLE_FAILURE"] == "1"
}

func retryBackoffPending(root string, facts Facts, kind RunKind, statuses []Status, now time.Time) (bool, int, error) {
	runDir := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, kind)
	if _, err := os.Stat(runDir); errors.Is(err, os.ErrNotExist) {
		return false, 0, nil
	} else if err != nil {
		return false, 0, err
	}
	if !hasRetryableFailureMarker(runDir) {
		// reapLabelLessClaims leaves only a live or otherwise protected canonical
		// claim here. Its existence is the idempotency claim; never launch a
		// competing unit merely because there is no failure cadence to consult.
		return true, 0, nil
	}
	attempt, retryable, err := retryableAttempt(runDir, kind, statuses)
	if err != nil || !retryable {
		return false, 0, err
	}
	due, err := retryDue(runDir, attempt, now)
	return !due, attempt, err
}
