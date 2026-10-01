package shell

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// These journeys execute the shipped adaptations. The server supplies faulty
// reads, and records only requests the adaptation actually makes.
func TestQualityForgeMalformedReadsWithholdWrites(t *testing.T) {
	for _, bad := range []string{"", "{", "{}", "null", "[null]"} {
		for _, verb := range []string{"guarded-add-reaction", "guarded-remove-reaction", "claim", "guarded-set-status", "guarded-post-review"} {
			// Null is confirmed emptiness only on the reaction endpoint.
			// The positive reaction journeys below cover that contract.
			if bad == "null" && (verb == "guarded-add-reaction" || verb == "guarded-remove-reaction" || verb == "claim") {
				continue
			}
			t.Run(verb+"/"+bad, func(t *testing.T) {
				endpoint := "/reactions"
				if verb == "guarded-set-status" {
					endpoint = "/statuses"
				}
				if verb == "guarded-post-review" {
					endpoint = "/reviews"
				}
				result, writes := runQualityAdaptation(t, verb, func(r *http.Request) (string, bool) {
					if strings.HasSuffix(r.URL.Path, endpoint) {
						return bad, true
					}
					return "", false
				})
				if result["outcome"] != "uncertain" {
					t.Fatalf("outcome = %v, want uncertain; result = %v", result["outcome"], result)
				}
				if len(writes) != 0 {
					t.Fatalf("unreadable forge triggered writes: %v", writes)
				}
			})
		}
	}
}

func TestQualityForgeMalformedGuardIsUncertain(t *testing.T) {
	for _, endpoint := range []string{"user", "pull"} {
		for _, bad := range []string{"", "{", "[]", "null"} {
			t.Run(endpoint+"/"+bad, func(t *testing.T) {
				result, writes := runQualityAdaptation(t, "guarded-add-reaction", func(r *http.Request) (string, bool) {
					match := r.URL.Path == "/api/v1/user"
					if endpoint == "pull" {
						match = strings.HasSuffix(r.URL.Path, "/pulls/17")
					}
					return bad, match
				})
				if result["outcome"] != "uncertain" {
					t.Fatalf("guard outcome = %v, want uncertain; result = %v", result["outcome"], result)
				}
				if len(writes) != 0 {
					t.Fatalf("unreadable guard triggered writes: %v", writes)
				}
			})
		}
	}
}

func TestQualityForgeMalformedReviewCommentsAreUnreadable(t *testing.T) {
	for _, bad := range []string{"", "{", "{}", "null", "[null]"} {
		t.Run(bad, func(t *testing.T) {
			result, writes := runQualityAdaptation(t, "guarded-post-review", func(r *http.Request) (string, bool) {
				if strings.HasSuffix(r.URL.Path, "/reviews") {
					if r.URL.Query().Get("page") == "2" {
						return "[]", true
					}
					return `[{"id":1,"commit_id":"head","state":"COMMENT","body":"review","user":{"login":"Minos"}}]`, true
				}
				if strings.HasSuffix(r.URL.Path, "/comments") {
					return bad, true
				}
				return "", false
			})
			if result["outcome"] != "uncertain" || result["reason"] != "review read-back could not be confirmed" {
				t.Fatalf("review result = %v, want unreadable review read-back", result)
			}
			if len(writes) != 0 {
				t.Fatalf("unreadable comments triggered writes: %v", writes)
			}
		})
	}
}

func runQualityAdaptation(t *testing.T, verb string, read func(*http.Request) (string, bool)) (map[string]any, []string) {
	t.Helper()
	var mu sync.Mutex
	var writes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			mu.Lock()
			writes = append(writes, r.Method+" "+r.URL.Path)
			mu.Unlock()
			fmt.Fprint(w, `{}`)
			return
		}
		if response, handled := read(r); handled {
			fmt.Fprint(w, response)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/user":
			fmt.Fprint(w, `{"login":"Minos"}`)
		case strings.HasSuffix(r.URL.Path, "/pulls/17"):
			fmt.Fprint(w, `{"number":17,"base":{"repo":{"full_name":"owner/repo"}},"state":"open","merged":false,"head":{"sha":"head"},"requested_reviewers":[{"login":"Minos"}]}`)
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	defer server.Close()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "forgejo", verb))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"owner", "repo", "17", "head", "target", "Minos"}
	input := ""
	switch verb {
	case "guarded-add-reaction", "guarded-remove-reaction":
		args = append(args, "+1")
	case "claim":
		args = []string{"owner", "repo", "17", "Minos"}
	case "guarded-set-status":
		args = append(args, "Minos", "success", "clean")
	case "guarded-post-review":
		input = `{"state":"COMMENT","body":"review","comments":[{"path":"a.go","body":"finding","new_position":1,"old_position":0}]}`
	case "alert":
		args = []string{"owner", "repo"}
		input = `{"title":"operator alert","body":"diagnostic"}`
	}
	cmd := exec.CommandContext(t.Context(), script, args...)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "MINOS_API_BASE="+server.URL, "MINOS_FORGE_TOKEN=fixture-token", "MINOS_STATUS_CONTEXT=Minos", `MINOS_MARKERS={"in-flight":{"reaction":"eyes"}}`)
	cmd.Stdin = strings.NewReader(input)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("adaptation failed: %v; stdout = %s", err, output)
	}
	var result map[string]any
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode adaptation output: %v: %s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	return result, append([]string(nil), writes...)
}

