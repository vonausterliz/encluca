package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// openByProcesses usa lsof (presente di serie su macOS) sulla cartella aperta.
func openByProcesses(work string) ([]inUseFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-n", "-F", "pcn", "+D", work).Output()
	var ee *exec.ExitError
	if err != nil && !(errors.As(err, &ee) && len(raw) == 0) { // uscita 1 senza output: nessun file aperto
		return nil, err
	}
	self := os.Getpid()
	var out []inUseFile
	pid, name := 0, ""
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		l := sc.Text()
		if l == "" {
			continue
		}
		switch l[0] {
		case 'p':
			pid, _ = strconv.Atoi(l[1:])
			name = ""
		case 'c':
			name = l[1:]
		case 'n':
			if pid == self || ignoredProc(name) {
				continue
			}
			rel, ok := relInWork(work, l[1:])
			if !ok {
				continue
			}
			if info, err := os.Stat(l[1:]); err != nil || !info.Mode().IsRegular() {
				continue
			}
			out = append(out, inUseFile{Path: rel, Proc: name})
		}
	}
	return out, nil
}
