package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAutostartCreatedOnce(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("solo Linux")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := newBareClient(t)
	exe := "/opt/prova/encluca"
	created, err := ensureTrayAutostart(c, exe)
	if err != nil || !created {
		t.Fatalf("prima creazione: %v %v", created, err)
	}
	p := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "autostart", "encluca-tray.desktop")
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), `Exec="/opt/prova/encluca" tray --data "`+c.dir+`"`) {
		t.Fatalf("file di avvio: %v\n%s", err, b)
	}
	os.Remove(p) // l'utente non vuole l'icona
	c2, _ := newClient(c.dir)
	if created, _ := ensureTrayAutostart(c2, exe); created {
		t.Fatal("file di avvio ricreato dopo che l'utente l'ha cancellato")
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("file di avvio presente")
	}
}

func TestCloseOnLockDefault(t *testing.T) {
	c := newBareClient(t)
	if v, _ := c.Status()["closeOnLock"].(bool); !v {
		t.Fatal("chiusura al blocco non attiva di predefinito")
	}
	if err := c.SetCloseOnLock(false); err != nil {
		t.Fatal(err)
	}
	c2, _ := newClient(c.dir)
	if v, _ := c2.Status()["closeOnLock"].(bool); v {
		t.Fatal("impostazione non salvata")
	}
}

func TestCloseOnLockAPI(t *testing.T) {
	c := newBareClient(t)
	if code, out := apiCall(t, c, "/api/closeonlock", map[string]any{"On": false}); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if v, _ := c.Status()["closeOnLock"].(bool); v {
		t.Fatal("API non applicata")
	}
}

func TestTrayOffSetting(t *testing.T) {
	c := newBareClient(t)
	if v, _ := c.Status()["tray"].(bool); !v {
		t.Fatal("icona non attiva di predefinito")
	}
	if err := c.SetTray(false); err != nil {
		t.Fatal(err)
	}
	c2, _ := newClient(c.dir)
	if v, _ := c2.Status()["tray"].(bool); v {
		t.Fatal("icona disattivata non salvata")
	}
	if c2.trayWanted() {
		t.Fatal("il servizio avvierebbe l'icona disattivata")
	}
	if code, _ := apiCall(t, c2, "/api/tray", map[string]any{"On": true}); code != 200 || !c2.trayWanted() {
		t.Fatalf("riattivazione via API: %d", code)
	}
}

func TestTrayQuitsWhenOff(t *testing.T) {
	off := false
	if !trayShouldQuit(trayStatus{Tray: &off}) {
		t.Fatal("il tray resta con l'icona disattivata")
	}
	if trayShouldQuit(trayStatus{}) {
		t.Fatal("il tray esce con un servizio che non conosce l'impostazione")
	}
}

// I test che avviano il vero "encluca client" non devono lasciare icone sul desktop.
func TestStartTrayRespectsNoTrayEnv(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("solo Linux")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ENCLUCA_NO_TRAY", "1")
	c := newBareClient(t)
	startTray(c)
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "autostart", "encluca-tray.desktop")); err == nil {
		t.Fatal("con ENCLUCA_NO_TRAY il servizio ha configurato l'icona")
	}
}
