//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// openFromBundle gestisce il doppio clic su Encluca.app: il servizio gira già in
// background (LaunchAgent), quindi lo si risveglia se fermo e si apre l'interfaccia.
// Restituisce false se il programma non è dentro un'app.
func openFromBundle() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	if appBundleOf(exe) == "" {
		return false
	}
	exec.Command("launchctl", "kickstart", fmt.Sprintf("gui/%d/%s", os.Getuid(), macAgentLabel)).Run()
	exec.Command("open", "http://127.0.0.1:8484").Run()
	return true
}

// afterReplace firma di nuovo l'app dopo che l'aggiornamento ne ha sostituito
// l'eseguibile: la firma (ad hoc, senza certificato Apple) copre tutta la cartella
// .app e un file cambiato la invaliderebbe.
func afterReplace(exe string, logf func(string, ...any)) {
	app := appBundleOf(exe)
	if app == "" {
		return
	}
	if out, err := exec.Command("codesign", "--force", "--sign", "-", app).CombinedOutput(); err != nil {
		logf("codesign %s: %v %s", app, err, out)
	}
}
