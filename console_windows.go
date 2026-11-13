//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// Su Windows il binario è compilato con -H=windowsgui (il servizio non deve aprire una finestra
// nera), quindi non ha una console e i comandi da terminale non stamperebbero nulla.
// attachParentConsole si aggancia alla console del terminale che ha lanciato il comando
// (cmd/PowerShell) e vi ricollega stdin/stdout/stderr. Se l'output è già rediretto
// (es. "encluca status > stato.txt") la redirezione resta com'è. Se non c'è un terminale
// padre (doppio clic, attività pianificata) non fa nulla.
func attachParentConsole() {
	const attachParentProcess = ^uint32(0) // ATTACH_PARENT_PROCESS = (DWORD)-1
	attach := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")
	if r, _, _ := attach.Call(uintptr(attachParentProcess)); r == 0 {
		return
	}
	open := func(name string, std uint32) *os.File {
		p, _ := windows.UTF16PtrFromString(name)
		h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			return nil
		}
		windows.SetStdHandle(std, h)
		return os.NewFile(uintptr(h), name)
	}
	missing := func(f *os.File) bool {
		h := windows.Handle(f.Fd())
		return h == 0 || h == windows.InvalidHandle
	}
	if missing(os.Stdout) {
		if f := open("CONOUT$", windows.STD_OUTPUT_HANDLE); f != nil {
			os.Stdout = f
		}
	}
	if missing(os.Stderr) {
		if f := open("CONOUT$", windows.STD_ERROR_HANDLE); f != nil {
			os.Stderr = f
		}
	}
	if missing(os.Stdin) {
		if f := open("CONIN$", windows.STD_INPUT_HANDLE); f != nil {
			os.Stdin = f
		}
	}
}
