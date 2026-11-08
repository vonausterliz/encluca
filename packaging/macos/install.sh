#!/usr/bin/env bash
# Encluca - installazione su macOS come LaunchAgent (si avvia al login, si riavvia se si ferma).
#   ./install.sh            installa e avvia
#   ./install.sh uninstall  rimuove (i dati cifrati restano in ~/Library/Application Support/Encluca)
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
APPDIR="$HOME/Library/Application Support/Encluca"
BIN="$APPDIR/bin/encluca"
PLIST="$HOME/Library/LaunchAgents/it.encluca.client.plist"
LABEL="it.encluca.client"
UID_NUM="$(id -u)"

install_agent() {
  [ -x "$HERE/encluca" ] || { echo "Binario non trovato: $HERE/encluca"; exit 1; }
  mkdir -p "$APPDIR/bin" "$HOME/Library/LaunchAgents"
  launchctl bootout "gui/$UID_NUM/$LABEL" 2>/dev/null || true
  cp "$HERE/encluca" "$BIN"
  chmod 755 "$BIN"
  # il binario non è firmato da Apple: togliamo la quarantena del download
  xattr -d com.apple.quarantine "$BIN" 2>/dev/null || true
  cat > "$PLIST" <<EOF
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
EOF
  launchctl bootstrap "gui/$UID_NUM" "$PLIST"
  launchctl kickstart -k "gui/$UID_NUM/$LABEL"

  WEBLOC='<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>URL</key><string>http://127.0.0.1:8484</string></dict></plist>'
  mkdir -p "$HOME/Applications"
  printf '%s\n' "$WEBLOC" > "$HOME/Applications/Encluca.webloc"
  printf '%s\n' "$WEBLOC" > "$HOME/Desktop/Encluca.webloc"
  sleep 1
  echo "Encluca installato e avviato. Interfaccia: http://127.0.0.1:8484"
  open http://127.0.0.1:8484 || true
}

uninstall_agent() {
  launchctl bootout "gui/$UID_NUM/$LABEL" 2>/dev/null || true
  rm -f "$PLIST" "$BIN" "$HOME/Applications/Encluca.webloc" "$HOME/Desktop/Encluca.webloc"
  echo "Encluca rimosso. I dati cifrati restano in \"$APPDIR\"."
}

case "${1:-install}" in
  install) install_agent ;;
  uninstall) uninstall_agent ;;
  *) echo "Uso: $0 [install|uninstall]"; exit 2 ;;
esac
