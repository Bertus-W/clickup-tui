#!/bin/sh
# Builds a release into dist/: cu for macOS, Linux and Windows (amd64 and arm64) as .tar.gz / .zip
# archives, plus checksums.txt. Codeberg's CI runs it for every v* tag; locally:
#
#   packaging/release.sh v0.2.1
set -eu
tag="$1"
version="${tag#v}"
cd "$(dirname "$0")/.."
root="$PWD"
rm -rf dist
mkdir -p dist/build

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}" arch="${target#*/}"
  dir="dist/build/${os}_${arch}"
  exe=cu
  [ "$os" = windows ] && exe=cu.exe
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$tag" -o "$dir/$exe" ./cmd/cu
  cp README.md LICENSE "$dir/"
  name="cu_${version}_${os}_${arch}"
  if [ "$os" = windows ]; then
    (cd "$dir" && zip -q -X "$root/dist/$name.zip" "$exe" README.md LICENSE)
  else
    tar -czf "dist/$name.tar.gz" -C "$dir" "$exe" README.md LICENSE
  fi
done

rm -rf dist/build
(cd dist && sha256sum -- * > checksums.txt)
ls -1 dist
