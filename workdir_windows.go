//go:build windows

package main

import "os"

// Su Windows la cartella sta nel profilo dell'utente, protetto dai permessi del profilo.
func ownedByMe(fi os.FileInfo) bool { return true }

func restrictDir(dir string) error { return nil }
