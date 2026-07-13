package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetryableExitRecordCategoryValidation(t *testing.T) {
	tests := []struct {
		name     string
		category string
		valid    bool
	}{
		{name: "host capacity", category: "host-capacity", valid: true},
		{name: "engine unavailable", category: "engine-unavailable", valid: true},
		{name: "stale OAuth", category: "stale-oauth", valid: true},
		{name: "forge unavailable", category: "forge-unavailable", valid: true},
		{name: "network unavailable", category: "network-unavailable", valid: true},
		{name: "empty", category: "", valid: false},
		{name: "spaces", category: "stale oauth", valid: false},
		{name: "uppercase", category: "Stale-OAuth", valid: false},
		{name: "unrecognised", category: "another-transient", valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runDir := t.TempDir()
			err := writeRetryableExitRecord(runDir, test.category)
			if test.valid && err != nil {
				t.Fatal(err)
			}
			if !test.valid && err == nil {
				t.Fatalf("category %q was accepted", test.category)
			}
		})
	}
}

func TestRetryableExitRecordRejectsUnrecognisedOrTrailingData(t *testing.T) {
	tests := []string{
		`{"schema":1,"category":"stale-oauth","diagnostic":"raw backend text"}`,
		`{"schema":1,"category":"stale-oauth"} trailing`,
		`{"schema":2,"category":"stale-oauth"}`,
	}
	for index, data := range tests {
		runDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(runDir, retryableExitMarkerName), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readRetryableExitRecord(runDir); err == nil {
			t.Fatalf("case %d accepted invalid record %q", index, data)
		}
	}
}
