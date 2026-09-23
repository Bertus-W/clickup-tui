#!/bin/sh
# Builds everything a release ships into dist/, on Linux or macOS:
#
#   packaging/release.sh v0.2.0
#
# - cu for macOS, Linux and Windows on amd64 and arm64, as .tar.gz / .zip archives
# - Linux packages: .deb, .rpm, .apk and Arch (needs nfpm)
# - the macOS installer, cu_<version>_macos.pkg (plain Go, see packaging/macos)
# - the Windows installer, cu_<version>_windows_setup.exe (needs makensis)
# - checksums.txt over all of them
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
  echo "build $os/$arch"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$tag" -o "$dir/$exe" ./cmd/cu
  cp README.md LICENSE "$dir/"
  name="cu_${version}_${os}_${arch}"
  if [ "$os" = windows ]; then
    (cd "$dir" && zip -q -X "$root/dist/$name.zip" "$exe" README.md LICENSE)
  else
    tar -czf "dist/$name.tar.gz" -C "$dir" "$exe" README.md LICENSE
  fi
done

mkdir -p dist/linux-pkg
for arch in amd64 arm64; do
  cp "dist/build/linux_$arch/cu" dist/linux-pkg/cu
  for format in deb rpm apk archlinux; do
    CU_VERSION="$version" CU_ARCH="$arch" \
      nfpm package --config packaging/linux/nfpm.yaml --packager "$format" --target dist/ >/dev/null
  done
done
rm -rf dist/linux-pkg
echo "packaged Linux: deb rpm apk archlinux"

packaging/macos/build-pkg.sh "$version" dist/build/darwin_amd64/cu dist/build/darwin_arm64/cu dist >/dev/null
echo "packaged macOS: cu_${version}_macos.pkg"

makensis -V2 -DVERSION="$version" -DAMD64="$root/dist/build/windows_amd64/cu.exe" \
  -DARM64="$root/dist/build/windows_arm64/cu.exe" -DOUTFILE="$root/dist/cu_${version}_windows_setup.exe" \
  packaging/windows/cu.nsi
echo "packaged Windows: cu_${version}_windows_setup.exe"

rm -rf dist/build
(cd dist && sha256sum -- * > checksums.txt)
ls -1 dist
