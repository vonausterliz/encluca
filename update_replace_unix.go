//go:build !windows

package main

import "os"

// replaceExe sostituisce exe con newFile in modo atomico: una copia completa
// in exe+".new" e poi una rinomina, così un'interruzione non lascia mai un
// programma a metà.
func replaceExe(exe, newFile string) error {
	tmp := exe + ".new"
	if err := copyFileSync(newFile, tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// relaunchSelf non serve fuori da Windows: systemd e launchd riavviano il
// servizio quando esce con un codice diverso da 0.
func relaunchSelf(exe string) error { return nil }
