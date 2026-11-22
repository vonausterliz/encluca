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

func (c *Client) trayWanted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.cfg.TrayOff
}

// SetTray attiva o disattiva l'icona nella barra ("Esci dall'icona" la disattiva).
func (c *Client) SetTray(on bool) error {
	c.mu.Lock()
	c.cfg.TrayOff = !on
	err := saveJSON(c.cfgPath(), &c.cfg)
	c.mu.Unlock()
	if err == nil && on && trayStartEnabled {
		go startTray(c)
	}
	return err
}

// trayStartEnabled: solo il servizio vero avvia processi tray (non i test)
var trayStartEnabled bool

// startTray è chiamata all'avvio del servizio vero (non nei test).
func startTray(c *Client) {
	// ENCLUCA_NO_TRAY: servizi di prova e test end-to-end non lasciano icone sul desktop
	if !c.trayWanted() || os.Getenv("ENCLUCA_NO_TRAY") != "" {
		return
	}
	exe := updateExePath() // su Windows, dopo un aggiornamento, os.Executable è il file vecchio
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return
		}
	}
	if _, err := ensureTrayAutostart(c, exe); err != nil {
		c.log.Printf(T("Avvio automatico dell'icona non configurato: %v"), err)
	}
	if err := startTrayChild(exe, c.dir); err != nil {
		c.log.Printf(T("Icona nella barra non avviata: %v"), err)
	}
}
