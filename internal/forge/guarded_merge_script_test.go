package forge

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGuardedMergeRetriesOutcomeDiscoveryWithoutInventingSuccess(t *testing.T) {
	adaptation := t.TempDir()
	copyExecutable(t, filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "common.sh"), filepath.Join(adaptation, "common.sh"))
	copyExecutable(t, filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "guarded-merge"), filepath.Join(adaptation, "guarded-merge"))

	bin := t.TempDir()
	state := filepath.Join(t.TempDir(), "state")
	posts := filepath.Join(t.TempDir(), "posts")
	count := filepath.Join(t.TempDir(), "count")
	sleeps := filepath.Join(t.TempDir(), "sleeps")
	writeExecutable(t, filepath.Join(bin, "sleep"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$1\" >>\"$TEST_SLEEP_LOG\"\n")
	writeExecutable(t, filepath.Join(bin, "curl"), `#!/usr/bin/env sh
set -eu
method="GET"
url=""
output=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -X) method="$2"; shift 2 ;;
    -o) output="$2"; shift 2 ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  */api/v1/user) printf '%s\n' '{"login":"Minos"}' ;;
  */branches/main) printf '%s\n' '{"commit":{"id":"expected-target"},"user_can_merge":true}' ;;
  */pulls/4/merge)
    post_count=0
    [ ! -f "$TEST_MERGE_POSTS" ] || post_count="$(cat "$TEST_MERGE_POSTS")"
    post_count=$((post_count + 1))
    printf '%s\n' "$post_count" >"$TEST_MERGE_POSTS"
    : >"$TEST_MERGE_POSTED"
    if { [ "$TEST_MERGE_VISIBILITY" = "transient-refusal" ] && [ "$post_count" -eq 1 ]; } ||
       [ "$TEST_MERGE_VISIBILITY" = "transient-never" ]; then
      printf '%s\n' '{"message":"Please try again later"}' >"$output"
      printf '%s' '405'
    else
      printf '%s\n' '{}' >"$output"
      printf '%s' '200'
    fi
    ;;
  */pulls/4)
    if [ ! -f "$TEST_MERGE_POSTED" ]; then
      printf '%s\n' '{"number":4,"state":"open","merged":false,"head":{"sha":"expected-head"},"base":{"ref":"main","repo":{"full_name":"acme/widget"}}}'
      exit 0
    fi
    current=0
    [ ! -f "$TEST_DISCOVERY_COUNT" ] || current="$(cat "$TEST_DISCOVERY_COUNT")"
    current=$((current + 1))
    printf '%s\n' "$current" >"$TEST_DISCOVERY_COUNT"
    post_count="$(cat "$TEST_MERGE_POSTS")"
    if { [ "$TEST_MERGE_VISIBILITY" = "eventual" ] && [ "$current" -ge 3 ]; } ||
       { [ "$TEST_MERGE_VISIBILITY" = "transient-refusal" ] && [ "$post_count" -ge 2 ]; }; then
      printf '%s\n' '{"number":4,"state":"closed","merged":true,"head":{"sha":"expected-head"},"base":{"ref":"main","repo":{"full_name":"acme/widget"}}}'
    else
      printf '%s\n' '{"number":4,"state":"open","merged":false,"head":{"sha":"expected-head"},"base":{"ref":"main","repo":{"full_name":"acme/widget"}}}'
    fi
    ;;
  *) echo "unexpected curl URL: $url" >&2; exit 2 ;;
esac
`)

	tests := []struct {
		name       string
		visibility string
		want       WriteOutcome
		wantReads  int
		wantPosts  int
	}{
		{name: "eventual visibility is discovered", visibility: "eventual", want: WriteApplied, wantReads: 3, wantPosts: 1},
		{name: "exhausted discovery remains uncertain", visibility: "never", want: WriteUncertain, wantReads: 6, wantPosts: 1},
		{name: "confirmed transient refusal is retried", visibility: "transient-refusal", want: WriteApplied, wantReads: 3, wantPosts: 2},
		{name: "exhausted transient refusals remain uncertain", visibility: "transient-never", want: WriteUncertain, wantReads: 5, wantPosts: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_ = os.Remove(state)
			_ = os.Remove(posts)
			_ = os.Remove(count)
			_ = os.Remove(sleeps)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TEST_MERGE_POSTED", state)
			t.Setenv("TEST_MERGE_POSTS", posts)
			t.Setenv("TEST_DISCOVERY_COUNT", count)
			t.Setenv("TEST_SLEEP_LOG", sleeps)
			t.Setenv("TEST_MERGE_VISIBILITY", test.visibility)

			ownership := &recordingOwnership{}
			adapter, err := NewAdapter(ScriptRunner{Directory: adaptation, APIBase: "https://forge.invalid", Credential: "secret-token"}, ownership, "Minos")
			if err != nil {
				t.Fatal(err)
			}
			// The production budget is tested separately. These script cases bind
			// retry semantics without making the unit suite wait for wall time.
			adapter.mergeRetryDelays = []time.Duration{0, 0}
			result := adapter.Merge(t.Context(), Guard{
				Ownership: LifecycleOwnership(1), Repository: Repository{Owner: "acme", Name: "widget"},
				PullRequest: 4, HeadSHA: "expected-head", TargetSHA: "expected-target",
			}, MergeMethodSquash)
			if result.Outcome != test.want {
				t.Fatalf("Merge() = %#v, want %q", result, test.want)
			}
			reads, err := os.ReadFile(count)
			if err != nil {
				t.Fatal(err)
			}
			gotReads, err := strconv.Atoi(strings.TrimSpace(string(reads)))
			if err != nil {
				t.Fatal(err)
			}
			if gotReads != test.wantReads {
				t.Fatalf("discovery reads = %d, want %d", gotReads, test.wantReads)
			}
			postData, err := os.ReadFile(posts)
			if err != nil {
				t.Fatal(err)
			}
			gotPosts, err := strconv.Atoi(strings.TrimSpace(string(postData)))
			if err != nil {
				t.Fatal(err)
			}
			if gotPosts != test.wantPosts {
				t.Fatalf("merge POSTs = %d, want %d", gotPosts, test.wantPosts)
			}
			if ownership.calls != test.wantPosts {
				t.Fatalf("ownership checks = %d, want one per POST (%d)", ownership.calls, test.wantPosts)
			}
		})
	}
}
