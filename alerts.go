package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Avvisi: calcolati dallo stato a ogni richiesta. Ogni condizione ha un ID stabile: chi
// notifica (l'icona nella barra) avvisa quando un ID compare e "risolto" quando sparisce.

// backupInfo è lo stato del backup su Google Drive che l'hub comunica ai dispositivi
// (solo data ed esito: nessun nome di file né altro della cassaforte).
type backupInfo struct {
	Enabled bool      `json:"enabled"` // backup autorizzato sul NAS
	LastOK  time.Time `json:"lastOk"`
	Error   string    `json:"error,omitempty"`
}

type alert struct {
	ID    string    `json:"id"`
	Level string    `json:"level"` // "warn" | "info"
	Text  string    `json:"text"`
	Since time.Time `json:"since,omitempty"`
}

const (
	alertOfflineDefault = 2  // ore
	alertBackupDefault  = 24 // ore
	alertMaxHours       = 720
	backupCheckInterval = 10 * time.Minute
	updateAlertFor      = 24 * time.Hour
)

func hoursOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// humanDur: "5 ore" fino a 48 ore, poi "3 giorni".
func humanDur(d time.Duration) string {
	h := int(d.Hours())
	if h < 1 {
		return fmt.Sprintf(T("%d minuti"), int(d.Minutes()))
	}
	if h <= 48 {
		return fmt.Sprintf(T("%d ore"), h)
	}
	return fmt.Sprintf(T("%d giorni"), h/24)
}

func (c *Client) alertsLocked(now time.Time) []alert {
	var out []alert
	add := func(id, level, text string, since time.Time) {
		out = append(out, alert{ID: id, Level: level, Text: text, Since: since})
	}
	if c.revoked {
		add("revoked", "warn", T("Questo computer è stato scollegato dal NAS"), time.Time{})
	}
	if c.cip == nil && c.st.Open {
		add("staleOpen", "warn", T("La cassaforte è rimasta aperta dopo un riavvio: aprila con la password e poi chiudila"), time.Time{})
	}
	if c.incompat != "" {
		add("incompatible", "warn", c.incompat, time.Time{})
	}
	if h := hoursOr(c.cfg.AlertOfflineHours, alertOfflineDefault); h > 0 && c.cfg.Token != "" && !c.revoked && !c.online && !c.offlineSince.IsZero() &&
		now.Sub(c.offlineSince) >= time.Duration(h)*time.Hour {
		add("offline", "warn", fmt.Sprintf(T("Il NAS non risponde da %s"), humanDur(now.Sub(c.offlineSince))), c.offlineSince)
	}
	if h := hoursOr(c.cfg.AlertBackupHours, alertBackupDefault); h > 0 && c.backup != nil && c.backup.Enabled {
		b := c.backup
		if b.LastOK.IsZero() {
			add("backup", "warn", T("Il backup su Google Drive non è mai riuscito"), time.Time{})
		} else if now.Sub(b.LastOK) >= time.Duration(h)*time.Hour {
			txt := fmt.Sprintf(T("Ultimo backup su Google Drive riuscito %s fa"), humanDur(now.Sub(b.LastOK)))
			if b.Error != "" {
				txt += ": " + b.Error
			}
			add("backup", "warn", txt, b.LastOK)
		}
	}
	if c.massDel > 0 {
		add("massDelete", "warn", fmt.Sprintf(T("%d eliminazioni in attesa di conferma"), c.massDel), time.Time{})
	}
	if c.massRecvDel > 0 {
		add("massRecvDelete", "warn", fmt.Sprintf(T("%d eliminazioni arrivate da un altro dispositivo in attesa di conferma"), c.massRecvDel), time.Time{})
	}
	n := len(c.conflicts)
	for _, st := range c.st.Files {
		if st.Conflict > 0 {
			n++
		}
	}
	if n > 0 {
		// l'ID cambia con il numero: un conflitto nuovo è un avviso nuovo
		add(fmt.Sprintf("conflicts-%d", n), "info", fmt.Sprintf(T("%d conflitti tra versioni"), n), time.Time{})
	}
	if ac := c.autoCloseStatusLocked(now); ac.Warn {
		add("autoClose", "info", fmt.Sprintf(T("La cassaforte si chiude da sola alle %s"), ac.CloseAt.Local().Format("15:04")), ac.CloseAt)
	}
	if c.upd != nil && c.upd.state().VerifyFailed {
		add("updateVerify", "warn", T("Aggiornamento rifiutato: verifica della firma non riuscita"), time.Time{})
	}
	if c.updatedTo != "" && now.Sub(c.updatedAt) < updateAlertFor {
		add("update-"+c.updatedTo, "info", fmt.Sprintf(T("Aggiornamento installato: versione %s"), c.updatedTo), c.updatedAt)
	}
	return out
}

func (c *Client) alertCfgLocked() map[string]int {
	return map[string]int{
		"offlineHours": hoursOr(c.cfg.AlertOfflineHours, alertOfflineDefault),
		"backupHours":  hoursOr(c.cfg.AlertBackupHours, alertBackupDefault),
	}
}

// refreshBackup chiede al NAS lo stato del backup. 404 = hub di una versione che non lo
// espone: l'avviso resta disattivato. Altri errori (NAS spento): resta l'ultimo valore noto.
func (c *Client) refreshBackup(ctx context.Context) {
	var bi backupInfo
	err := c.getJSON(ctx, "/api/v1/backup", nil, &bi)
	var he *httpStatusErr
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case err == nil:
		c.backup = &bi
	case errors.As(err, &he) && he.Code == http.StatusNotFound:
		c.backup = nil
	}
}

func (c *Client) backupLoop(ctx context.Context) {
	for {
		if c.paired() {
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			c.refreshBackup(rctx)
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backupCheckInterval):
		}
	}
}

func (c *Client) noteUpdated(to string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updatedTo, c.updatedAt = to, time.Now()
}

// SetAlerts imposta le soglie in ore (0 = avviso disattivato).
func (c *Client) SetAlerts(offlineHours, backupHours int) error {
	if offlineHours < 0 || offlineHours > alertMaxHours || backupHours < 0 || backupHours > alertMaxHours {
		return errors.New(T("indica un numero di ore tra 0 e 720"))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.AlertOfflineHours, c.cfg.AlertBackupHours = &offlineHours, &backupHours
	return saveJSON(c.cfgPath(), &c.cfg)
}

var urlRe = regexp.MustCompile(`https?://[^\s"/]+[^\s"]*`)

// cleanBackupErr riduce l'errore del backup a una riga breve per avvisi e notifiche: gli
// indirizzi restano solo come host (un URL di caricamento di Drive vale come credenziale).
func cleanBackupErr(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = urlRe.ReplaceAllStringFunc(s, func(u string) string {
		if p, err := url.Parse(u); err == nil && p.Host != "" {
			return p.Scheme + "://" + p.Host
		}
		return "…"
	})
	if r := []rune(strings.TrimSpace(s)); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return strings.TrimSpace(s)
}
