#!/bin/sh
# Installs stui into ~/.local/bin.
#
#   curl -fsSL https://dl.sophonz.io/stui/install.sh | sh
#
# Set STUI_INSTALL_DIR to put it somewhere else, STUI_VERSION to pin a
# release (v0.1.0), or STUI_BASE_URL to download from a mirror.
set -eu

BASE="${STUI_BASE_URL:-https://dl.sophonz.io/stui}"
DIR="${STUI_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${STUI_VERSION:-latest}"

die() { printf '\033[31merror\033[0m  %s\n' "$*" >&2; exit 1; }
say() { printf '  %s\n' "$*"; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  *)      die "stui has no build for $(uname -s)" ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64)  arch=amd64 ;;
  *)             die "stui has no build for $(uname -m)" ;;
esac
asset="stui-$os-$arch.tar.gz"

# `latest` is overwritten in place: read which release it is, then download
# from that release's own directory so the archive and its checksum match.
if [ "$VERSION" = latest ]; then
  VERSION=$(curl -fsSL "$BASE/latest/version.txt") || die "could not read $BASE/latest/version.txt"
fi
url="$BASE/$VERSION"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
say "downloading stui $VERSION ($os/$arch)"
curl -fsSL -o "$tmp/$asset" "$url/$asset" || die "could not download $url/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$url/checksums.txt" || die "could not download $url/checksums.txt"

want=$(awk -v a="$asset" '$2 == a {print $1}' "$tmp/checksums.txt")
[ -n "$want" ] || die "$asset is not in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$asset" | awk '{print $1}')
else
  got=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
fi
[ "$want" = "$got" ] || die "checksum mismatch for $asset"

tar -C "$tmp" -xzf "$tmp/$asset" stui
mkdir -p "$DIR"
mv "$tmp/stui" "$DIR/stui"
chmod +x "$DIR/stui"
say "installed $("$DIR/stui" --version) to $DIR/stui"

command -v tmux >/dev/null 2>&1 || say "stui needs tmux on PATH (brew install tmux, apt install tmux)"
case ":$PATH:" in
  *":$DIR:"*) ;;
  *) say "$DIR is not on your PATH; add it, e.g. echo 'export PATH=\"$DIR:\$PATH\"' >> ~/.profile" ;;
esac
