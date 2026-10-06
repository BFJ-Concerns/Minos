package shell

import (
	"net/http"
	"strings"
	"testing"
)

func TestQualityAlertFindsTitleOnSecondPage(t *testing.T) {
	result, writes := runQualityAdaptation(t, "alert", func(r *http.Request) (string, bool) {
		if !strings.HasSuffix(r.URL.Path, "/issues") {
			return "", false
		}
		switch r.URL.Query().Get("page") {
		case "", "1":
			return `[{"number":1,"title":"another alert"}]`, true
		case "2":
			return `[{"number":51,"title":"operator alert"}]`, true
		default:
			return "[]", true
		}
	})
	if result["outcome"] != "applied" {
		t.Fatalf("alert result = %v, want applied", result)
	}
	if len(writes) != 1 || writes[0] != "POST /api/v1/repos/owner/repo/issues/51/comments" {
		t.Fatalf("alert writes = %v, want comment on existing page-two issue", writes)
	}
}
