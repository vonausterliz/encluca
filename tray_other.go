//go:build !linux && !windows

package main

import "fmt"

func runTray(dataDir string) error {
	fmt.Println(T("l'icona nella barra non è disponibile su macOS"))
	return nil
}
