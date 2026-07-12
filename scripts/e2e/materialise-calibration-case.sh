#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: materialise-calibration-case.sh CASE_ID DESTINATION" >&2
  exit 2
fi

case_id="$1"
destination="$2"
if [[ ! "$case_id" =~ ^[a-z0-9]+(-[a-z0-9]+)*$ ]]; then
  echo "invalid calibration case id: ${case_id}" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
case_root="${root}/verification/calibration/cases/${case_id}"
target_tree="${case_root}/target"
head_tree="${case_root}/head"
if [[ ! -d "$target_tree" || ! -d "$head_tree" ]]; then
  echo "unknown or incomplete calibration case: ${case_id}" >&2
  exit 2
fi

# Refuse an existing path rather than trying to decide which contents are safe
# to replace. The E2E caller owns disposal of successfully materialised repos.
created=false
complete=false
cleanup_incomplete_repository() {
  if [[ "$created" == true && "$complete" != true ]]; then
    rm -rf -- "$destination"
  fi
}
trap cleanup_incomplete_repository EXIT
mkdir -- "$destination"
created=true

copy_tree() {
  local source="$1"
  cp -a "${source}/." "${destination}/"
}

export GIT_AUTHOR_NAME="Minos Calibration"
export GIT_AUTHOR_EMAIL="calibration@minos.invalid"
export GIT_COMMITTER_NAME="$GIT_AUTHOR_NAME"
export GIT_COMMITTER_EMAIL="$GIT_AUTHOR_EMAIL"

git -C "$destination" init -q -b main --template=
# The fixture hashes must not depend on a developer's signing, hook, or line-
# ending configuration. The source trees themselves remain the only input.
git -C "$destination" config commit.gpgSign false
git -C "$destination" config core.autocrlf false
git -C "$destination" config core.hooksPath .git/no-calibration-hooks
copy_tree "$target_tree"
git -C "$destination" add -A
GIT_AUTHOR_DATE="2000-01-01T00:00:00Z" \
  GIT_COMMITTER_DATE="2000-01-01T00:00:00Z" \
  git -C "$destination" commit -q -m "baseline: ${case_id}"
target_sha="$(git -C "$destination" rev-parse HEAD)"

git -C "$destination" switch -q -c change
git -C "$destination" rm -q -r -- .
copy_tree "$head_tree"
git -C "$destination" add -A
GIT_AUTHOR_DATE="2000-01-01T00:00:01Z" \
  GIT_COMMITTER_DATE="2000-01-01T00:00:01Z" \
  git -C "$destination" commit -q -m "change: ${case_id}"
head_sha="$(git -C "$destination" rev-parse HEAD)"

complete=true
printf '%s\t%s\t%s\n' "$case_id" "$target_sha" "$head_sha"
