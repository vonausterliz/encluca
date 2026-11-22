package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// openByProcesses usa lsof (presente di serie su macOS) sulla cartella aperta. "+c 0" chiede
// il nome completo del programma (altrimenti è troncato e gli indicizzatori non si riconoscono).
func openByProcesses(work string) ([]inUseFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-n", "+c", "0", "-F", "pcn", "+D", work).Output()
	if err != nil {
		var ee *exec.ExitError
		// lsof esce con 1 anche quando trova file ma incontra un elemento illeggibile o sparito:
		// il risultato vale comunque. Errore vero solo se non c'è nessuna uscita e lsof si lamenta.
		if !errors.As(err, &ee) || ctx.Err() != nil || len(raw) == 0 && len(ee.Stderr) > 0 {
			return nil, err
		}
	}
	return parseLsof(raw, work, os.Getpid()), nil
}
