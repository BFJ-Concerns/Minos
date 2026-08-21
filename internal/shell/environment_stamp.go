package shell

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
)

// currentEnvironmentStamp identifies the run environment a hold was decided
// under: the minos binary and the run-body environment file. A hold binds to
// the target SHA on the theory the blocking cause lives on the target, but a
// cause can equally live in Minos's own environment; a deploy that changes
// either input produces a new stamp, and the sweep spends every hold bound to
// an older one so the pull request earns one fresh attempt. Overridable for
// tests.
var currentEnvironmentStamp = runEnvironmentStamp

var environmentStamp struct {
	once sync.Once
	// The config root is constant for the life of a process, so the first
	// computation serves every caller.
	value string
}

func runEnvironmentStamp(cfg ServiceConfig) string {
	environmentStamp.once.Do(func() {
		digest := sha256.New()
		if binary, err := os.Executable(); err == nil {
			if content, err := os.ReadFile(binary); err == nil {
				digest.Write(content)
			}
		}
		if content, err := os.ReadFile(filepath.Join(cfg.Root, "run-body.env")); err == nil {
			digest.Write(content)
		}
		environmentStamp.value = hex.EncodeToString(digest.Sum(nil))[:12]
	})
	return environmentStamp.value
}
