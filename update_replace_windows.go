//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// replaceExe: su Windows il programma in esecuzione non si può sovrascrivere ma
// si può rinominare. Il vecchio diventa exe+".old" (cancellato al prossimo avvio).
func replaceExe(exe, newFile string) error {
	tmp := exe + ".new"
	if err := copyFileSync(newFile, tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Remove(exe + ".old")
	if err := os.Rename(exe, exe+".old"); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Rename(exe+".old", exe) // rimette a posto il programma in uso
		os.Remove(tmp)
		return err
	}
	return nil
}

// relaunchSelf avvia il nuovo programma con gli stessi argomenti: l'attività
// pianificata di Windows non riavvia un processo uscito con errore.
func relaunchSelf(exe string) error {
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
	return cmd.Start()
}
