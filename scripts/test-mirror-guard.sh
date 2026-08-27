#!/usr/bin/env bash
# Regression test for the "never rewind the GitHub mirror" guard in
# .gitea/workflows/goreleaser.yml.
#
# Why this exists: the guard's SKIP path only fires when someone reruns an OLDER
# tag's release run. Normal CI never takes it, so nothing else would notice if a
# later edit broke it -- and the failure is silent (public main quietly rewinds).
set -euo pipefail

# --- Containment -------------------------------------------------------------
# git exports GIT_DIR and friends into hook processes, and GIT_DIR BEATS cwd
# discovery. This script runs as a pre-commit hook, so without clearing them the
# fixture `git init` / `git config` / `git commit` calls below all retarget the
# CALLER's repository -- no cd can prevent it. That is not hypothetical: it once
# appended fixture commits to a live branch and rewrote core.bare, core.hooksPath
# and user.* in the real repo's config.
#
# GIT_CONFIG_GLOBAL is deliberately NOT cleared: CI sets it to carry the private
# -module insteadOf rewrite, and dropping it would fall back to ~/.gitconfig.
GIT_LEAK_VARS="GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_PREFIX GIT_COMMON_DIR
GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_NAMESPACE
GIT_CEILING_DIRECTORIES GIT_TEMPLATE_DIR GIT_CONFIG_COUNT"
# shellcheck disable=SC2086
unset $GIT_LEAK_VARS

# Verify the unset took, BEFORE the first git command. Checked by NAME, because
# git cannot be trusted to report the leak: with GIT_DIR set and GIT_WORK_TREE
# unset, `rev-parse --show-toplevel` reports cwd -- so a path-containment probe
# passes under precisely the vector it is meant to catch (measured, git 2.55).
for v in $GIT_LEAK_VARS; do
  eval "leaked=\${$v+set}"
  [ -z "${leaked:-}" ] || { echo "REFUSING: $v is still set after unset"; exit 1; }
done

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
workflow="${here}/../.gitea/workflows/goreleaser.yml"
fail=0

# --- Drift: the tested copy must still match the workflow ---------------------
# The guard is duplicated below because it lives inside a YAML `run:` block and
# cannot be sourced. Compare the WHOLE if/elif/else block, not individual lines:
# a line-by-line grep passes a workflow whose branch bodies were SWAPPED, since
# every line is still present -- and a swapped guard force-pushes precisely when
# it must not, i.e. this bug inverted. (Demonstrated: the earlier needle-based
# check stayed green through exactly that mutation.)
expected_block() {
  cat <<'BLOCK'
          if [ -z "$remote_main" ] || [ "$remote_main" = "$head_sha" ] \
             || git merge-base --is-ancestor "$remote_main" "$head_sha"; then
            git push github "HEAD:refs/heads/main"
          elif git merge-base --is-ancestor "$head_sha" "$remote_main"; then
            echo "::notice::${TAG} is behind the GitHub mirror's main (${remote_main}); leaving main alone. The tag below is what the registry ingests."
          else
            echo "::error::${TAG} has diverged from the GitHub mirror's main (${remote_main}); refusing to rewrite it."
            exit 1
          fi
BLOCK
}
# Strip comment-only lines so prose edits don't red the build; everything that
# executes is compared verbatim, including branch order.
actual_block="$(awk '/^          if \[ -z "\$remote_main" \]/,/^          fi$/' "$workflow" | grep -v '^ *#')"
if [ "$actual_block" != "$(expected_block)" ]; then
  echo "DRIFT: the guard block in the workflow no longer matches the copy tested here."
  echo "--- workflow ---"; printf '%s\n' "$actual_block"
  echo "--- tested ---";   expected_block
  fail=1
fi
for needle in \
  'git fetch -q --no-tags github "+refs/heads/main:refs/remotes/github/main"' \
  'remote_main="$(git rev-parse -q --verify refs/remotes/github/main || true)"'
do
  grep -qF -- "$needle" "$workflow" || { echo "DRIFT: missing: $needle"; fail=1; }
done

