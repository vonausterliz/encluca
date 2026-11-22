//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFileExclusive: flock è legato al file aperto e viene rilasciato alla chiusura o alla
// morte del processo; due aperture distinte (anche nello stesso processo) si escludono.
func lockFileExclusive(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
