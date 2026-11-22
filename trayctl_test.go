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
