#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd -P)"
test_database_url="${ZJMFGO_TEST_DATABASE_URL:-}"

if [[ -z "${test_database_url//[[:space:]]/}" ]]; then
  printf 'ZJMFGO_TEST_DATABASE_URL must point to an isolated PostgreSQL 18 database with a name ending in _test.\n' >&2
  exit 1
fi
if ! command -v flock >/dev/null 2>&1; then
  printf 'test-all requires flock for exclusive execution; no tests started.\n' >&2
  exit 1
fi

# All linked worktrees share the common Git directory. Keep this lock outside
# the working tree. This protects this script, not direct go test invocations
# or other clones using the same database.
if git_common_dir="$(git -C "$repo_root" rev-parse --git-common-dir 2>/dev/null)"; then
  if [[ "$git_common_dir" != /* ]]; then
    git_common_dir="$repo_root/$git_common_dir"
  fi
  lock_dir="$(cd "$git_common_dir" && pwd -P)"
  lock_path="$lock_dir/zjmfgo-test-all.lock"
else
  # Source archives have no shared Git directory. Use a stable per-source-path
  # key inside an owned private temporary directory; never write into sources.
  if ! command -v sha256sum >/dev/null 2>&1; then
    printf 'test-all requires sha256sum for source-archive exclusion; no tests started.\n' >&2
    exit 1
  fi
  lock_dir="${TMPDIR:-/tmp}/zjmfgo-test-all-$UID"
  if [[ ! -e "$lock_dir" && ! -L "$lock_dir" ]]; then
    (umask 077; mkdir -m 700 -- "$lock_dir") 2>/dev/null || true
  fi
  if [[ ! -d "$lock_dir" || -L "$lock_dir" || ! -O "$lock_dir" ]]; then
    printf 'test-all cannot use its private source-archive lock directory; no tests started.\n' >&2
    exit 1
  fi
  # An old directory with broader permissions is not a safe lock namespace.
  if [[ "$(stat -c '%a' -- "$lock_dir")" != 700 ]]; then
    printf 'test-all source-archive lock directory must have mode 700; no tests started.\n' >&2
    exit 1
  fi
  source_key="$(printf '%s' "$repo_root" | sha256sum)"
  lock_path="$lock_dir/${source_key%% *}.lock"
fi

# The open description is inherited across exec into the driver and Go. It
# holds the lock until that real process exits, including on test failures.
shopt -u varredir_close 2>/dev/null || true
exec {test_lock_fd}>"$lock_path"
if flock -n -E 75 "$test_lock_fd"; then
  :
else
  lock_result=$?
  if [[ "$lock_result" == 75 ]]; then
    printf 'Another test-all run holds this source checkout lock (including linked worktrees); no tests started. Retry after it exits.\n' >&2
  else
    printf 'test-all could not acquire its execution lock; no tests started.\n' >&2
  fi
  exit "$lock_result"
fi

export REQUIRE_INTEGRATION=1
cd "$repo_root"
# Every integration case migrates an isolated schema. The growing HTTP suite
# exceeds thirty minutes on the acceptance host; keep an explicit package cap.
exec "$repo_root/scripts/go.sh" test -count=1 -timeout=45m ./...
