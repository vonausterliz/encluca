package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ensureTrayAutostart crea una sola volta ~/.config/autostart/encluca-tray.desktop: se poi
// l'utente lo cancella (non vuole l'icona) non viene ricreato.
func ensureTrayAutostart(c *Client, exe string) (bool, error) {
	c.mu.Lock()
	done := c.cfg.TrayAutostart
	c.mu.Unlock()
	if done {
		return false, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false, err
		}
		base = filepath.Join(home, ".config")
	}
	dir := filepath.Join(base, "autostart")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	desk := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=Encluca\nComment=%s\nExec=%q tray --data %q\nIcon=encluca\nTerminal=false\nX-GNOME-Autostart-enabled=true\n",
		T("Icona di Encluca nella barra di sistema"), exe, c.dir)
	if err := writeFileAtomic(filepath.Join(dir, "encluca-tray.desktop"), []byte(desk), 0o644); err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.TrayAutostart = true
	return true, saveJSON(c.cfgPath(), &c.cfg)
}

// startTrayChild avvia il tray fuori dal gruppo di processi del servizio: con
// "systemd-run --scope" ha un'unità propria e non viene terminato quando il servizio si
// riavvia (aggiornamenti). Senza sessione grafica/DBus non si avvia: ci pensa l'autostart.
func startTrayChild(exe, dataDir string) error {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		if _, err := os.Stat(fmt.Sprintf("/run/user/%d/bus", os.Getuid())); err != nil {
			return nil
		}
	}
	args := []string{exe, "tray", "--data", dataDir}
	var cmd *exec.Cmd
	if sr, err := exec.LookPath("systemd-run"); err == nil {
		cmd = exec.Command(sr, append([]string{"--user", "--scope", "--quiet", "--collect", "--unit", fmt.Sprintf("encluca-tray-%d", os.Getpid())}, args...)...)
	} else {
		cmd = exec.Command(args[0], args[1:]...)
		detach(cmd)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
