#!/usr/bin/env bash
# No PostgreSQL, Go build, or real test suite is used by this regression.
set -euo pipefail
# Keep fixture Git operations independent of the invoking shell or worktree.
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES
script_dir="$(cd "$(dirname "$0")" && pwd -P)"
fixture_root="$(mktemp -d "${TMPDIR:-/tmp}/zjmfgo-test-all-regression.XXXXXXXX")"
fixture_pid=""
cleanup() {
  if [[ -n "$fixture_pid" ]] && kill -0 "$fixture_pid" 2>/dev/null; then
    kill "$fixture_pid" 2>/dev/null || true
    wait "$fixture_pid" 2>/dev/null || true
  fi
  rm -rf -- "$fixture_root"
}
trap cleanup EXIT
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
expect_exit() {
  local expected="$1" output="$2" result
  shift 2
  if "$@" >"$output" 2>&1; then result=0; else result=$?; fi
  [[ "$result" == "$expected" ]] || fail "expected exit $expected, got $result (see $output)"
}
wait_ready() {
  local ready="$1" attempt
  for ((attempt=0;attempt<100;attempt++)); do
    [[ -f "$ready" ]] && return
    kill -0 "$fixture_pid" 2>/dev/null || fail 'first fixture process exited before readiness'
    sleep 0.03
  done
  fail 'fixture driver did not become ready'
}
make_source() {
  local target="$1"
  mkdir -p "$target/scripts"
  cp "$script_dir/test-all.sh" "$target/scripts/test-all.sh"
  cat >"$target/scripts/go.sh" <<'DRIVER'
#!/usr/bin/env bash
set -euo pipefail
[[ "${REQUIRE_INTEGRATION:-}" == 1 ]] || exit 91
[[ "$*" == 'test -count=1 -timeout=45m ./...' ]] || exit 92
printf 'driver\n' >>"$STUB_CALLS"
printf 'ready\n' >"$STUB_READY"
while [[ ! -f "$STUB_RELEASE" ]]; do sleep 0.03; done
exit "${STUB_EXIT:-0}"
DRIVER
  chmod +x "$target/scripts/test-all.sh" "$target/scripts/go.sh"
}
export ZJMFGO_TEST_DATABASE_URL='stub-only-never-a-database-url'
export STUB_CALLS="$fixture_root/calls"
export STUB_READY="$fixture_root/ready"
export STUB_RELEASE="$fixture_root/release"
export TMPDIR="$fixture_root"
repo="$fixture_root/repo"
make_source "$repo"
git -C "$repo" init -q
git -C "$repo" -c user.name=Fixture -c user.email=fixture@example.invalid add scripts
git -C "$repo" -c user.name=Fixture -c user.email=fixture@example.invalid commit -qm fixture
git -C "$repo" worktree add -q -b sibling "$fixture_root/sibling"
"$repo/scripts/test-all.sh" >"$fixture_root/first.log" 2>&1 &
fixture_pid=$!
wait_ready "$STUB_READY"
expect_exit 75 "$fixture_root/same.log" "$repo/scripts/test-all.sh"
expect_exit 75 "$fixture_root/sibling.log" "$fixture_root/sibling/scripts/test-all.sh"
[[ "$(wc -l <"$STUB_CALLS")" == 1 ]] || fail 'contending process invoked the driver'
rg -q 'Another test-all run.*linked worktrees' "$fixture_root/sibling.log" || fail 'conflict explanation missing'
# A failed preflight must never even reach the already locked driver.
expect_exit 1 "$fixture_root/no-dsn.log" env -u ZJMFGO_TEST_DATABASE_URL "$repo/scripts/test-all.sh"
expect_exit 1 "$fixture_root/blank-dsn.log" env ZJMFGO_TEST_DATABASE_URL="  " "$repo/scripts/test-all.sh"
[[ "$(wc -l <"$STUB_CALLS")" == 1 ]] || fail 'missing DSN invoked driver'
touch "$STUB_RELEASE"
wait "$fixture_pid" || fail 'first driver status changed'
fixture_pid=""
expect_exit 23 "$fixture_root/exit.log" env STUB_EXIT=23 "$fixture_root/sibling/scripts/test-all.sh"
expect_exit 0 "$fixture_root/retry.log" "$repo/scripts/test-all.sh"
[[ "$(wc -l <"$STUB_CALLS")" == 3 ]] || fail 'lock not reusable after driver exit'
# A source archive uses the same private, stable key across real processes.
archive="$fixture_root/archive"
make_source "$archive"
rm -f "$STUB_READY" "$STUB_RELEASE"
"$archive/scripts/test-all.sh" >"$fixture_root/archive-first.log" 2>&1 &
fixture_pid=$!
wait_ready "$STUB_READY"
expect_exit 75 "$fixture_root/archive-second.log" "$archive/scripts/test-all.sh"
touch "$STUB_RELEASE"
wait "$fixture_pid" || fail 'source archive first driver failed'
fixture_pid=""
expect_exit 0 "$fixture_root/archive-retry.log" "$archive/scripts/test-all.sh"
[[ ! -e "$archive/.git" && ! -e "$archive/zjmfgo-test-all.lock" ]] || fail 'fallback polluted source directory'
# A shared or symlinked archive lock namespace must fail closed.
archive_lock_dir="$fixture_root/zjmfgo-test-all-$UID"
calls_before="$(wc -l <"$STUB_CALLS")"
chmod 755 "$archive_lock_dir"
expect_exit 1 "$fixture_root/archive-permissions.log" "$archive/scripts/test-all.sh"
chmod 700 "$archive_lock_dir"
mv "$archive_lock_dir" "$archive_lock_dir.saved"
ln -s "$archive_lock_dir.saved" "$archive_lock_dir"
expect_exit 1 "$fixture_root/archive-symlink.log" "$archive/scripts/test-all.sh"
rm "$archive_lock_dir"
mv "$archive_lock_dir.saved" "$archive_lock_dir"
[[ "$(wc -l <"$STUB_CALLS")" == "$calls_before" ]] || fail 'unsafe archive lock namespace invoked driver'
# Deliberately restricted PATH provides script startup tools but no flock.
missing_bin="$fixture_root/missing-flock"
mkdir "$missing_bin"
for tool in bash dirname; do ln -s "$(command -v "$tool")" "$missing_bin/$tool"; done
calls_before="$(wc -l <"$STUB_CALLS")"
expect_exit 1 "$fixture_root/missing-flock.log" env PATH="$missing_bin" "$archive/scripts/test-all.sh"
rg -q 'requires flock' "$fixture_root/missing-flock.log" || fail 'missing flock diagnostic absent'
[[ "$(wc -l <"$STUB_CALLS")" == "$calls_before" ]] || fail 'missing flock invoked driver'
if rg -q 'stub-only-never-a-database-url' "$fixture_root/"*.log; then fail 'DSN leaked into diagnostics'; fi
git -C "$repo" diff --exit-code --quiet || fail 'lock changed tracked source'
[[ "$(git -C "$repo" status --porcelain)" == '' ]] || fail 'lock polluted worktree'
printf 'PASS: real-process contention, linked worktrees, archive fallback, lock lifetime/reuse, driver exit status, missing DSN/flock, and private diagnostics (stub driver; no PostgreSQL/Go).\n'
