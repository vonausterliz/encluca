package main

import "os"

// Avvio dell'icona nella barra da parte del servizio, così arriva anche ai computer che si
// aggiornano da soli (senza reinstallare). Un solo tray per utente (tray.lock).

// SetCloseOnLock attiva o disattiva la chiusura della cassaforte al blocco dello schermo o
// alla sospensione (la esegue il processo dell'icona).
func (c *Client) SetCloseOnLock(on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.CloseOnLock = &on
	return saveJSON(c.cfgPath(), &c.cfg)
}

func (c *Client) closeOnLockLocked() bool { return c.cfg.CloseOnLock == nil || *c.cfg.CloseOnLock }

// startTray è chiamata all'avvio del servizio vero (non nei test).
func startTray(c *Client) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if _, err := ensureTrayAutostart(c, exe); err != nil {
		c.log.Printf(T("Avvio automatico dell'icona non configurato: %v"), err)
	}
	if err := startTrayChild(exe, c.dir); err != nil {
		c.log.Printf(T("Icona nella barra non avviata: %v"), err)
	}
}
