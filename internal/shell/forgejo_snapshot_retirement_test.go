package shell

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

func TestForgejoSnapshotRetiresMergeMetadataAndSourceProtection(t *testing.T) {
	for _, refuseSource := range []bool{false, true} {
		t.Run(strconv.FormatBool(refuseSource), func(t *testing.T) {
			f := newStatusReaderFixture(t, nil, false, refuseSource)
			out, err := f.runner.Run(t.Context(), forge.RunRequest{Operation: "snapshot", Arguments: []string{"owner", "repo", "17"}})
			if err != nil {
				t.Fatalf("review snapshot must not require source-branch protection: %v", err)
			}
			var snapshot map[string]json.RawMessage
			if err := json.Unmarshal(out, &snapshot); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"mergeable", "source_protected", "can_merge", "allowed_merge_methods", "head_repository", "default_branch", "protection_satisfied"} {
				if _, present := snapshot[key]; present {
					t.Errorf("snapshot still emits retired field %q: %s", key, out)
				}
			}
			for key, want := range map[string]string{"target_sha": `"target"`, "head_branch": `"topic"`, "merged": "false"} {
				if string(snapshot[key]) != want {
					t.Errorf("snapshot %s = %s, want %s", key, snapshot[key], want)
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			want := []string{"/api/v1/repos/owner/repo/branches/main"}
			if !reflect.DeepEqual(f.branches, want) {
				t.Errorf("branch requests = %v, want target branch only: %v", f.branches, want)
			}
		})
	}
}
