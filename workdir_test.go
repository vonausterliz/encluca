//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
)

// La cartella aperta contiene i file in chiaro: deve essere una cartella vera, di questo
// utente e non scrivibile da altri (un altro utente del PC potrebbe averla preparata lui).
func TestOpenRefusesUnsafeWorkDir(t *testing.T) {
	env := newTestEnv(t)
	a := env.newClient("A")
	if err := a.CreateVault(dirPw); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	// collegamento simbolico al posto della cartella
	target := t.TempDir()
	if err := os.Symlink(target, a.cfg.WorkDir); err != nil {
		t.Fatal(err)
	}
	if err := a.Open(dirPw); err == nil || !strings.Contains(err.Error(), "collegamento") {
		t.Fatalf("aperta su un collegamento simbolico: %v", err)
	}
	if ents, _ := os.ReadDir(target); len(ents) != 0 {
		t.Fatal("scritto nella destinazione del collegamento")
	}
	os.Remove(a.cfg.WorkDir)
	// cartella già esistente e scrivibile da tutti: i permessi vengono ristretti
	os.Mkdir(a.cfg.WorkDir, 0o777)
	os.Chmod(a.cfg.WorkDir, 0o777)
	if err := a.Open(dirPw); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(a.cfg.WorkDir)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("permessi della cartella aperta: %o", fi.Mode().Perm())
	}
}
