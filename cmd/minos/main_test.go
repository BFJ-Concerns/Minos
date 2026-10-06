package main

import (
	"errors"
	"os/exec"
	"testing"
)

func TestExitStatusCarriesAChildExitCodeAndMapsOtherErrorsToOne(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 75").Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("expected an exit error, got %v", err)
	}
	if got := exitStatus(err); got != 75 {
		t.Fatalf("exitStatus(child 75) = %d, want 75", got)
	}
	if got := exitStatus(errors.New("usage")); got != 1 {
		t.Fatalf("exitStatus(plain error) = %d, want 1", got)
	}
}
