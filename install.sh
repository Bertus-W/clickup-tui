#!/bin/sh
# Installs cu, the ClickUp terminal UI, from the latest Codeberg release (macOS and Linux):
#
#   curl -fsSL https://codeberg.org/b-wisman/clickup-tui/raw/branch/main/install.sh | sh
#
# CU_VERSION=v0.1.0 picks a release; CU_INSTALL_DIR picks where cu goes (default: /usr/local/bin
# when writable, else ~/.local/bin); CU_DOWNLOAD_URL serves the release files from elsewhere.
# The download is checked against the release's checksums.
set -eu

repo="b-wisman/clickup-tui"

fail() { echo "install.sh: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "needs $1"; }
need curl
need tar

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "unsupported system $(uname -s); on Windows use install.ps1" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported CPU $(uname -m)" ;;
esac

tag="${CU_VERSION:-}"
if [ -z "$tag" ]; then
  tag="$(curl -fsSL "https://codeberg.org/api/v1/repos/$repo/releases/latest" 2>/dev/null |
    sed -n 's/.*"tag_name":"\([^"]*\)".*/\1/p')" || true
  case "$tag" in v*) ;; *) fail "couldn't find the latest release" ;; esac
fi

archive="cu_${tag#v}_${os}_${arch}.tar.gz"
base="${CU_DOWNLOAD_URL:-https://codeberg.org/$repo/releases/download/$tag}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading cu ${tag} for ${os}/${arch}..."
curl -fsSL -o "$tmp/$archive" "$base/$archive" || fail "no $archive in release $tag"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "no checksums in release $tag"

want="$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "$tmp/$archive" | cut -d' ' -f1)"
else
  got="$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)"
fi
[ -n "$want" ] && [ "$want" = "$got" ] || fail "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" cu

dir="${CU_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir"
install -m 0755 "$tmp/cu" "$dir/cu"
echo "Installed $("$dir/cu" version) to $dir/cu"

case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Note: $dir is not on your PATH; add it, e.g. export PATH=\"$dir:\$PATH\"" ;;
esac
echo "Run cu to start: the first time it asks for your ClickUp API token."