func TestQualityForgeMergedCleanupGuardIsUncertain(t *testing.T) {
	for _, endpoint := range []string{"user", "pull"} {
		for _, bad := range []string{"", "{", "[]", "null"} {
			t.Run(endpoint+"/"+bad, func(t *testing.T) {
				userReads, pullReads := 0, 0
				result, writes := runQualityAdaptation(t, "guarded-remove-reaction", func(r *http.Request) (string, bool) {
					if strings.HasSuffix(r.URL.Path, "/reactions") {
						return `[{"content":"+1","user":{"login":"Minos"}}]`, true
					}
					if r.URL.Path == "/api/v1/user" {
						userReads++
						if endpoint == "user" && userReads == 2 {
							return bad, true
						}
					}
					if strings.HasSuffix(r.URL.Path, "/pulls/17") {
						pullReads++
						if endpoint == "pull" && pullReads == 2 {
							return bad, true
						}
						return `{"number":17,"base":{"repo":{"full_name":"owner/repo"}},"state":"closed","merged":true,"head":{"sha":"head"}}`, true
					}
					return "", false
				})
				if result["outcome"] != "uncertain" {
					t.Fatalf("merged cleanup result = %v, want uncertain", result)
				}
				if len(writes) != 0 {
					t.Fatalf("unreadable merged guard triggered writes: %v", writes)
				}
			})
		}
	}
}

func TestQualityForgeMergedCleanupRemovesMarker(t *testing.T) {
	state := newForgejoFixtureState(t)
	state.reactions = []string{"eyes"}
	state.changePullRequest(func(pr map[string]any) { pr["state"] = "closed"; pr["merged"] = true })
	configureForgeCommandFixture(t, state)
	if err := ForgeCommand(t.Context(), []string{"marker", state.headSHA(), state.targetSHA(), "in-flight", "remove"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("merged marker cleanup failed: %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if slices.Contains(state.reactions, "eyes") || state.reactionDeleteWrites != 1 {
		t.Fatalf("merged cleanup reactions = %v, deletes = %d, want removed marker and one delete", state.reactions, state.reactionDeleteWrites)
	}
}

func TestQualityForgeNullReactionPagesAllowConfirmedWrites(t *testing.T) {
	for _, action := range []string{"add", "claim", "remove", "already absent", "add after null tail", "claim after null tail"} {
		t.Run(action, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			if action == "remove" {
				state.reactions = []string{"eyes"}
			}
			if strings.HasSuffix(action, "null tail") {
				state.reactions = []string{"unrelated"}
				state.reactionPageSize = 1
			}
			configureForgeCommandFixture(t, state)
			args := []string{"marker", state.headSHA(), state.targetSHA(), "in-flight", "add"}
			if strings.HasPrefix(action, "claim") {
				args = []string{"claim"}
			} else if action == "remove" || action == "already absent" {
				args[len(args)-1] = "remove"
			}
			var output bytes.Buffer
			if err := ForgeCommand(t.Context(), args, &output); err != nil {
				t.Fatalf("null reaction %s failed: %v; output = %s", action, err, output.Bytes())
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			wantPresent, wantAdds, wantDeletes := true, 1, 0
			if action == "remove" {
				wantPresent, wantAdds, wantDeletes = false, 0, 1
			}
			if action == "already absent" {
				wantPresent, wantAdds = false, 0
			}
			if slices.Contains(state.reactions, "eyes") != wantPresent || state.reactionWrites != wantAdds || state.reactionDeleteWrites != wantDeletes {
				t.Fatalf("null reaction %s: reactions = %v, adds = %d, deletes = %d; want present = %t, adds = %d, deletes = %d", action, state.reactions, state.reactionWrites, state.reactionDeleteWrites, wantPresent, wantAdds, wantDeletes)
			}
		})
	}
}
