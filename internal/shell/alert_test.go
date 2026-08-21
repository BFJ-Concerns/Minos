package shell

import (
	"context"
	"strings"
	"testing"
)

func TestRepoSkipCountingAlertsOncePerOutageAtTheThreshold(t *testing.T) {
	original := operatorAlert
	t.Cleanup(func() { operatorAlert = original })
	var alerts []string
	operatorAlert = func(_ context.Context, _ ServiceConfig, title, body string) {
		alerts = append(alerts, title+"\n"+body)
	}
	cfg := scratchTestConfig(t)

	for pass := 1; pass < repoSkipAlertThreshold; pass++ {
		recordRepoSkips(t.Context(), cfg, map[string]string{"owner/vanished": "listing returned 404"})
	}
	if len(alerts) != 0 {
		t.Fatalf("alerts before the threshold = %d, want none", len(alerts))
	}
	recordRepoSkips(t.Context(), cfg, map[string]string{"owner/vanished": "listing returned 404"})
	if len(alerts) != 1 {
		t.Fatalf("alerts at the threshold = %d, want one", len(alerts))
	}
	if !strings.Contains(alerts[0], "owner/vanished") || !strings.Contains(alerts[0], "listing returned 404") {
		t.Fatalf("alert does not carry the repo and error: %q", alerts[0])
	}
	recordRepoSkips(t.Context(), cfg, map[string]string{"owner/vanished": "listing returned 404"})
	if len(alerts) != 1 {
		t.Fatalf("alerts past the threshold = %d, want still one", len(alerts))
	}
}

func TestRepoSkipCountingResetsWhenTheRepoSweepsAgain(t *testing.T) {
	original := operatorAlert
	t.Cleanup(func() { operatorAlert = original })
	var alerts int
	operatorAlert = func(context.Context, ServiceConfig, string, string) { alerts++ }
	cfg := scratchTestConfig(t)

	for pass := 1; pass < repoSkipAlertThreshold; pass++ {
		recordRepoSkips(t.Context(), cfg, map[string]string{"owner/flaky": "transient outage"})
	}
	recordRepoSkips(t.Context(), cfg, map[string]string{})
	for pass := 1; pass < repoSkipAlertThreshold; pass++ {
		recordRepoSkips(t.Context(), cfg, map[string]string{"owner/flaky": "fresh outage"})
	}
	if alerts != 0 {
		t.Fatalf("alerts = %d, want none: recovery must reset the counter", alerts)
	}
	recordRepoSkips(t.Context(), cfg, map[string]string{"owner/flaky": "fresh outage"})
	if alerts != 1 {
		t.Fatalf("alerts = %d, want one for the fresh outage", alerts)
	}
}
