package shell

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const maxWebhookBodyBytes int64 = 1 << 20

func ReceiveCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("receive", flag.ContinueOnError)
	configRoot := fs.String("config", DefaultConfigRoot, "configuration root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := LoadServiceConfig(*configRoot)
	if err != nil {
		return err
	}
	log.Printf("minos receiver listening on %s", cfg.Listener.Bind)
	return http.ListenAndServe(cfg.Listener.Bind, receiverRoutes(ctx, cfg))
}

// receiverRoutes mounts the listener's surface. The status projection is
// mounted only when a token file is configured for it, so a deployment that
// has not been given one serves the webhook route alone.
func receiverRoutes(ctx context.Context, cfg ServiceConfig) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/hooks/", func(w http.ResponseWriter, r *http.Request) {
		if err := handleHook(ctx, cfg, w, r); err != nil {
			log.Printf("hook failed: %v", err)
		}
	})
	if cfg.Listener.StatusTokenFile != "" {
		// One cache for the listener's lifetime: it is what turns a polled
		// route back into an occasional read of the archive host.
		recentRuns := newRecentRunsCache(cfg.RecentTimingsCacheTTL())
		mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
			if err := handleStatus(ctx, cfg, w, r); err != nil {
				log.Printf("status failed: %v", err)
			}
		})
		mux.HandleFunc("/runs/recent", func(w http.ResponseWriter, r *http.Request) {
			if err := handleRecentRuns(ctx, cfg, recentRuns, w, r); err != nil {
				log.Printf("recent runs failed: %v", err)
			}
		})
	}
	return mux
}

func handleHook(ctx context.Context, cfg ServiceConfig, w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil
	}
	forgeName := strings.TrimPrefix(r.URL.Path, "/hooks/")
	forgeConfig, ok := cfg.Forges[forgeName]
	if !ok {
		http.Error(w, "unknown forge", http.StatusNotFound)
		return nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return err
	}
	secret, err := ReadSecret(forgeConfig.WebhookSecretFile)
	if err != nil {
		http.Error(w, "secret unavailable", http.StatusInternalServerError)
		return err
	}
	header := forgeConfig.SignatureHeader
	if header == "" {
		header = "X-Forgejo-Signature"
	}
	if !VerifyHexHMACSHA256(body, secret, r.Header.Get(header)) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return nil
	}
	adaptation, err := NewAdaptation(forgeConfig)
	if err != nil {
		http.Error(w, "forge unavailable", http.StatusInternalServerError)
		return err
	}
	facts, err := adaptation.NormaliseEvent(ctx, body, map[string]string{
		"X-Forgejo-Event": r.Header.Get("X-Forgejo-Event"), "X-Forgejo-Delivery": r.Header.Get("X-Forgejo-Delivery"),
	})
	if err != nil {
		http.Error(w, "bad event", http.StatusBadRequest)
		return err
	}
	if facts.Occasion == "" {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "ignored\n")
		return nil
	}
	facts.Forge = forgeName
	repo, err := FindRepoConfig(cfg, facts)
	if err != nil {
		if errors.Is(err, errRepoNotOptedIn) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, "not opted in\n")
			return nil
		}
		http.Error(w, "repository configuration unavailable", http.StatusInternalServerError)
		return err
	}
	result, err := reconcilePullRequest(ctx, cfg, repo, facts)
	if err != nil {
		http.Error(w, "start failed", http.StatusBadGateway)
		return err
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprintln(w, result.Decision)
	return nil
}
