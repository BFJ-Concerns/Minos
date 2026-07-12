package incidents

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
)

func TestCommandProvidesOperatorAcknowledgementAndRecoveryRoute(t *testing.T) {
	store := filepath.Join(t.TempDir(), "incidents")
	identity := []string{"--store", store, "--forge", "forgejo", "--owner", "owner", "--repo", "repo", "--pr", strconv.Itoa(7), "--category", "stale-oauth"}
	actions := [][]string{
		append([]string{"raise"}, append(identity, "--attempt", "1", "--observed-head", "head-abc", "--diagnostic", "Claude OAuth stale", "--log", "runs/7/run.log")...),
		append([]string{"acknowledge"}, append(identity, "--actor", "operator")...),
		append([]string{"recover"}, identity...),
	}
	var output bytes.Buffer
	for _, args := range actions {
		output.Reset()
		if err := Command(context.Background(), args, &output); err != nil {
			t.Fatal(err)
		}
	}
	var recovered Incident
	if err := json.Unmarshal(output.Bytes(), &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.State != StateRecovered || recovered.AcknowledgedBy != "operator" {
		t.Fatalf("unexpected incident: %#v", recovered)
	}
}

func TestCheckFailsWhileIncidentIsOpen(t *testing.T) {
	store := filepath.Join(t.TempDir(), "incidents")
	identity := []string{"--store", store, "--forge", "forgejo", "--owner", "owner", "--repo", "repo", "--pr", "9", "--category", "engine"}
	if err := Command(context.Background(), append([]string{"raise"}, append(identity, "--diagnostic", "engine unavailable", "--log", "runs/9/run.log")...), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := Command(context.Background(), []string{"check", "--store", store}, &bytes.Buffer{}); err == nil {
		t.Fatal("open incident did not fail external check")
	}
	if err := Command(context.Background(), append([]string{"recover"}, identity...), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := Command(context.Background(), []string{"check", "--store", store}, &bytes.Buffer{}); err != nil {
		t.Fatalf("recovered incident failed external check: %v", err)
	}
}
