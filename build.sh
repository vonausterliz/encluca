#!/usr/bin/env bash
# Compila Encluca per tutti i sistemi e crea i pacchetti in dist/.
set -euo pipefail
cd "$(dirname "$0")"
VERSION="$(grep -oP 'Version = "\K[^"]+' main.go)"
rm -rf dist && mkdir -p dist

build() { # os arch packaging-dir
  local os=$1 arch=$2 pkg=$3 ext="" ldflags="-s -w"
  [ "$os" = windows ] && ext=".exe" && ldflags="$ldflags -H=windowsgui"
  local name="encluca-$VERSION-$os-$arch" out="dist/encluca-$VERSION-$os-$arch"
  mkdir -p "$out"
  echo "→ $name"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$out/encluca$ext" .
  cp packaging/$pkg/* "$out/"
  cp LEGGIMI.md README.md "$out/"
  [ "$os" = linux ] && cp docs/INSTALLA-TRUENAS.md "$out/"
  if [ "$os" = windows ]; then
    (cd dist && python3 -c "import shutil,sys; shutil.make_archive(sys.argv[1], 'zip', '.', sys.argv[1])" "$name")
  else
    tar -C dist -czf "dist/$name.tar.gz" "$name"
  fi
}

build linux   amd64 linux
build linux   arm64 linux
build darwin  arm64 macos
build darwin  amd64 macos
build windows amd64 windows
# il pacchetto per TrueNAS è il binario linux-amd64 + il compose
cp packaging/truenas/docker-compose.yaml dist/encluca-$VERSION-linux-amd64/
tar -C dist -czf "dist/encluca-$VERSION-linux-amd64.tar.gz" "encluca-$VERSION-linux-amd64"
ls -lh dist/*.tar.gz dist/*.zip