# --- The guard under test, verbatim ------------------------------------------
# Mirrors the block above; echoes which branch was taken.
guard() {
  local remote="$1"
  local remote_main head_sha
  git fetch -q --no-tags "$remote" "+refs/heads/main:refs/remotes/github/main" 2>/dev/null || true
  remote_main="$(git rev-parse -q --verify refs/remotes/github/main || true)"
  head_sha="$(git rev-parse HEAD)"
  if [ -z "$remote_main" ] || [ "$remote_main" = "$head_sha" ] \
     || git merge-base --is-ancestor "$remote_main" "$head_sha"; then
    echo PUSH
  elif git merge-base --is-ancestor "$head_sha" "$remote_main"; then
    echo SKIP
  else
    echo ERROR
  fi
}

check() { # check <label> <expected> <actual>
  if [ "$2" = "$3" ]; then echo "  ok   $1 -> $3"
  else echo "  FAIL $1 -> got $3, want $2"; fail=1; fi
}

# pwd -P: macOS mktemp hands back /var/... while git reports /private/var/...
tmp="$(cd "$(mktemp -d)" && pwd -P)"
trap 'rm -rf "$tmp"' EXIT

newrepo() { # newrepo <path> [--bare]
  git init -q ${2:-} "$1"
  # Second net behind the env check: --absolute-git-dir names where writes land
  # (unlike --show-toplevel). If this ever points outside $tmp, stop immediately.
  local gd
  gd="$(git -C "$1" rev-parse --absolute-git-dir)"
  case "$gd" in
    "$tmp"/*) : ;;
    *) echo "REFUSING: fixture git dir is $gd, outside $tmp"; exit 1 ;;
  esac
  git -C "$1" config core.hooksPath /dev/null
  # Signing would prompt, or fail, in a hook context and block every commit here.
  git -C "$1" config commit.gpgsign false
  if [ "${2:-}" != "--bare" ]; then
    git -C "$1" config user.email t@example.com
    git -C "$1" config user.name t
  fi
}

newrepo "$tmp/mirror.git" --bare
newrepo "$tmp/work"
cd "$tmp/work" || { echo "cannot enter fixture repo"; exit 1; }

# Three commits A<-B<-C; the mirror is pushed at C. The guard reads no tags, so
# plain commits suffice -- a checkout of A stands in for "rerun of an older tag".
for m in A B C; do echo "$m" > f; git add f; git commit -qm "$m"; done
old_commit="$(git rev-parse HEAD~2)"
tip_commit="$(git rev-parse HEAD)"
git push -q "$tmp/mirror.git" HEAD:refs/heads/main

reset_ref() { git update-ref -d refs/remotes/github/main 2>/dev/null || true; }

echo "mirror-guard:"
git checkout -q "$old_commit"
reset_ref; check "rerun an older tag (the bug)"        SKIP "$(guard "$tmp/mirror.git")"
git checkout -q "$tip_commit"
reset_ref; check "rerun the current mirror tip"        PUSH "$(guard "$tmp/mirror.git")"
git checkout -q -B main "$tip_commit"
echo D > f; git add f; git commit -qm D
reset_ref; check "a new release, ahead of the mirror"  PUSH "$(guard "$tmp/mirror.git")"
newrepo "$tmp/empty.git" --bare
reset_ref; check "mirror empty (first ever push)"      PUSH "$(guard "$tmp/empty.git")"
newrepo "$tmp/div.git" --bare
newrepo "$tmp/other"
( cd "$tmp/other" && echo X > g && git add g && git commit -qm X \
  && git push -q "$tmp/div.git" HEAD:refs/heads/main )
reset_ref; check "mirror tip unknown to us (diverged)" ERROR "$(guard "$tmp/div.git")"

# Every arm is load-bearing. Note the THIRD case is what kills a guard missing
# the ancestor arm: a rerun of an older tag SKIPs either way, so testing only the
# bug would pass a guard that silently stops mirroring every future release.
if [ "$fail" -eq 0 ]; then
  echo "mirror-guard: all cases pass"
else
  echo "mirror-guard: FAILED"
  exit 1
fi
