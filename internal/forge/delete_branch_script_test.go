package forge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteBranchDistinguishesConfirmedAbsenceFromUnreadableForge(t *testing.T) {
	adaptation := t.TempDir()
	copyExecutable(t, filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "common.sh"), filepath.Join(adaptation, "common.sh"))
	copyExecutable(t, filepath.Join("..", "..", "scripts", "adaptations", "forgejo", "delete-branch"), filepath.Join(adaptation, "delete-branch"))

	bin := t.TempDir()
	gitCalled := filepath.Join(t.TempDir(), "git-called")
	writeExecutable(t, filepath.Join(bin, "git"), `#!/usr/bin/env sh
set -eu
printf '%s\n' "$*" >>"$TEST_GIT_CALLED"
case " $* " in
  *" push "*) exit 1 ;;
  *" ls-remote "*)
    if [ "$TEST_REMOTE_READ" = "failed" ]; then exit 8; fi
    if [ "$TEST_REMOTE_READ" = "same" ]; then printf '%s\t%s\n' expected-head refs/heads/feature; fi
    exit 0
    ;;
esac
exit 99
`)
	writeExecutable(t, filepath.Join(bin, "curl"), `#!/usr/bin/env sh
set -eu
url=""
output=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  */api/v1/user) printf '%s\n' '{"login":"Minos"}' ;;
  */api/v1/repos/acme/widget) printf '%s\n' '{"default_branch":"main","clone_url":"https://forge.invalid/acme/widget.git"}' ;;
  */pulls/4) printf '%s\n' '{"number":4,"merged":true,"head":{"sha":"expected-head","ref":"feature","repo":{"full_name":"acme/widget"}}}' ;;
  */branches/feature)
    if [ "$TEST_BRANCH_READ" = "transport" ]; then exit 7; fi
    if [ "$TEST_BRANCH_READ" = "absent" ]; then
      : >"$output"
      printf '%s' '404'
    else
      printf '%s\n' '{"protected":false,"commit":{"id":"expected-head"}}' >"$output"
      printf '%s' '200'
    fi
    ;;
  *) echo "unexpected curl URL: $url" >&2; exit 2 ;;
esac
`)

	tests := []struct {
		name       string
		branchRead string
		remoteRead string
		want       WriteOutcome
		wantReason string
		wantGit    bool
	}{
		{name: "confirmed API 404 is already deleted", branchRead: "absent", want: WriteApplied},
		{name: "API transport failure remains uncertain", branchRead: "transport", want: WriteUncertain, wantReason: "source branch could not be read"},
		{name: "failed remote read remains uncertain", branchRead: "present", remoteRead: "failed", want: WriteUncertain, wantReason: "branch deletion outcome could not be read", wantGit: true},
		{name: "failed guarded delete with unchanged remote remains retryable", branchRead: "present", remoteRead: "same", want: WriteRetryable, wantReason: "branch deletion should be retried", wantGit: true},
		{name: "successful empty remote read confirms deletion", branchRead: "present", remoteRead: "absent", want: WriteApplied, wantGit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_ = os.Remove(gitCalled)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TEST_BRANCH_READ", test.branchRead)
			t.Setenv("TEST_REMOTE_READ", test.remoteRead)
			t.Setenv("TEST_GIT_CALLED", gitCalled)
			adapter, err := NewAdapter(ScriptRunner{Directory: adaptation, APIBase: "https://forge.invalid", Credential: "secret-token"}, conformanceOwnership{}, "Minos")
			if err != nil {
				t.Fatal(err)
			}
			result := adapter.DeleteMergedBranch(t.Context(), Guard{
				Ownership: LifecycleOwnership(1), Repository: Repository{Owner: "acme", Name: "widget"},
				PullRequest: 4, HeadSHA: "expected-head", TargetSHA: "expected-target",
			})
			if result.Outcome != test.want {
				t.Fatalf("DeleteMergedBranch() = %#v, want %q", result, test.want)
			}
			if result.Reason != test.wantReason {
				t.Fatalf("DeleteMergedBranch() reason = %q, want %q", result.Reason, test.wantReason)
			}
			_, statErr := os.Stat(gitCalled)
			if test.wantGit && statErr != nil {
				t.Fatalf("expected guarded deletion Git calls: %v", statErr)
			}
			if !test.wantGit && !os.IsNotExist(statErr) {
				t.Fatalf("branch absence/read failure unexpectedly reached git: %v", statErr)
			}
		})
	}
}
