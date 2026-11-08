#!/usr/bin/env bash
# Encluca - installazione su Linux come servizio systemd.
#   ./install.sh               installa il client per l'utente corrente (servizio utente)
#   ./install.sh hub           installa l'hub come servizio di sistema (richiede sudo)
#   ./install.sh uninstall     rimuove il client (i dati cifrati restano in ~/.local/share/encluca)
#   ./install.sh uninstall-hub rimuove l'hub (i dati restano in /var/lib/encluca)
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
BIN_SRC="$HERE/encluca"
MODE="${1:-client}"

client_install() {
  [ -x "$BIN_SRC" ] || { echo "Binario non trovato: $BIN_SRC"; exit 1; }
  if [ "$(id -u)" = 0 ]; then echo "Esegui il client come utente normale, non come root."; exit 1; fi
  install -Dm755 "$BIN_SRC" "$HOME/.local/bin/encluca"
  mkdir -p "$HOME/.config/systemd/user"
  cat > "$HOME/.config/systemd/user/encluca.service" <<EOF
[Unit]
Description=Encluca - cassaforte cifrata sincronizzata
After=network-online.target

[Service]
ExecStart=%h/.local/bin/encluca client
Restart=on-failure
RestartSec=5
# tempo per cifrare e chiudere la cassaforte allo spegnimento
TimeoutStopSec=120

[Install]
WantedBy=default.target
EOF
  systemctl --user daemon-reload
  systemctl --user enable --now encluca.service
  systemctl --user restart encluca.service

  mkdir -p "$HOME/.local/share/applications"
  cat > "$HOME/.local/share/applications/encluca.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Encluca
Comment=Apri o chiudi la cassaforte cifrata
Exec=xdg-open http://127.0.0.1:8484
Icon=folder-locked
Terminal=false
Categories=Utility;Security;
EOF
  DESKTOP_DIR="$(xdg-user-dir DESKTOP 2>/dev/null || echo "$HOME/Desktop")"
  if [ -d "$DESKTOP_DIR" ]; then
    cp "$HOME/.local/share/applications/encluca.desktop" "$DESKTOP_DIR/Encluca.desktop"
    chmod +x "$DESKTOP_DIR/Encluca.desktop"
    gio set "$DESKTOP_DIR/Encluca.desktop" metadata::trusted true 2>/dev/null || true
  fi
  sleep 1
  if systemctl --user is-active --quiet encluca.service; then
    echo "Encluca installato e avviato. Interfaccia: http://127.0.0.1:8484"
    xdg-open http://127.0.0.1:8484 >/dev/null 2>&1 || true
  else
    echo "Il servizio non si è avviato. Controlla con: journalctl --user -u encluca -n 50"
    exit 1
  fi
}

client_uninstall() {
  systemctl --user disable --now encluca.service 2>/dev/null || true
  rm -f "$HOME/.config/systemd/user/encluca.service" "$HOME/.local/bin/encluca" \
        "$HOME/.local/share/applications/encluca.desktop"
  DESKTOP_DIR="$(xdg-user-dir DESKTOP 2>/dev/null || echo "$HOME/Desktop")"
  rm -f "$DESKTOP_DIR/Encluca.desktop"
  systemctl --user daemon-reload
  echo "Encluca rimosso. I dati cifrati restano in ~/.local/share/encluca (cancellali a mano se vuoi)."
}

hub_install() {
  [ -x "$BIN_SRC" ] || { echo "Binario non trovato: $BIN_SRC"; exit 1; }
  sudo install -Dm755 "$BIN_SRC" /usr/local/bin/encluca
  id encluca >/dev/null 2>&1 || sudo useradd --system --home /var/lib/encluca --shell /usr/sbin/nologin encluca
  sudo install -d -o encluca -g encluca -m 700 /var/lib/encluca
  sudo tee /etc/systemd/system/encluca-hub.service >/dev/null <<EOF
[Unit]
Description=Encluca hub (NAS)
After=network-online.target
Wants=network-online.target

[Service]
User=encluca
Group=encluca
ExecStart=/usr/local/bin/encluca hub --data /var/lib/encluca --listen :8443
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/encluca
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
  sudo systemctl daemon-reload
  sudo systemctl enable --now encluca-hub.service
  sudo systemctl restart encluca-hub.service
  sleep 2
  echo "Hub avviato: https://$(hostname -I | awk '{print $1}'):8443"
  if sudo test -f /var/lib/encluca/admin-password.txt; then
    echo "Password amministratore iniziale: $(sudo cat /var/lib/encluca/admin-password.txt)"
  fi
}

hub_uninstall() {
  sudo systemctl disable --now encluca-hub.service 2>/dev/null || true
  sudo rm -f /etc/systemd/system/encluca-hub.service /usr/local/bin/encluca
  sudo systemctl daemon-reload
  echo "Hub rimosso. I dati cifrati restano in /var/lib/encluca."
}

case "$MODE" in
  client) client_install ;;
  hub) hub_install ;;
  uninstall) client_uninstall ;;
  uninstall-hub) hub_uninstall ;;
  *) echo "Uso: $0 [client|hub|uninstall|uninstall-hub]"; exit 2 ;;
esac
