#!/bin/sh
# Builds cu_<version>_macos.pkg: one universal cu (Intel and Apple Silicon) in /usr/local/bin.
#
#   packaging/macos/build-pkg.sh 0.1.0 path/to/darwin_amd64/cu path/to/darwin_arm64/cu [outdir]
#
# The package is unsigned: without an Apple Developer ID, macOS asks for confirmation the
# first time (right-click → Open, or System Settings → Privacy & Security → Open Anyway).
set -eu
version="$1" amd64="$2" arm64="$3" out="${4:-.}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/root/usr/local/bin"
lipo -create -output "$work/root/usr/local/bin/cu" "$amd64" "$arm64"
chmod 0755 "$work/root/usr/local/bin/cu"
mkdir -p "$out"
xattr -cr "$work/root" # extended attributes would add ._ files to the package
pkgbuild --quiet --root "$work/root" --install-location / \
  --identifier org.codeberg.b-wisman.clickup-tui --version "$version" \
  "$out/cu_${version}_macos.pkg"
if pkgutil --payload-files "$out/cu_${version}_macos.pkg" | grep -q '/\._'; then
  echo "build-pkg.sh: ._ files ended up in the package" >&2
  exit 1
fi
echo "$out/cu_${version}_macos.pkg"
