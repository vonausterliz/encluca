#!/usr/bin/env bash
# Compila Encluca per tutti i sistemi e crea i pacchetti in dist/.
set -euo pipefail
cd "$(dirname "$0")"
VERSION="$(grep -oP 'Version = "\K[^"]+' main.go)"
SIGNING_KEY="${ENCLUCA_SIGNING_KEY:-$HOME/.config/encluca-release/signing.key}"
if [ ! -f "$SIGNING_KEY" ]; then
  echo "errore: chiave di firma non trovata in $SIGNING_KEY: i rilasci devono essere firmati" >&2
  exit 1
fi
rm -rf dist && mkdir -p dist

# modulo WebAssembly per sfogliare la cassaforte dalla pagina del NAS (incluso nel binario)
GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w" -o web/vault.wasm ./cmd/vaultwasm
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/wasm_exec.js

build() { # os arch packaging-dir
  local os=$1 arch=$2 pkg=$3 ext="" ldflags="-s -w"
  [ "$os" = windows ] && ext=".exe" && ldflags="$ldflags -H=windowsgui"
  local name="encluca-$VERSION-$os-$arch" out="dist/encluca-$VERSION-$os-$arch"
  mkdir -p "$out"
  echo "→ $name"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$out/encluca$ext" .
  cp packaging/$pkg/* "$out/"
  cp LEGGIMI.md README.md "$out/"
  case $os in # logo per i collegamenti
    linux) cp assets/logo.svg "$out/encluca.svg"; cp assets/encluca-256.png "$out/encluca.png" ;;
    darwin) cp assets/encluca-512.png "$out/encluca.png" ;;
    windows) cp assets/encluca.ico "$out/encluca.ico" ;;
  esac
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
# impronte firmate: il client accetta un aggiornamento solo se la firma è valida
(cd dist && sha256sum *.tar.gz *.zip > SHA256SUMS)
ENCLUCA_SIGNING_KEY="$SIGNING_KEY" go run ./cmd/sign sign dist/SHA256SUMS
ls -lh dist/*.tar.gz dist/*.zip dist/SHA256SUMS*
