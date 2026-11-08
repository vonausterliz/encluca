#!/usr/bin/env bash
# CassaSync - installazione su macOS come LaunchAgent (si avvia al login, si riavvia se si ferma).
#   ./install.sh            installa e avvia
#   ./install.sh uninstall  rimuove (i dati cifrati restano in ~/Library/Application Support/CassaSync)
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
APPDIR="$HOME/Library/Application Support/CassaSync"
BIN="$APPDIR/bin/cassasync"
PLIST="$HOME/Library/LaunchAgents/it.cassasync.client.plist"
LABEL="it.cassasync.client"
UID_NUM="$(id -u)"

install_agent() {
  [ -x "$HERE/cassasync" ] || { echo "Binario non trovato: $HERE/cassasync"; exit 1; }
  mkdir -p "$APPDIR/bin" "$HOME/Library/LaunchAgents"
  launchctl bootout "gui/$UID_NUM/$LABEL" 2>/dev/null || true
  cp "$HERE/cassasync" "$BIN"
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
  printf '%s\n' "$WEBLOC" > "$HOME/Applications/CassaSync.webloc"
  printf '%s\n' "$WEBLOC" > "$HOME/Desktop/CassaSync.webloc"
  sleep 1
  echo "CassaSync installato e avviato. Interfaccia: http://127.0.0.1:8484"
  open http://127.0.0.1:8484 || true
}

uninstall_agent() {
  launchctl bootout "gui/$UID_NUM/$LABEL" 2>/dev/null || true
  rm -f "$PLIST" "$BIN" "$HOME/Applications/CassaSync.webloc" "$HOME/Desktop/CassaSync.webloc"
  echo "CassaSync rimosso. I dati cifrati restano in \"$APPDIR\"."
}

case "${1:-install}" in
  install) install_agent ;;
  uninstall) uninstall_agent ;;
  *) echo "Uso: $0 [install|uninstall]"; exit 2 ;;
esac
