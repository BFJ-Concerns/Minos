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
	"time"

	"bfj/minos/internal/ledger"
	"bfj/minos/internal/reconcile"
)

// Webhooks contain event metadata, not repository contents. One MiB leaves
// ample room for forge payloads while bounding work done before authentication.
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
	mux := http.NewServeMux()
	mux.HandleFunc("/hooks/", func(w http.ResponseWriter, r *http.Request) {
		if err := handleHook(ctx, cfg, w, r); err != nil {
			log.Printf("hook failed: %v", err)
		}
	})
	log.Printf("minos receiver listening on %s", cfg.Listener.Bind)
	return http.ListenAndServe(cfg.Listener.Bind, mux)
}

func handleHook(ctx context.Context, cfg ServiceConfig, w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil
	}
	forgeName := strings.TrimPrefix(r.URL.Path, "/hooks/")
	forge, ok := cfg.Forges[forgeName]
	if !ok {
		http.Error(w, "unknown forge", http.StatusNotFound)
		return nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return err
		}
		http.Error(w, "bad body", http.StatusBadRequest)
		return err
	}
	secret, err := ReadSecret(forge.WebhookSecretFile)
	if err != nil {
		http.Error(w, "secret unavailable", http.StatusInternalServerError)
		return err
	}
	signatureHeader := forge.SignatureHeader
	if signatureHeader == "" {
		signatureHeader = "X-Forgejo-Signature"
	}
	if !VerifyHexHMACSHA256(body, secret, r.Header.Get(signatureHeader)) {
		log.Printf("hook delivery rejected: bad signature forge=%s event=%q delivery=%q", forgeName, r.Header.Get("X-Forgejo-Event"), r.Header.Get("X-Forgejo-Delivery"))
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return nil
	}
	// Authentication is the network seam: logging it here distinguishes a quiet
	// receiver from a forge that reached the box, without waiting on adaptation.
	log.Printf("hook delivery accepted: forge=%s event=%q delivery=%q", forgeName, r.Header.Get("X-Forgejo-Event"), r.Header.Get("X-Forgejo-Delivery"))
	adaptation, err := NewAdaptation(forge)
	if err != nil {
		http.Error(w, "adaptation unavailable", http.StatusInternalServerError)
		return err
	}
	headers := map[string]string{
		"X-Forgejo-Event":     r.Header.Get("X-Forgejo-Event"),
		"X-Forgejo-Delivery":  r.Header.Get("X-Forgejo-Delivery"),
		"X-Forgejo-Signature": r.Header.Get("X-Forgejo-Signature"),
	}
	facts, err := adaptation.NormaliseEvent(ctx, body, headers)
	if err != nil {
		http.Error(w, "normalise failed", http.StatusBadRequest)
		return err
	}
	if facts.Occasion == "" {
		log.Printf("hook delivery ignored: unmapped forge event")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("unmapped event\n"))
		return nil
	}
	facts.Forge = forgeName
	repo, err := FindRepoConfig(cfg.Root, facts)
	if err != nil {
		if !errors.Is(err, errRepoNotOptedIn) {
			http.Error(w, "repository configuration unavailable", http.StatusInternalServerError)
			return err
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("not opted in\n"))
		return nil
	}
	snapshot, err := buildSnapshot(ctx, cfg, repo, adaptation, facts)
	if err != nil {
		http.Error(w, "forge snapshot unavailable", http.StatusBadGateway)
		return err
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		http.Error(w, "run ledger unavailable", http.StatusServiceUnavailable)
		return err
	}
	defer store.Close()
	view, err := ledgerView(ctx, store, snapshot.Key, cfg.Sweep.LivenessThreshold.Duration)
	if err != nil {
		http.Error(w, "run ledger unavailable", http.StatusServiceUnavailable)
		return err
	}
	decision := reconcile.Decide(snapshot, view, time.Now())
	if err := executeDecision(ctx, cfg, repo, facts, snapshot, decision, store, log.Writer()); err != nil {
		if errors.Is(err, ErrRunLedger) {
			http.Error(w, "run ledger unavailable", http.StatusServiceUnavailable)
		} else {
			http.Error(w, "reconcile failed", http.StatusInternalServerError)
		}
		return err
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprintf(w, "%s\n", decision.Kind)
	return nil
}
