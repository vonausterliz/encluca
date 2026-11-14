#!/usr/bin/env bash
# Encluca - installazione su macOS come app (Encluca.app) con avvio automatico (LaunchAgent).
#   ./install.sh            installa (o aggiorna) e avvia
#   ./install.sh uninstall  rimuove (i dati cifrati restano in ~/Library/Application Support/Encluca)
#
# L'app va in /Applications (o in ~/Applications se /Applications non è scrivibile).
# Il servizio gira in background da Encluca.app/Contents/MacOS/encluca; il doppio clic
# sull'app apre l'interfaccia http://127.0.0.1:8484.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
APPDIR="$HOME/Library/Application Support/Encluca"
PLIST="$HOME/Library/LaunchAgents/it.encluca.client.plist"
LABEL="it.encluca.client"
UID_NUM="$(id -u)"

if [ -w /Applications ]; then APPS=/Applications; else APPS="$HOME/Applications"; fi
APP="$APPS/Encluca.app"
BIN="$APP/Contents/MacOS/encluca"

# resti delle versioni ≤ 0.2.0 (programma in Application Support, collegamenti .webloc)
remove_old_layout() {
  rm -f "$APPDIR/bin/encluca" "$APPDIR/bin/encluca.new" "$APPDIR/encluca.png" \
        "$HOME/Applications/Encluca.webloc" "$HOME/Desktop/Encluca.webloc"
  rmdir "$APPDIR/bin" 2>/dev/null || true
}

install_agent() {
  [ -x "$HERE/encluca" ] || { echo "Binario non trovato: $HERE/encluca"; exit 1; }
  VER="$("$HERE/encluca" version | awk '{print $2}')"
  mkdir -p "$APPDIR" "$HOME/Library/LaunchAgents" "$APPS"
  launchctl bootout "gui/$UID_NUM/$LABEL" 2>/dev/null || true

  # costruzione dell'app
  rm -rf "$APP"
  mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
  cp "$HERE/encluca" "$BIN"
  chmod 755 "$BIN"
  [ -f "$HERE/encluca.icns" ] && cp "$HERE/encluca.icns" "$APP/Contents/Resources/encluca.icns"
  cat > "$APP/Contents/Info.plist" <<PL
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key><string>$LABEL</string>
  <key>CFBundleName</key><string>Encluca</string>
  <key>CFBundleDisplayName</key><string>Encluca</string>
  <key>CFBundleExecutable</key><string>encluca</string>
  <key>CFBundleIconFile</key><string>encluca</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VER</string>
  <key>CFBundleVersion</key><string>$VER</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>LSUIElement</key><true/>
  <key>NSLocalNetworkUsageDescription</key><string>Encluca sincronizza la cassaforte cifrata con il NAS della rete di casa.</string>
</dict>
</plist>
PL
  # il programma non è firmato da Apple: togliamo la quarantena del download e
  # firmiamo l'app "ad hoc" (firma locale, senza certificato) così macOS la riconosce come tale
  xattr -dr com.apple.quarantine "$APP" 2>/dev/null || true
  codesign --force --sign - "$APP" 2>/dev/null || echo "Avviso: firma locale dell'app non riuscita (l'app funziona lo stesso)."

  remove_old_layout

  cat > "$PLIST" <<EOP
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key>
  <array><string>$BIN</string><string>client</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ExitTimeOut</key><integer>120</integer>
  <key>StandardErrorPath</key><string>$APPDIR/launchd.log</string>
  <key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
EOP
  launchctl bootstrap "gui/$UID_NUM" "$PLIST"
  launchctl kickstart -k "gui/$UID_NUM/$LABEL"
  sleep 1
  echo "Encluca $VER installato in $APP e avviato."
  echo "Lo trovi in Applicazioni, Launchpad e Spotlight. Interfaccia: http://127.0.0.1:8484"
  echo "Se macOS chiede l'accesso alla rete locale, rispondi Consenti (serve per raggiungere il NAS)."
  open http://127.0.0.1:8484 || true
}

uninstall_agent() {
  launchctl bootout "gui/$UID_NUM/$LABEL" 2>/dev/null || true
  rm -f "$PLIST"
  rm -rf "/Applications/Encluca.app" "$HOME/Applications/Encluca.app"
  remove_old_layout
  echo "Encluca rimosso. I dati cifrati restano in \"$APPDIR\"."
}

case "${1:-install}" in
  install) install_agent ;;
  uninstall) uninstall_agent ;;
  *) echo "Uso: $0 [install|uninstall]"; exit 2 ;;
esac
