package preflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"bfj/minos/internal/atomicreplace"
)

type CachedGate struct {
	ConfigPath string
	CachePath  string
	TTL        time.Duration
	Now        func() time.Time
	Run        func(context.Context, Config) Report
}

type cachedReport struct {
	Schema       int       `json:"schema"`
	ConfigDigest string    `json:"config_digest"`
	CheckedAt    time.Time `json:"checked_at"`
	Report       Report    `json:"report"`
}

func (gate CachedGate) Check(ctx context.Context) (Report, bool, error) {
	if gate.ConfigPath == "" || gate.CachePath == "" || gate.TTL <= 0 {
		return Report{}, false, fmt.Errorf("preflight cache requires config path, cache path, and positive TTL")
	}
	// Receiver requests and the sweep can race across processes. Serialise the
	// miss-check-probe-write sequence so one burst spends one engine probe, not
	// one probe per contender.
	lock, err := acquireCacheLock(ctx, gate.CachePath+".lock")
	if err != nil {
		return Report{}, false, err
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()
	configuration, err := os.ReadFile(gate.ConfigPath)
	if err != nil {
		return Report{}, false, err
	}
	digestBytes := sha256.Sum256(configuration)
	digest := hex.EncodeToString(digestBytes[:])
	now := time.Now
	if gate.Now != nil {
		now = gate.Now
	}
	checkedAt := now().UTC()
	if cached, err := readCachedReport(gate.CachePath); err == nil && cached.Schema == 1 && cached.ConfigDigest == digest && !checkedAt.Before(cached.CheckedAt) && checkedAt.Sub(cached.CheckedAt) < gate.TTL {
		return cached.Report, true, nil
	}

	cfg, err := LoadConfig(gate.ConfigPath)
	if err != nil {
		return Report{}, false, err
	}
	run := gate.Run
	if run == nil {
		run = func(ctx context.Context, cfg Config) Report { return Run(ctx, Probes(cfg)) }
	}
	report := run(ctx, cfg)
	data, err := json.Marshal(cachedReport{Schema: 1, ConfigDigest: digest, CheckedAt: checkedAt, Report: report})
	if err != nil {
		return Report{}, false, err
	}
	data = append(data, '\n')
	if err := atomicreplace.Write(gate.CachePath, data, 0o600); err != nil {
		return Report{}, false, err
	}
	return report, false, nil
}

func acquireCacheLock(ctx context.Context, path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func readCachedReport(path string) (cachedReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cachedReport{}, err
	}
	var cached cachedReport
	if err := json.Unmarshal(data, &cached); err != nil {
		return cachedReport{}, err
	}
	if cached.CheckedAt.IsZero() || cached.Schema != 1 {
		return cachedReport{}, errors.New("invalid preflight cache record")
	}
	return cached, nil
}
