#!/bin/sh
# Prepare the archive consumed by -tags bundled_tmux. Docker builds the native
# Linux CPU by default; a release runner passes its explicit target architecture.
set -eu
arch=${1:-$(go env GOARCH)}
case "$arch" in amd64|arm64) ;; *) echo "unsupported Linux CPU: $arch" >&2; exit 1;; esac
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT HUP INT TERM
docker buildx build --platform "linux/$arch" --file "$root/scripts/tmux-linux.Dockerfile" \
  --target export --output "type=local,dest=$out" "$root"
cp "$out/tmux.tar.gz" "$root/internal/tmuxruntime/tmux.tar.gz"
