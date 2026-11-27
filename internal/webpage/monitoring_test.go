package webpage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/shell"
)

func TestOperatorQueueShowsSweepReasons(t *testing.T) {
	var sweep shell.SweepDeferralDocument
	if err := json.Unmarshal([]byte(`{
		"completed_at":"2026-11-27T04:45:00Z","partial":true,
		"deferrals":[
			{"forge":"forgejo","owner":"acme","repo":"widget","pr":"1","reason":"draft","reasons":["draft","open dependencies: acme/other#8"]},
			{"forge":"forgejo","owner":"acme","repo":"widget","pr":"2","reason":"pull request snapshot unavailable"}
		],
		"suppressed":[
			{"owner":"acme","repo":"widget","pr":"3","detail":"all 2/2 run slots are occupied"},
			{"owner":"acme","repo":"widget","pr":"4","blocking_unit":"minos-run-widget-pr4.service"}
		],
		"skipped_repositories":{"acme/offline":"forge unavailable"}
	}`), &sweep); err != nil {
		t.Fatal(err)
	}
	reading := &sweep
	site, _ := testSite(t, Readings{Sweep: func() *shell.SweepDeferralDocument { return reading }}, Settings{})
	if response := visit(site, "/queue", nil); response.Code != http.StatusSeeOther {
		t.Fatalf("queue without session returned %d", response.Code)
	}
	session := signIn(t, site)
	response := visit(site, "/queue", session)
	assertMonitoringPage(t, response.Code, response.Body.String(),
		"2026-11-27T04:45:00Z", "Partial", "Draft pull request", "open dependencies: acme/other#8",
		"acme/widget #2", "pull request snapshot unavailable", "all 2/2 run slots are occupied",
		"Already running", "acme/offline", "forge unavailable")
	reading = nil
	response = visit(site, "/queue", session)
	assertMonitoringPage(t, response.Code, response.Body.String(), "No completed sweep record is available")
}

func TestOperatorLiveRunsShowStatusWithoutOpeningRunDirectories(t *testing.T) {
	var status shell.StatusDocument
	if err := json.Unmarshal([]byte(`{
		"max_concurrent":2,"runs":[
			{"unit":"run-1","run_dir":"/nonexistent","owner":"acme","repo":"widget","pr":"3","head":"abcdef",
			"stage":"review","stages":[{"name":"review","attempt":2,"state":"running"}],
			"timings":{"workers":[{"label":"Explore","phase":"explore","engine":"codex","status":"running"}]}},
			{"unit":"run-2","stage":"starting","error":"run directory could not be read"}
		]
	}`), &status); err != nil {
		t.Fatal(err)
	}
	site, _ := testSite(t, Readings{Status: func(context.Context) (shell.StatusDocument, error) {
		return status, nil
	}}, Settings{})
	response := visit(site, "/live", signIn(t, site))
	assertMonitoringPage(t, response.Code, response.Body.String(), "2 of 2 run slots occupied",
		"acme/widget #3", "abcdef", "review (attempt 2)", "Explore", "explore codex",
		"run-2", "starting", "run directory could not be read")
}

func TestOperatorRecentRunsShowArchivedEvidenceAndLegacyTimings(t *testing.T) {
	now := time.Date(2026, 11, 27, 4, 45, 0, 0, time.UTC)
	site, _ := testSite(t, Readings{RecentRuns: func(_ context.Context, hours, limit int) ([]shell.RecentRun, time.Time, error) {
		if hours != 24 || limit != 60 {
			t.Fatalf("requested %d hours and %d records", hours, limit)
		}
		return []shell.RecentRun{
			{Owner: "acme", Repo: "widget", PR: "4", Timings: json.RawMessage(`{
				"run_summary":{"outcome":"clean","duration_seconds":10,"review_skipped":true,"skip_reason":"Only generated output changed."}
			}`)},
			{Unit: "failed-unit", Timings: json.RawMessage(`{
				"run_summary":{"outcome":"failed","duration_seconds":75,"failure_reason":"stage=setup cause=failed to clone","report_excerpt":"<script>archive text</script>"}
			}`)},
			{Unit: "legacy-unit", Timings: json.RawMessage(`{
				"commands":[{"started_at":"2026-11-27T04:00:00Z","ended_at":"2026-11-27T04:00:45Z"}]
			}`)},
		}, now, nil
	}}, Settings{})
	response := visit(site, "/recent", signIn(t, site))
	body := response.Body.String()
	assertMonitoringPage(t, response.Code, body, "Last 24 hours", "2026-11-27T04:45:00Z", "acme/widget #4", "Clean",
		"10s", "Only generated output changed.", "failed-unit", "Failed", "1m 15s", "During setup — failed to clone",
		"&lt;script&gt;archive text&lt;/script&gt;", "legacy-unit", "Unknown", "45s of recorded activity")
}

func TestOperatorFindingsUseAuthorRepliesAndCurrentThreadState(t *testing.T) {
	repo := shell.RepoConfig{Forge: "forgejo", Owner: "acme", Repo: "widget"}
	const url = "/repositories/forgejo/acme/widget/findings"
	site, _ := testSite(t, Readings{
		Configuration: func() (shell.ServiceConfig, []shell.RepoConfig, error) {
			return shell.ServiceConfig{}, []shell.RepoConfig{repo}, nil
		},
		FindingHistoryPullRequests: func(_ context.Context, selected shell.RepoConfig, page int) ([]shell.FindingHistoryPullRequest, bool, error) {
			if selected.Repo != repo.Repo || page != 2 {
				t.Fatalf("incorrect history query for %v, page %d", selected.Repo, page)
			}
			return []shell.FindingHistoryPullRequest{{Number: 9, Author: "Author"}, {Number: 8}}, true, nil
		},
		ReviewThreads: func(_ context.Context, _ shell.RepoConfig, number int64) ([]forge.ReviewThread, error) {
			if number == 8 {
				return nil, errors.New("temporary forge error")
			}
			opening := forge.ReviewThreadComment{User: "Minos", Body: "Finding"}
			return []forge.ReviewThread{
				{Resolved: true, Comments: []forge.ReviewThreadComment{opening}},
				{Resolved: true, Comments: []forge.ReviewThreadComment{opening, {User: "author", Body: "Rejected: documented behaviour."}}},
				{Outdated: true, Comments: []forge.ReviewThreadComment{opening, {User: "other", Body: "Rejected"}}},
				{Comments: []forge.ReviewThreadComment{opening, {User: "Author", Body: "I disagree with one detail. Looking into it."}}},
			}, nil
		},
	}, Settings{})
	session := signIn(t, site)
	home := visit(site, "/", session)
	assertMonitoringPage(t, home.Code, home.Body.String(), `href="`+url+`"`)
	page := visit(site, url+"?page=2", session)
	assertMonitoringPage(t, page.Code, page.Body.String(), "Finding responses for acme/widget", "#9", "#8",
		`<td class="numeric">1</td><td class="numeric">1</td><td class="numeric">2</td>`,
		"Thread state unavailable", "Newer pull requests", "Older pull requests", "?page=3")
	if missing := visit(site, "/repositories/forgejo/acme/other/findings", session); missing.Code != http.StatusNotFound {
		t.Fatalf("unenrolled repository returned %d", missing.Code)
	}
}

func assertMonitoringPage(t *testing.T, code int, body string, want ...string) {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("want successful page, got %d\n%s", code, body)
	}
	for _, value := range want {
		if !strings.Contains(body, value) {
			t.Errorf("page misses %q\n%s", value, body)
		}
	}
}
