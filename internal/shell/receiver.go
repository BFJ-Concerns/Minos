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
	log.Printf("pump19 receiver listening on %s", cfg.Listener.Bind)
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
	facts, err = resolveReceiverFacts(ctx, adaptation, facts)
	if err != nil {
		http.Error(w, "label event resolution failed", http.StatusBadRequest)
		return err
	}
	decision, ok := EvaluateTriggers(facts, repo)
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("no trigger\n"))
		return nil
	}
	label, err := InFlightLabel(decision.Kind)
	if err != nil {
		return err
	}
	if facts.HasLabel(label) && !NewHeadOccasion(facts.Occasion) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("already in flight\n"))
		return nil
	}
	latched, err := hasTerminalMarker(cfg.Runs.Dir, facts, decision.Kind)
	if err != nil {
		http.Error(w, "terminal evidence unavailable", http.StatusInternalServerError)
		return err
	}
	if latched {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("terminal latch\n"))
		return nil
	}
	if err := SpawnRun(ctx, cfg, repo, facts, decision.Kind, facts.Occasion); err != nil {
		if errors.Is(err, ErrRunCapacity) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("deferred: capacity\n"))
			return nil
		}
		if errors.Is(err, ErrRunLedger) {
			http.Error(w, "run ledger unavailable", http.StatusServiceUnavailable)
			return err
		}
		http.Error(w, "spawn failed", http.StatusInternalServerError)
		return err
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprintf(w, "spawned %s\n", decision.Kind)
	return nil
}

func resolveReceiverFacts(ctx context.Context, adaptation Adaptation, facts Facts) (Facts, error) {
	if facts.Occasion != "label-updated" {
		return facts, nil
	}
	event, err := adaptation.LatestLabelEvent(ctx, facts.Owner, facts.Repo, facts.PR)
	if err != nil {
		return Facts{}, err
	}
	if event.Label == "" || event.Action == "" {
		return facts, nil
	}
	facts.Occasion = "label-" + event.Action + ":" + event.Label
	facts.Actor = event.Actor
	if event.Action == "added" {
		actor, err := adaptation.LabelActor(ctx, facts.Owner, facts.Repo, facts.PR, event.Label)
		if err != nil {
			return Facts{}, err
		}
		facts.Actor = actor
	}
	return facts, nil
}
