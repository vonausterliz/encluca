package main

import (
	"path/filepath"
	"strings"
)

// Su macOS il client è installato come Encluca.app (vedi packaging/macos/install.sh):
// il programma vero sta in Encluca.app/Contents/MacOS/encluca e il LaunchAgent lo
// avvia da lì. Così macOS lo tratta come un'app: compare in Applicazioni, Launchpad
// e Spotlight, e il permesso "Rete locale" è legato all'app.

// macAgentLabel è il nome del LaunchAgent creato da install.sh.
const macAgentLabel = "it.encluca.client"

// appBundleOf restituisce la cartella .app che contiene exe, se exe è
// l'eseguibile principale di un'app (X.app/Contents/MacOS/<nome>); altrimenti "".
func appBundleOf(exe string) string {
	if exe == "" {
		return ""
	}
	macos := filepath.Dir(exe)
	contents := filepath.Dir(macos)
	app := filepath.Dir(contents)
	if filepath.Base(macos) != "MacOS" || filepath.Base(contents) != "Contents" || !strings.HasSuffix(app, ".app") {
		return ""
	}
	return app
}
