package preflight

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type ForgeProbe struct {
	APIBase              string
	CredentialFile       string
	ExpectedLogin        string
	PermissionRepository string
	RequiredPermissions  []string
	Client               *http.Client
}

func (probe ForgeProbe) Run(ctx context.Context) Result {
	token, err := os.ReadFile(probe.CredentialFile)
	if err != nil {
		return Result{Name: "forge", Diagnostic: fmt.Sprintf("credential unavailable: %v", err)}
	}
	client := probe.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := forgeGet(ctx, client, probe.APIBase+"/user", strings.TrimSpace(string(token)), &user); err != nil {
		return Result{Name: "forge", Diagnostic: err.Error()}
	}
	if user.Login != probe.ExpectedLogin {
		return Result{Name: "forge", Diagnostic: fmt.Sprintf("identity mismatch: got %q, want %q", user.Login, probe.ExpectedLogin)}
	}
	var repository struct {
		Permissions map[string]bool `json:"permissions"`
	}
	if err := forgeGet(ctx, client, probe.APIBase+"/repos/"+probe.PermissionRepository, strings.TrimSpace(string(token)), &repository); err != nil {
		return Result{Name: "forge", Diagnostic: err.Error()}
	}
	for _, permission := range probe.RequiredPermissions {
		if !repository.Permissions[permission] {
			return Result{Name: "forge", Diagnostic: fmt.Sprintf("identity %s lacks %s on %s", user.Login, permission, probe.PermissionRepository)}
		}
	}
	return Result{Name: "forge", Passed: true, Diagnostic: fmt.Sprintf("identity %s has %s on %s", user.Login, strings.Join(probe.RequiredPermissions, ","), probe.PermissionRepository)}
}

func forgeGet(ctx context.Context, client *http.Client, url, token string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "token "+token)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("forge unreachable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("forge returned HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return fmt.Errorf("forge response invalid: %w", err)
	}
	return nil
}

type EngineProbe struct {
	Engines []EngineConfig
	Runner  commandRunner
}

func (probe EngineProbe) Run(ctx context.Context) Result {
	result := Result{Name: "engine", Passed: true}
	for _, engine := range probe.Engines {
		for _, model := range engine.Models {
			args := make([]string, len(engine.Args))
			for index, arg := range engine.Args {
				args[index] = strings.ReplaceAll(arg, "{model}", model)
			}
			// Engine output is inspected only long enough to classify the known
			// failure shape; the returned deployment diagnostic is service-owned.
			commandResult := CommandProbe{Name: "engine", Command: engine.Command, Args: args, Timeout: 30 * time.Second, Runner: probe.Runner, ExposeOutput: true}.Run(ctx)
			check := Check{Name: engine.Name + ":" + model, Passed: commandResult.Passed, Diagnostic: "pinned model served successfully"}
			if !commandResult.Passed {
				classification := ClassifyBackendFailure(BackendFailure{
					StatusCode: inferredStatus(commandResult.Diagnostic),
					Message:    commandResult.Diagnostic,
					Immediate:  true,
					CostUSD:    0,
				})
				check.Diagnostic = fmt.Sprintf("%s; recovery: %s", classification.Kind, classification.Recovery)
				result.Passed = false
			}
			result.Checks = append(result.Checks, check)
		}
	}
	if result.Passed {
		result.Diagnostic = fmt.Sprintf("all %d pinned models served successfully", len(result.Checks))
	} else {
		result.Diagnostic = fmt.Sprintf("%d of %d pinned models failed", failedChecks(result.Checks), len(result.Checks))
	}
	return result
}

func failedChecks(checks []Check) int {
	failed := 0
	for _, check := range checks {
		if !check.Passed {
			failed++
		}
	}
	return failed
}

func inferredStatus(diagnostic string) int {
	lower := strings.ToLower(diagnostic)
	if strings.Contains(lower, "403") || strings.Contains(lower, "organization has disabled claude subscription access") {
		return http.StatusForbidden
	}
	if strings.Contains(lower, "429") || strings.Contains(lower, "quota") || strings.Contains(lower, "usage limit") {
		return http.StatusTooManyRequests
	}
	return 0
}

type RegistryProbe struct {
	Enabled bool
	Command string
	Args    []string
	Runner  commandRunner
}

func (probe RegistryProbe) Run(ctx context.Context) Result {
	if !probe.Enabled {
		return Result{Name: "registry", Passed: true, Diagnostic: "not configured (optional)"}
	}
	result := CommandProbe{Name: "registry", Command: probe.Command, Args: probe.Args, Timeout: 10 * time.Second, Runner: probe.Runner}.Run(ctx)
	return result
}

type AlertProbe struct{ Directory string }

func (probe AlertProbe) Run(context.Context) Result {
	if err := os.MkdirAll(probe.Directory, 0o750); err != nil {
		return Result{Name: "alert", Diagnostic: fmt.Sprintf("incident stream unavailable: %v", err)}
	}
	file, err := os.CreateTemp(probe.Directory, ".preflight-*")
	if err != nil {
		return Result{Name: "alert", Diagnostic: fmt.Sprintf("incident stream not writable: %v", err)}
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		return Result{Name: "alert", Diagnostic: fmt.Sprintf("incident stream close failed: %v", err)}
	}
	if err := os.Remove(name); err != nil {
		return Result{Name: "alert", Diagnostic: fmt.Sprintf("incident stream cleanup failed: %v", err)}
	}
	return Result{Name: "alert", Passed: true, Diagnostic: "incident stream is writable"}
}

type ToolchainProbe struct{ Binaries []string }

func (probe ToolchainProbe) Run(context.Context) Result {
	for _, binary := range probe.Binaries {
		path, err := exec.LookPath(binary)
		if err != nil {
			return Result{Name: "toolchain", Diagnostic: fmt.Sprintf("%s is not on PATH", binary)}
		}
		if !filepath.IsAbs(path) && !strings.Contains(path, string(filepath.Separator)) {
			return Result{Name: "toolchain", Diagnostic: fmt.Sprintf("%s resolved unexpectedly to %s", binary, path)}
		}
	}
	return Result{Name: "toolchain", Passed: true, Diagnostic: fmt.Sprintf("found %d required binaries", len(probe.Binaries))}
}

func Probes(cfg Config) []Probe {
	return []Probe{
		ForgeProbe{APIBase: strings.TrimRight(cfg.Forge.APIBase, "/"), CredentialFile: cfg.Forge.CredentialFile, ExpectedLogin: cfg.Forge.ExpectedLogin, PermissionRepository: cfg.Forge.PermissionRepository, RequiredPermissions: cfg.Forge.RequiredPermissions},
		EngineProbe{Engines: cfg.Engines},
		RegistryProbe{Enabled: cfg.Registry.Enabled, Command: cfg.Registry.Command, Args: cfg.Registry.Args},
		AlertProbe{Directory: cfg.Alert.Directory},
		ToolchainProbe{Binaries: cfg.Toolchain.Binaries},
	}
}
