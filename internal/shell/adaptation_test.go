package shell

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdaptationRunUsesRunnerValidation(t *testing.T) {
	for _, test := range []struct{ name, dir, operation, want string }{
		{"directory", " ", "operation", "adaptation directory is required"},
		{"operation", t.TempDir(), " ", "adaptation operation is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := (Adaptation{Dir: test.dir}).Run(t.Context(), test.operation, nil, nil)
			if err == nil || err.Error() != test.want {
				t.Fatalf("Run error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAdaptationRunCarriesProcessInputsAndFailureOutput(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s|%s|%s|%s|' "$MINOS_API_BASE" "$MINOS_FORGE_CREDENTIAL" "$MINOS_TEST_VALUE" "$1"
cat
printf 'failure detail\n' >&2
exit 7
`
	if err := os.WriteFile(filepath.Join(dir, "operation"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	out, err := (Adaptation{Dir: dir, APIBase: "fixture-api", Credential: "fixture-credential"}).Run(t.Context(), "operation", strings.NewReader("input"), map[string]string{"MINOS_TEST_VALUE": "extra"}, "argument")
	if !bytes.Equal(out, []byte("fixture-api|fixture-credential|extra|argument|input")) {
		t.Fatalf("stdout = %q", out)
	}
	if err == nil || !strings.Contains(err.Error(), "operation: exit status 7: failure detail") {
		t.Fatalf("error = %v", err)
	}
}
