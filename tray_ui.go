//go:build linux || windows

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"fyne.io/systray"
)

// runTray avvia l'icona nella barra. Esce subito (senza errore) se un'altra istanza è attiva.
func runTray(ctx context.Context, dataDir string) error {
	release, ok := acquireTrayLock(dataDir)
	if !ok {
		return nil
	}
	defer release()
	t := &trayApp{api: &trayAPI{dir: dataDir}}
	// SIGTERM/Ctrl-C (fine della sessione): main li intercetta per il servizio, qui si esce
	go func() { <-ctx.Done(); systray.Quit() }()
	systray.Run(t.ready, func() {})
	if t.relaunch {
		release()
		if exe, err := os.Executable(); err == nil {
			cmd := exec.Command(exe, "tray")
			detach(cmd)
			return cmd.Start()
		}
	}
	return nil
}

type trayApp struct {
	api      *trayAPI
	mu       sync.Mutex
	notifier trayNotifier
	icon     string
	relaunch bool
	lastRef  time.Time // ultima notifica di chiusura rifiutata

	status, openV, closeV, sync_, folder, ui, postpone, quit *systray.MenuItem
}

func (t *trayApp) ready() {
	systray.SetTitle("Encluca")
	systray.SetTooltip("Encluca")
	t.status = systray.AddMenuItem("…", "")
	t.status.Disable()
	systray.AddSeparator()
	t.openV = systray.AddMenuItem("", "")
	t.closeV = systray.AddMenuItem("", "")
	t.sync_ = systray.AddMenuItem("", "")
	t.folder = systray.AddMenuItem("", "")
	t.ui = systray.AddMenuItem("", "")
	t.postpone = systray.AddMenuItem("", "")
	systray.AddSeparator()
	t.quit = systray.AddMenuItem("", "")
	t.setLabels()
	t.refresh()

	go t.clicks()
	go func() {
		for range time.Tick(trayPoll) {
			t.refresh()
		}
	}()
	go func() {
		if err := watchLock(context.Background(), t.onLock); err != nil {
			// nessuna fonte di eventi: resta solo la chiusura per inattività
			_ = err
		}
	}()
}

func (t *trayApp) setLabels() {
	t.openV.SetTitle(T("Apri cassaforte"))
	t.closeV.SetTitle(T("Chiudi cassaforte"))
	t.sync_.SetTitle(T("Sincronizza ora"))
	t.folder.SetTitle(T("Apri cartella"))
	t.ui.SetTitle(T("Apri interfaccia"))
	t.postpone.SetTitle(T("Rimanda chiusura automatica"))
	t.quit.SetTitle(T("Esci dall'icona"))
}

func show(m *systray.MenuItem, on bool) {
	if on {
		m.Show()
	} else {
		m.Hide()
	}
}

func (t *trayApp) refresh() {
	st, err := t.api.status()
	s := trayState{Reachable: err == nil, Status: st}
	if s.Reachable {
		if st.Lang != "" && st.Lang != getLang() {
			setLang(st.Lang)
		}
		if st.Version != "" && st.Version != Version {
			// il servizio è stato aggiornato: si riparte con il binario nuovo
			t.relaunch = true
			systray.Quit()
			return
		}
	}
	v := viewFor(s)
	t.mu.Lock()
	defer t.mu.Unlock()
	if v.Icon != t.icon {
		t.icon = v.Icon
		if runtime.GOOS == "windows" {
			systray.SetIcon(trayIconICO(v.Icon))
		} else {
			systray.SetIcon(trayIconPNG(v.Icon))
		}
	}
	t.setLabels()
	t.status.SetTitle(v.StatusLine)
	systray.SetTooltip("Encluca · " + v.StatusLine)
	show(t.openV, v.CanOpen)
	show(t.closeV, v.CanClose)
	show(t.folder, v.CanFolder)
	show(t.postpone, v.CanPostpone)
	show(t.sync_, s.Reachable)
	if !s.Reachable {
		return
	}
	shown, resolved := t.notifier.diff(st.Alerts)
	for _, a := range shown {
		notify("Encluca", a.Text)
	}
	for _, a := range resolved {
		switch a.ID {
		case "offline":
			notify("Encluca", T("Il NAS è di nuovo raggiungibile"))
		case "backup":
			notify("Encluca", T("Il backup su Google Drive è di nuovo in regola"))
		}
	}
}

func (t *trayApp) act(path string) {
	if err := t.api.post(path); err != nil {
		notify("Encluca", err.Error())
	}
	t.refresh()
}

func (t *trayApp) clicks() {
	for {
		select {
		case <-t.openV.ClickedCh:
			t.act("/unlock") // la password si digita nell'interfaccia, mai nel menu
		case <-t.ui.ClickedCh:
			t.act("/unlock")
		case <-t.closeV.ClickedCh:
			t.act("/api/close")
		case <-t.sync_.ClickedCh:
			t.act("/api/sync")
		case <-t.folder.ClickedCh:
			t.act("/api/openfolder")
		case <-t.postpone.ClickedCh:
			t.act("/api/autoclose/postpone")
		case <-t.quit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// onLock: schermo bloccato o sospensione. Con file aperti la chiusura è rifiutata dal
// servizio: non si forza, si avvisa (al massimo una volta al minuto).
func (t *trayApp) onLock() {
	err := trayLockedClose(t.api)
	t.refresh()
	if err == nil {
		return
	}
	t.mu.Lock()
	tooSoon := time.Since(t.lastRef) < trayRefusalEvery
	if !tooSoon {
		t.lastRef = time.Now()
	}
	t.mu.Unlock()
	if !tooSoon {
		notify(T("Cassaforte non chiusa"), err.Error())
	}
}

var errNoLockSource = errors.New("nessuna fonte di eventi di blocco disponibile")
