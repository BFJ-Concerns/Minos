package forge

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ScriptRunner runs an adaptation's operation scripts as processes, handing
// each the API and web bases and the forge's credential: the one process seam every
// forge's adaptation directory sits behind. A script's standard error is
// its diagnostics: a failed script's is returned with the error, and a
// script that succeeds and still writes some, as one does to say why it
// answered as it did, has it logged, so it reaches the receiver's log or a
// run's own output.
type ScriptRunner struct {
	Directory  string
	APIBase    string
	WebBase    string
	Credential string
}

func (r ScriptRunner) Run(ctx context.Context, request RunRequest) ([]byte, error) {
	if strings.TrimSpace(r.Directory) == "" {
		return nil, fmt.Errorf("adaptation directory is required")
	}
	if strings.TrimSpace(request.Operation) == "" {
		return nil, fmt.Errorf("adaptation operation is required")
	}

	cmd := exec.CommandContext(ctx, filepath.Join(r.Directory, request.Operation), request.Arguments...)
	cmd.Stdin = request.Stdin
	cmd.Env = append(os.Environ(),
		"MINOS_API_BASE="+r.APIBase,
		"MINOS_WEB_BASE="+r.WebBase,
		"MINOS_FORGE_CREDENTIAL="+r.Credential,
	)
	for key, value := range request.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%s: %w: %s", request.Operation, err, strings.TrimSpace(stderr.String()))
	}
	if diagnostics := strings.TrimSpace(stderr.String()); diagnostics != "" {
		log.Printf("%s: %s", request.Operation, diagnostics)
	}
	return stdout.Bytes(), nil
}
