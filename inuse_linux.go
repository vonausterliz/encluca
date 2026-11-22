package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// openByProcesses legge /proc/PID/fd: si vedono solo i processi dello stesso utente,
// che sono gli unici che possono aprire la cartella (permessi 0700).
func openByProcesses(work string) ([]inUseFile, error) {
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	var out []inUseFile
	for _, p := range procs {
		pid, err := strconv.Atoi(p.Name())
		if err != nil || pid == self {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", p.Name(), "fd"))
		if err != nil {
			continue // processo di un altro utente o già terminato
		}
		var name string
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join("/proc", p.Name(), "fd", fd.Name()))
			if err != nil {
				continue
			}
			rel, ok := relInWork(work, target)
			if !ok {
				continue
			}
			if info, err := os.Stat(target); err != nil || !info.Mode().IsRegular() {
				continue
			}
			if name == "" {
				b, _ := os.ReadFile(filepath.Join("/proc", p.Name(), "comm"))
				name = strings.TrimSpace(string(b))
			}
			if ignoredProc(name) {
				break
			}
			out = append(out, inUseFile{Path: rel, Proc: name})
		}
	}
	return out, nil
}
