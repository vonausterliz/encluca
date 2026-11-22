package main

import (
	"errors"
	"io/fs"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// openByProcesses prova ad aprire ogni file senza condivisione: se un programma lo tiene
// aperto Windows risponde ERROR_SHARING_VIOLATION. Il nome del programma non è noto.
func openByProcesses(work string) ([]inUseFile, error) {
	var out []inUseFile
	err := filepath.WalkDir(work, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		name, err := windows.UTF16PtrFromString(p)
		if err != nil {
			return nil
		}
		h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			windows.CloseHandle(h)
			return nil
		}
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			if rel, ok := relInWork(work, p); ok {
				out = append(out, inUseFile{Path: rel})
			}
		}
		return nil
	})
	return out, err
}
