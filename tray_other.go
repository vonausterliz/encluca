//go:build !linux && !windows

package main

import (
	"context"
	"fmt"
)

func runTray(ctx context.Context, dataDir string) error {
	fmt.Println(T("l'icona nella barra non è disponibile su macOS"))
	return nil
}
