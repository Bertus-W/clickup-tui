#!/bin/sh
# Builds cu_<version>_macos.pkg: one universal cu (Intel and Apple Silicon) in /usr/local/bin.
# Runs on Linux and macOS alike: fatbin and mkpkg stand in for Apple's lipo and pkgbuild.
#
#   packaging/macos/build-pkg.sh 0.1.0 path/to/darwin_amd64/cu path/to/darwin_arm64/cu [outdir]
#
# The package is unsigned: without an Apple Developer ID, macOS asks for confirmation the
# first time (right-click → Open, or System Settings → Privacy & Security → Open Anyway).
set -eu
version="$1" amd64="$2" arm64="$3" out="${4:-.}"
here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/root/usr/local/bin" "$out"
go run "$here/fatbin" -o "$work/root/usr/local/bin/cu" "$amd64" "$arm64"
chmod 0755 "$work/root/usr/local/bin/cu"
go run "$here/mkpkg" -root "$work/root" -id org.codeberg.b-wisman.clickup-tui -version "$version" \
  -o "$out/cu_${version}_macos.pkg"
echo "$out/cu_${version}_macos.pkg"
