package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const finishedMarkerFile = "finished.env"

func writeFinishedMarker(runDir string, finishedAt time.Time, outcome string) error {
	data := fmt.Sprintf(
		"MINOS_FINISHED_VERSION=1\nMINOS_FINISHED_AT=%s\nMINOS_FINISHED_OUTCOME=%s\n",
		finishedAt.UTC().Format(time.RFC3339Nano), outcome,
	)
	return atomicPublishFile(filepath.Join(runDir, finishedMarkerFile), []byte(data), 0o644)
}

func readFinishedAt(runDir string) (time.Time, bool, error) {
	values, err := readMetaFile(filepath.Join(runDir, finishedMarkerFile))
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if values["MINOS_FINISHED_VERSION"] != "1" {
		return time.Time{}, false, fmt.Errorf("unsupported finished marker version")
	}
	finishedAt, err := time.Parse(time.RFC3339Nano, values["MINOS_FINISHED_AT"])
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse finished timestamp: %w", err)
	}
	return finishedAt, true, nil
}
