package main

import (
	"os/exec"
	"syscall"
)

const createBreakawayFromJob = 0x01000000

func ensureTrayAutostart(c *Client, exe string) (bool, error) { return false, nil }

// startTrayChild: il servizio gira già nella sessione dell'utente (attività al login). Il tray
// esce dal "job" dell'attività pianificata, se consentito, per sopravvivere ai suoi riavvii.
func startTrayChild(exe, dataDir string) error {
	for _, flags := range []uint32{createNoWindow | detachedProcess | createBreakawayFromJob, createNoWindow | detachedProcess} {
		cmd := exec.Command(exe, "tray", "--data", dataDir)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		if err := cmd.Start(); err == nil {
			go cmd.Wait()
			return nil
		}
	}
	return exec.ErrNotFound
}
