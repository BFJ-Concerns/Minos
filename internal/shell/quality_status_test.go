package shell

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestQualityStatusReportsConfigurationFailure(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "no runs", true: "live run"}[live], func(t *testing.T) {
			cfg := statusTestConfig(t)
			if live {
				writeStatusRunDirectory(t, cfg)
			}
			writeStatusFile(t, filepath.Join(cfg.Root, "repos", "relay.toml"), "invalid = [")
			stubStatusCommands(t)
			if !live {
				original := commandCombinedOutput
				commandCombinedOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
					if name == "systemctl" && slices.Contains(args, "list-units") {
						return nil, nil
					}
					return original(ctx, name, args...)
				}
			}
			request := httptest.NewRequest(http.MethodGet, "/status", nil)
			request.Header.Set("Authorization", "Bearer status-token")
			response := httptest.NewRecorder()
			receiverRoutes(t.Context(), cfg).ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("partial status = %d: %s", response.Code, response.Body.String())
			}
			var document struct {
				Error string      `json:"error"`
				Runs  []statusRun `json:"runs"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(document.Error, "relay.toml") {
				t.Fatalf("configuration diagnostic = %q, want broken config path", document.Error)
			}
			if live {
				if len(document.Runs) != 1 || document.Runs[0].Owner != "example" || document.Runs[0].Error == "" || document.Runs[0].Forge != "" {
					t.Fatalf("partial run = %+v, want orientation identity, visible error and no guessed forge", document.Runs)
				}
			} else if len(document.Runs) != 0 {
				t.Fatalf("runs = %v, want none", document.Runs)
			}
		})
	}
}

func TestQualityStatusReportsTimingFailures(t *testing.T) {
	for _, fault := range []string{"command", "invalid JSON", "unreadable result", "temporary directory"} {
		t.Run(fault, func(t *testing.T) {
			cfg := statusTestConfig(t)
			runDir := writeStatusRunDirectory(t, cfg)
			writeStatusFile(t, filepath.Join(runDir, "review-args.json"), "{}")
			script := filepath.Join(t.TempDir(), "timings")
			body := "#!/bin/sh\necho 'collection diagnostic' >&2\nexit 1\n"
			expected := "collection diagnostic"
			switch fault {
			case "invalid JSON":
				body = "#!/bin/sh\nprintf '{' > \"$2\"\n"
				expected = "invalid timing JSON"
			case "unreadable result":
				body = "#!/bin/sh\nrm \"$2\"\nmkdir \"$2\"\n"
				expected = "read timing result"
			case "temporary directory":
				path := filepath.Join(t.TempDir(), "not-a-directory")
				writeStatusFile(t, path, "obstruction")
				t.Setenv("TMPDIR", path)
				expected = "create timing destination"
			}
			writeScript(t, script, body)
			cfg.Runs.TimingsCommand = script
			stubStatusCommands(t)
			run := readStatusDocument(t, cfg).Runs[0]
			if !strings.Contains(run.Error, expected) {
				t.Fatalf("timing diagnostic = %q, want %q", run.Error, expected)
			}
			if string(run.Timings) != "null" || run.Stage != "review" {
				t.Fatalf("partial timing status = %+v", run)
			}
		})
	}
}
