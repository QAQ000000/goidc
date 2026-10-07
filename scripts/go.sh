#!/usr/bin/env bash
set -euo pipefail

# Keep repository commands on the server's Go toolchain. A user-local Go with
# a different GOROOT can produce compile version mismatch errors.
preferred="/usr/local/btgo/bin/go"
if [[ -x "$preferred" ]]; then
  unset GOROOT
  exec "$preferred" "$@"
fi

exec go "$@"
