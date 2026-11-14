//go:build !windows

package main

import (
	"os"
	"syscall"
)

// ownedByMe dice se il file appartiene all'utente che esegue il programma.
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return !ok || int(st.Uid) == os.Getuid()
}

// restrictDir toglie ogni accesso agli altri utenti.
func restrictDir(dir string) error { return os.Chmod(dir, 0o700) }
