package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Modello dell'icona nella barra: logica pura (stato dell'icona, menu, notifiche, blocco
// schermo), separata dalla parte grafica che dipende dal sistema operativo.

// trayStatus è la parte di /api/status che serve all'icona.
type trayStatus struct {
	Version     string  `json:"version"`
	Exe         string  `json:"exe"` // binario del servizio (su Windows diverso da os.Executable dopo un aggiornamento)
	Lang        string  `json:"lang"`
	Open        bool    `json:"open"`
	StaleOpen   bool    `json:"staleOpen"`
	Pending     int     `json:"pending"`
	CloseOnLock *bool   `json:"closeOnLock"`
	Tray        *bool   `json:"tray"` // nil = servizio che non conosce l'impostazione
	Alerts      []alert `json:"alerts"`
}

type trayState struct {
	Reachable bool // il servizio risponde
	Status    trayStatus
}

type trayView struct {
	Icon                                      string // "closed" | "open" | "problem"
	StatusLine                                string
	CanOpen, CanClose, CanFolder, CanPostpone bool
}

func viewFor(s trayState) trayView {
	if !s.Reachable {
		return trayView{Icon: "problem", StatusLine: T("Servizio encluca non raggiungibile")}
	}
	st := s.Status
	v := trayView{Icon: "closed", CanOpen: !st.Open, CanClose: st.Open, CanFolder: st.Open}
	switch {
	case st.Open:
		v.Icon = "open"
		v.StatusLine = T("Cassaforte aperta")
		if st.Pending > 0 {
			v.StatusLine += " · " + fmt.Sprintf(T("%d in coda"), st.Pending)
		}
	case st.StaleOpen:
		v.StatusLine = T("Cassaforte rimasta aperta")
	default:
		v.StatusLine = T("Cassaforte chiusa")
	}
	for _, a := range st.Alerts {
		if a.Level == "warn" {
			v.Icon = "problem"
		}
		if a.ID == "autoClose" {
			v.CanPostpone = true
		}
	}
	return v
}

// trayNotifier decide le notifiche: una quando un ID compare, "risolto" solo per gli avvisi
// che ne hanno senso (NAS, backup). Per i conflitti l'ID contiene il numero: si notifica
// solo quando il numero supera il massimo già notificato (un conflitto risolto non avvisa).
type trayNotifier struct {
	seen         map[string]alert
	maxConflicts int
}

func (n *trayNotifier) diff(alerts []alert) (show, resolved []alert) {
	now := map[string]alert{}
	for _, a := range alerts {
		now[a.ID] = a
		if _, ok := n.seen[a.ID]; ok {
			continue
		}
		if c, ok := strings.CutPrefix(a.ID, "conflicts-"); ok {
			k, _ := strconv.Atoi(c)
			if k <= n.maxConflicts {
				continue
			}
			n.maxConflicts = k
		}
		show = append(show, a)
	}
	for id, a := range n.seen {
		if _, ok := now[id]; !ok && (id == "offline" || id == "backup") {
			resolved = append(resolved, a)
		}
	}
	n.seen = now
	return show, resolved
}

// relaunchTarget: il servizio è stato aggiornato e il tray va rilanciato con il binario
// nuovo. Al massimo una volta per versione (already = versione per cui ci si è già
// rilanciati): se il binario rilanciato è ancora diverso non si va in ciclo.
func relaunchTarget(st trayStatus, already string) (string, bool) {
	if st.Version == "" || st.Version == Version || st.Version == already {
		return "", false
	}
	return st.Exe, true
}

// trayShouldQuit: l'utente ha disattivato l'icona (anche quella avviata dall'autostart esce).
func trayShouldQuit(st trayStatus) bool { return st.Tray != nil && !*st.Tray }

// markupEscape: i demoni di notifica (xfce4-notifyd, GNOME) interpretano il testo come
// markup; un nome di file con "&" farebbe sparire il messaggio.
func markupEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// lockAction: al blocco dello schermo si chiude se la cassaforte è aperta e l'opzione è attiva.
func lockAction(s trayState) bool {
	return s.Reachable && s.Status.Open && (s.Status.CloseOnLock == nil || *s.Status.CloseOnLock)
}
