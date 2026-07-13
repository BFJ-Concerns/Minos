package shell

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/ledger"
	"bfj/minos/internal/product"
)

func TestStrandedStateConvergesFromEveryReconciliationPrompt(t *testing.T) {
	for _, prompt := range []string{"receiver", "sweep"} {
		t.Run(prompt, func(t *testing.T) {
			cfg, facts, reaction, removals := prepareStrandedPromptFixture(t)
			originalSystemctl := systemctlCommand
			defer func() { systemctlCommand = originalSystemctl }()
			systemctlCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				if args[1] == "show" {
					return exec.CommandContext(ctx, "sh", "-c", "printf inactive")
				}
				return exec.CommandContext(ctx, "true")
			}

			switch prompt {
			case "receiver":
				body := []byte(`{"action":"closed"}`)
				mac := hmac.New(sha256.New, []byte("secret"))
				_, _ = mac.Write(body)
				request := httptest.NewRequest(http.MethodPost, "/hooks/local", strings.NewReader(string(body)))
				request.Header.Set("X-Forgejo-Event", "pull_request")
				request.Header.Set("X-Forgejo-Signature", hex.EncodeToString(mac.Sum(nil)))
				response := httptest.NewRecorder()
				if err := handleHook(t.Context(), cfg, response, request); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusAccepted || response.Body.String() != "cleanup\n" {
					t.Fatalf("receiver response = %d %q", response.Code, response.Body.String())
				}
			case "sweep":
				if err := SweepCommand(t.Context(), []string{"--config", cfg.Root}); err != nil {
					t.Fatal(err)
				}
			}

			store, err := ledger.Open(ledgerPath(cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			key := coordinationKey(facts)
			if cleanup, found, err := store.Cleanup(t.Context(), key); err != nil || found {
				t.Fatalf("cleanup remains after %s prompt: cleanup=%#v found=%v err=%v", prompt, cleanup, found, err)
			}
			if lease, found, err := store.Lease(t.Context(), key); err != nil || found {
				t.Fatalf("lease remains after %s prompt: lease=%#v found=%v err=%v", prompt, lease, found, err)
			}
			assertPresenceClosed(t, reaction, removals)
		})
	}
}

func prepareStrandedPromptFixture(t *testing.T) (ServiceConfig, Facts, string, string) {
	t.Helper()
	cfg, _, facts := coordinationConfig(t)
	if err := os.MkdirAll(filepath.Join(cfg.Root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := `forge = "local"
owner = "owner"
repo = "subject"

[adaptation]
build = "true"
test = "true"
skill = "skill"

[[eligibility]]
authors = ["*"]
`
	if err := os.WriteFile(filepath.Join(cfg.Root, "repos", "subject.toml"), []byte(repoConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	reaction, removals := presenceProbe(t, cfg)
	if err := os.WriteFile(reaction, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	adaptation := cfg.Forges["local"].Adaptation
	writeScript(t, filepath.Join(adaptation, "normalise-event"), "#!/bin/sh\nprintf 'OCCASION=pr-closed\\nOWNER=owner\\nREPO=subject\\nPR=7\\nHEAD_SHA=head-1\\nBASE_REF=main\\nAUTHOR=alice\\nOPEN=false\\nMERGED=true\\n'\n")
	writeScript(t, filepath.Join(adaptation, "list-open-prs"), "#!/bin/sh\nexit 0\n")
	forgeSnapshot := forge.Snapshot{
		AuthenticatedUser: "Minos", Repository: "owner/subject", PullRequest: 7,
		State: "closed", Merged: true, Author: "alice", HeadSHA: facts.HeadSHA, HeadBranch: "change",
		HeadRepository: "owner/subject", TargetSHA: "target-merged", TargetBranch: "main",
		TargetRepository: "owner/subject", DefaultBranch: "main",
		Statuses: []forge.Status{{
			ID: 1, Provider: forge.ForgejoProvider, Context: forge.OwnedStatusContext,
			State: forge.StatusSuccess, Creator: "Minos", Description: product.Merged().Description(),
		}},
	}
	data, err := json.Marshal(forgeSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptation, "snapshot"), "#!/bin/sh\nprintf '%s\\n' "+strconv.Quote(string(data))+"\n")
	writeScript(t, filepath.Join(adaptation, "delete-branch"), "#!/bin/sh\nprintf '%s\\n' '{\"outcome\":\"retryable\",\"reason\":\"branch deletion should be retried\"}'\n")

	staleAt := time.Now().Add(-time.Hour)
	store, err := ledger.OpenWithClock(ledgerPath(cfg), func() time.Time { return staleAt })
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(t.Context(), ledger.Lease{
		Key: coordinationKey(facts), ObservedHead: facts.HeadSHA, ObservedTarget: "target-merged",
		Unit: "dead-unit", Workspace: t.TempDir(),
	}, 2)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.AddCleanup(t.Context(), ledger.Cleanup{Key: lease.Key, MergedHead: facts.HeadSHA, Branch: "change"}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	for range 7 {
		if err := store.BumpCleanupAttempt(t.Context(), lease.Key); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return cfg, facts, reaction, removals
}
