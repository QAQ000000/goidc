#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
test_database_url="${ZJMFGO_TEST_DATABASE_URL:-}"
if [[ -z "${test_database_url//[[:space:]]/}" ]]; then
  printf 'ZJMFGO_TEST_DATABASE_URL must point to an isolated loopback PostgreSQL 18 database ending in _test.\n' >&2
  exit 1
fi

export REQUIRE_INTEGRATION=1
export ZJMFGO_WORKER_CRASH_ACCEPTANCE=1
cd "$repo_root"
recovery_log="$(mktemp "${TMPDIR:-/tmp}/zjmfgo-worker-recovery.XXXXXXXX.log")"
trap 'rm -f -- "$recovery_log"' EXIT

# These opt-in tests build the real worker, SIGKILL it during loopback I/O,
# and observe recovery by replacement workers in per-test random schemas.
# The race detector covers the test harness; child workers use normal builds.
"$repo_root/scripts/go.sh" test -race -count=1 -v -timeout=10m \
  ./internal/store ./internal/httpapi \
  -run '^Test(Notification|Supplier)WorkerSIGKILLRecovery$' | tee "$recovery_log"

# Go exits successfully when -run matches no tests. Require both suites to
# actually pass so a rename, removal or accidental skip cannot pass this gate.
for suite in Notification Supplier; do
  if ! rg -q -- "^--- PASS: Test${suite}WorkerSIGKILLRecovery \\(" "$recovery_log"; then
    printf 'Missing executed PASS for Test%sWorkerSIGKILLRecovery.\n' "$suite" >&2
    exit 1
  fi
done
