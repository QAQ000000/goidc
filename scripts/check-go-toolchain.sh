#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
go_cmd="$repo_root/scripts/go.sh"

version="$($go_cmd version)"
goroot="$($go_cmd env GOROOT)"
gotooldir="$($go_cmd env GOTOOLDIR)"

printf 'go=%s\n' "$version"
printf 'GOROOT=%s\n' "$goroot"
printf 'GOTOOLDIR=%s\n' "$gotooldir"

if [[ "$version" != "go version go1.25."* ]]; then
  printf 'unsupported Go version: %s\n' "$version" >&2
  exit 1
fi
