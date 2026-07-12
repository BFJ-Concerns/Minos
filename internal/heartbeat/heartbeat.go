// Package heartbeat publishes a small file that a monitor outside the Minos
// service can age-check. The service does not get to declare itself alive.
package heartbeat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"bfj/minos/internal/atomicreplace"
)

type Record struct {
	At time.Time `json:"at"`
}

func Write(path string, at time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := json.Marshal(Record{At: at.UTC()})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicreplace.Write(path, data, 0o640)
}

func Check(path string, now time.Time, maximumAge time.Duration) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("heartbeat unavailable: %w", err)
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return fmt.Errorf("heartbeat invalid: %w", err)
	}
	age := now.Sub(record.At)
	if age < 0 {
		return fmt.Errorf("heartbeat is %s in the future", -age)
	}
	if age > maximumAge {
		return fmt.Errorf("heartbeat stale: age %s exceeds %s", age.Round(time.Second), maximumAge)
	}
	return nil
}
