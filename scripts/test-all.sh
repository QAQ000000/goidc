#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
test_database_url="${ZJMFGO_TEST_DATABASE_URL:-}"

if [[ -z "${test_database_url//[[:space:]]/}" ]]; then
  printf 'ZJMFGO_TEST_DATABASE_URL must point to an isolated PostgreSQL 18 database with a name ending in _test.\n' >&2
  exit 1
fi

export REQUIRE_INTEGRATION=1
cd "$repo_root"
# Every integration case migrates an isolated schema. The growing HTTP suite
# exceeds thirty minutes on the acceptance host; keep an explicit package cap.
exec "$repo_root/scripts/go.sh" test -count=1 -timeout=45m ./...
