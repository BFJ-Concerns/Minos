package shell

import "testing"

func TestScrubEnvRemovesOnlyConfiguredVariables(t *testing.T) {
	got := scrubEnv([]string{
		"PUMP19_FORGE_TOKEN=secret",
		"MODEL_KEY=secret",
		"PATH=/bin",
	}, []string{"PUMP19_FORGE_TOKEN", "MODEL_KEY"})
	want := []string{"PATH=/bin"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("scrubbed env = %#v, want %#v", got, want)
	}
}
